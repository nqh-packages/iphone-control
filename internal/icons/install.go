package icons

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Install fetches what the manifest file names into home, checks every
// hash, tries the detector on a blank picture and only then makes it home's
// install. Files already there with the right hash are kept. It reports to
// out.
func Install(ctx context.Context, manifestPath, home string, out io.Writer) (Status, error) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return Status{}, err
	}
	m, err := parseManifest(raw)
	if err != nil {
		return Status{}, err
	}
	dir := filepath.Join(home, m.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Status{}, err
	}
	fmt.Fprintf(out, "weights: %s, fetched to this Mac only (never committed, embedded or shipped)\n", m.Licence.Weights)
	for _, f := range []struct {
		from string
		to   installed
	}{{m.Files.Model.URL, m.model()}, {m.Files.Meta.URL, m.meta()}} {
		if verify(dir, f.to) != nil {
			if err := fetch(ctx, f.from, filepath.Join(dir, f.to.name), f.to); err != nil {
				return Status{}, err
			}
		}
		note := ""
		if f.to == m.meta() {
			if err := checkNames(filepath.Join(dir, f.to.name), m); err != nil {
				return Status{}, err
			}
			note = fmt.Sprintf("   (%d class names match the manifest)", len(m.Classes))
		}
		fmt.Fprintf(out, "%-16s%9d B  sha256 %.8s… ok%s\n", f.to.name, f.to.size, f.to.sha256, note)
	}
	r := m.Files.Runtime
	if verify(dir, m.runtime()) != nil {
		if err := fetchMember(ctx, dir, r, m.runtime()); err != nil {
			return Status{}, err
		}
	}
	fmt.Fprintf(out, "%-16s%9d B  sha256 %.8s… ok → %s sha256 %.8s… ok\n", "runtime wheel", r.Size, r.SHA256, m.runtime().name, r.MemberSHA256)
	if id, err := activeID(home); err == nil && id == m.ID {
		if err := os.Remove(filepath.Join(home, activeName)); err != nil {
			return Status{}, err
		}
	}
	if err := replace(filepath.Join(dir, manifestName), raw); err != nil {
		return Status{}, err
	}
	took, err := selfTest(ctx, dir, m)
	if err != nil {
		return Status{}, fmt.Errorf("self-test: %w", err)
	}
	fmt.Fprintf(out, "self-test: blank picture → 0 icons in %d ms\n", took.Milliseconds())
	if err := replace(filepath.Join(home, activeName), []byte(m.ID+"\n")); err != nil {
		return Status{}, err
	}
	fmt.Fprintf(out, "installed: %s\n", m.ID)
	return Check(home), nil
}

func replace(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".part-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err = errors.Join(err, f.Chmod(0o644), f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func store(r io.Reader, path string, want installed) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".part-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, want.size+1))
	if err = errors.Join(err, f.Chmod(0o644), f.Close()); err != nil {
		return fmt.Errorf("%s: %w", want.name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); n != want.size || got != want.sha256 {
		return fmt.Errorf("%s: got %d bytes with sha256 %s, the manifest says %d bytes with %s", want.name, n, got, want.size, want.sha256)
	}
	return os.Rename(f.Name(), path)
}

func fetch(ctx context.Context, from, to string, want installed) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, from, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", want.name, err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", want.name, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: the server answered %s", want.name, res.Status)
	}
	return store(res.Body, to, want)
}

func fetchMember(ctx context.Context, dir string, a archive, member installed) error {
	whole := installed{name: ".part-archive", sha256: a.SHA256, size: a.Size}
	path := filepath.Join(dir, whole.name)
	defer os.Remove(path)
	if err := fetch(ctx, a.URL, path, whole); err != nil {
		return fmt.Errorf("runtime archive: %w", err)
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("runtime archive: %w", err)
	}
	defer z.Close()
	r, err := z.Open(a.Member)
	if err != nil {
		return fmt.Errorf("runtime archive: %w", err)
	}
	defer r.Close()
	return store(r, filepath.Join(dir, member.name), member)
}

func checkNames(path string, m *manifest) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var meta struct {
		Names map[string]string `json:"names"`
	}
	name := filepath.Base(path)
	if err := json.Unmarshal(b, &meta); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if len(meta.Names) != len(m.Classes) {
		return fmt.Errorf("%s names %d classes, the manifest %d", name, len(meta.Names), len(m.Classes))
	}
	for i, c := range m.Classes {
		if got := meta.Names[strconv.Itoa(i)]; got != c.Name {
			return fmt.Errorf("%s: class %d is %q, the manifest says %q", name, i, got, c.Name)
		}
	}
	return nil
}

func selfTest(ctx context.Context, dir string, m *manifest) (time.Duration, error) {
	w, h := 375, 667
	if m.Orientation == "landscape" {
		w, h = h, w
	}
	blank := image.NewGray(image.Rect(0, 0, w, h))
	for i := range blank.Pix {
		blank.Pix[i] = 0xff
	}
	var pic bytes.Buffer
	if err := png.Encode(&pic, blank); err != nil {
		return 0, err
	}
	var said strings.Builder
	p, err := startProcess(dir, &said)
	if err != nil {
		return 0, err
	}
	defer context.AfterFunc(ctx, func() { p.cmd.Process.Kill() })()
	took, err := p.tryBlank(m, pic.Bytes())
	p.end()
	if err != nil && said.Len() > 0 {
		err = fmt.Errorf("%w; the detector said: %s", err, strings.TrimSpace(said.String()))
	}
	return took, errors.Join(ctx.Err(), err)
}

func (p *process) tryBlank(m *manifest, blank []byte) (time.Duration, error) {
	if err := p.ready(timings.start, m); err != nil {
		return 0, err
	}
	start := time.Now()
	r, err := p.ask(1, blank, timings.start)
	took := time.Since(start)
	if err != nil {
		return took, err
	}
	if r.Error != "" {
		return took, errors.New(r.Error)
	}
	for _, d := range r.Found {
		if d.Why == "" {
			return took, fmt.Errorf("the detector sees %q on a blank picture", d.Class)
		}
	}
	return took, nil
}
