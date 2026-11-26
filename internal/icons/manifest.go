package icons

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	activeName   = "active"
	manifestName = "manifest.json"
	reinstall    = "run `pinkyctl phone icons install <manifest>`"
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type manifest struct {
	ID      string `json:"id"`
	Licence struct {
		Code    string `json:"code"`
		Weights string `json:"weights"`
	} `json:"licence"`
	Files struct {
		Model   download `json:"model"`
		Meta    download `json:"meta"`
		Runtime archive  `json:"runtime"`
	} `json:"files"`
	Input struct {
		Width  int `json:"width"`
		Height int `json:"height"`
		Pad    int `json:"pad"`
	} `json:"input"`
	Orientation string  `json:"orientation"`
	MinScore    float32 `json:"min_score"`
	NMSIoU      float64 `json:"nms_iou"`
	Threads     int     `json:"threads"`
	Classes     []class `json:"classes"`
}

type download struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type archive struct {
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	Member       string `json:"member"`
	MemberSHA256 string `json:"member_sha256"`
	MemberSize   int64  `json:"member_size"`
}

type class struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	MinScore float32  `json:"min_score,omitempty"`
	Apps     []string `json:"apps,omitempty"`
	Off      bool     `json:"off,omitempty"`
}

type installed struct {
	name   string
	sha256 string
	size   int64
}

func (d download) name() string {
	u, err := url.Parse(d.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return path.Base(u.Path)
}

func (m *manifest) model() installed {
	return installed{m.Files.Model.name(), m.Files.Model.SHA256, m.Files.Model.Size}
}

func (m *manifest) meta() installed {
	return installed{m.Files.Meta.name(), m.Files.Meta.SHA256, m.Files.Meta.Size}
}

func (m *manifest) runtime() installed {
	return installed{path.Base(m.Files.Runtime.Member), m.Files.Runtime.MemberSHA256, m.Files.Runtime.MemberSize}
}

func (m *manifest) minScore(c class) float32 {
	if c.MinScore > 0 {
		return c.MinScore
	}
	return m.MinScore
}

func (m *manifest) fits(in, out []int64) error {
	want := []int64{1, 3, int64(m.Input.Height), int64(m.Input.Width)}
	if !slices.Equal(in, want) {
		return fmt.Errorf("the model takes %v, the manifest says %v", in, want)
	}
	if len(out) != 3 || out[1] != int64(4+len(m.Classes)) {
		return fmt.Errorf("the model answers %v, the manifest's %d classes need [1 %d n]", out, len(m.Classes), 4+len(m.Classes))
	}
	return nil
}

func (m *manifest) validate() error {
	if !safeName.MatchString(m.ID) || m.ID == activeName {
		return fmt.Errorf("id %q is not a folder name", m.ID)
	}
	if m.Licence.Weights == "" {
		return errors.New("licence.weights is missing")
	}
	r := m.Files.Runtime
	for _, f := range []struct {
		what string
		d    download
	}{{"model", m.Files.Model}, {"meta", m.Files.Meta}, {"runtime", download{r.URL, r.SHA256, r.Size}}} {
		if f.d.name() == "" {
			return fmt.Errorf("files.%s.url is not a web address", f.what)
		}
		if !isSHA256(f.d.SHA256) || f.d.Size <= 0 {
			return fmt.Errorf("files.%s needs a sha256 and a size", f.what)
		}
	}
	if r.Member == "" || !isSHA256(r.MemberSHA256) || r.MemberSize <= 0 {
		return errors.New("files.runtime needs a member with its member_sha256 and member_size")
	}
	seenFiles := map[string]bool{manifestName: true}
	for _, f := range []installed{m.model(), m.meta(), m.runtime()} {
		if !safeName.MatchString(f.name) || seenFiles[f.name] {
			return fmt.Errorf("%q cannot name an installed file", f.name)
		}
		seenFiles[f.name] = true
	}
	if m.Input.Width <= 0 || m.Input.Height <= 0 || m.Input.Width > 4096 || m.Input.Height > 4096 || m.Input.Pad < 0 || m.Input.Pad > 255 {
		return errors.New("input needs a width and height up to 4096 and a pad of 0 to 255")
	}
	if m.Orientation != "" && m.Orientation != "portrait" && m.Orientation != "landscape" {
		return fmt.Errorf("orientation %q is neither portrait nor landscape", m.Orientation)
	}
	if !(m.MinScore > 0 && m.MinScore <= 1) || !(m.NMSIoU > 0 && m.NMSIoU <= 1) {
		return errors.New("min_score and nms_iou must lie above 0 and up to 1")
	}
	if m.Threads < 1 || m.Threads > 64 {
		return errors.New("threads must be 1 to 64")
	}
	if len(m.Classes) == 0 {
		return errors.New("classes is empty")
	}
	seen := map[string]bool{}
	for _, c := range m.Classes {
		switch {
		case c.Name == "" || strings.TrimSpace(c.Label) == "":
			return fmt.Errorf("class %q needs a name and a label", c.Name)
		case seen[c.Name]:
			return fmt.Errorf("class %q is listed twice", c.Name)
		case !(c.MinScore >= 0 && c.MinScore <= 1):
			return fmt.Errorf("class %q: min_score %v is outside 0..1", c.Name, c.MinScore)
		case slices.Contains(c.Apps, ""):
			return fmt.Errorf("class %q names an empty app", c.Name)
		}
		seen[c.Name] = true
	}
	return nil
}

func isSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}

func parseManifest(b []byte) (*manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("manifest: text follows the object")
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, nil
}

func load(dir string) (*manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	return parseManifest(b)
}

func activeID(home string) (string, error) {
	b, err := os.ReadFile(filepath.Join(home, activeName))
	return strings.TrimSpace(string(b)), err
}

func active(home string) (m *manifest, dir string, err error) {
	id, err := activeID(home)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", errors.New("not installed")
	}
	if err != nil || !safeName.MatchString(id) {
		return nil, "", fmt.Errorf("%s does not name an install: %s", activeName, reinstall)
	}
	dir = filepath.Join(home, id)
	m, err = load(dir)
	if err != nil || m.ID != id {
		return nil, "", fmt.Errorf("%s of %s is missing or broken: %s", manifestName, id, reinstall)
	}
	for _, f := range []installed{m.model(), m.meta(), m.runtime()} {
		st, err := os.Stat(filepath.Join(dir, f.name))
		if err != nil || !st.Mode().IsRegular() || st.Size() != f.size {
			return nil, "", fmt.Errorf("%s is missing or has the wrong size: %s", f.name, reinstall)
		}
	}
	return m, dir, nil
}

func verify(dir string, f installed) error {
	r, err := os.Open(filepath.Join(dir, f.name))
	if err != nil {
		return fmt.Errorf("%s: %w", f.name, err)
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return fmt.Errorf("%s: %w", f.name, err)
	}
	if hex.EncodeToString(h.Sum(nil)) != f.sha256 {
		return fmt.Errorf("%s: sha256 differs from the manifest: %s", f.name, reinstall)
	}
	return nil
}

// Status is whether a detector is installed in a home folder.
type Status struct {
	Installed bool   `json:"-"`
	ID        string `json:"id,omitempty"`
	Reason    string `json:"-"`
}

// String is "installed", or "off: " and why.
func (s Status) String() string {
	if s.Installed {
		return "installed"
	}
	return "off: " + s.Reason
}

// Check reports whether home holds an install whose files are all there
// with their sizes. The detector process checks their hashes as it loads
// them.
func Check(home string) Status {
	m, _, err := active(home)
	if err != nil {
		return Status{Reason: err.Error()}
	}
	return Status{Installed: true, ID: m.ID}
}
