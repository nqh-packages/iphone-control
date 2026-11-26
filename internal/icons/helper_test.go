package icons

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const helperEnv = "GO_ICONS_HELPER"

func TestMain(m *testing.M) {
	if engine := os.Getenv(helperEnv); engine != "" {
		if engine == "fake" {
			openEngine = openFake
		}
		if err := Serve(os.Args[len(os.Args)-1], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	childCommand = helperCommand("fake")
	os.Exit(m.Run())
}

func helperCommand(engine string) func(string) (*exec.Cmd, error) {
	return func(dir string) (*exec.Cmd, error) {
		cmd := exec.Command(os.Args[0], dir)
		cmd.Env = append(os.Environ(), helperEnv+"="+engine)
		return cmd, nil
	}
}

type fakeEngine struct {
	dir string
	m   *manifest
}

func openFake(_, model string, m *manifest) (engine, error) {
	return fakeEngine{filepath.Dir(model), m}, nil
}

func (f fakeEngine) run(tensor []float32) ([]float32, []int64, error) {
	b, _ := os.ReadFile(filepath.Join(f.dir, "behaviour"))
	verb, arg, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	switch verb {
	case "sleep":
		d, _ := time.ParseDuration(arg)
		time.Sleep(d)
	case "crash":
		os.Exit(3)
	case "hang":
		time.Sleep(time.Hour)
	case "error":
		return nil, nil, errors.New("the fake engine refuses")
	}
	w, h := f.m.Input.Width, f.m.Input.Height
	blank := tensor[h/2*w+w/2] == 1
	anchors := []struct {
		class  int
		score  float32
		cx, cy float32
	}{{0, 0.9, 0.5, 0.25}, {1, 0.45, 0.5, 0.5}, {2, 0.8, 0.5, 0.75}, {0, 0.1, 0.2, 0.2}}
	channels, n := 4+len(f.m.Classes), len(anchors)
	out := make([]float32, channels*n)
	for j, a := range anchors {
		if blank {
			continue
		}
		out[j], out[n+j] = a.cx*float32(w), a.cy*float32(h)
		out[2*n+j], out[3*n+j] = 20, 20
		out[(4+a.class)*n+j] = a.score
	}
	return out, []int64{1, int64(channels), int64(n)}, nil
}

type fixture struct {
	model, meta, wheel, library []byte
	manifest                    *manifest
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func newFixture(t *testing.T, base string) *fixture {
	t.Helper()
	f := &fixture{model: []byte("a model that is data"), library: []byte("a runtime that is data")}
	f.meta = []byte(`{"names":{"0":"one_close","1":"one_faint","2":"one_scoped"}}`)
	var z bytes.Buffer
	w := zip.NewWriter(&z)
	m, err := w.Create("pkg/lib/libfake.dylib")
	if err != nil {
		t.Fatal(err)
	}
	m.Write(f.library)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.wheel = z.Bytes()
	man := &manifest{ID: "fake-v1", Orientation: "portrait", MinScore: 0.35, NMSIoU: 0.45, Threads: 1,
		Classes: []class{{Name: "one_close", Label: "close"}, {Name: "one_faint", Label: "faint", MinScore: 0.6},
			{Name: "one_scoped", Label: "scoped", Apps: []string{"com.example.app"}}}}
	man.Licence.Weights = "AGPL-3.0"
	man.Files.Model = download{base + "/model.onnx", sum(f.model), int64(len(f.model))}
	man.Files.Meta = download{base + "/meta.json", sum(f.meta), int64(len(f.meta))}
	man.Files.Runtime = archive{URL: base + "/rt.whl", SHA256: sum(f.wheel), Size: int64(len(f.wheel)),
		Member: "pkg/lib/libfake.dylib", MemberSHA256: sum(f.library), MemberSize: int64(len(f.library))}
	man.Input.Width, man.Input.Height, man.Input.Pad = 64, 128, 114
	f.manifest = man
	return f
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) place(t *testing.T, home string) (dir string) {
	t.Helper()
	dir = filepath.Join(home, f.manifest.ID)
	write(t, filepath.Join(dir, "model.onnx"), f.model)
	write(t, filepath.Join(dir, "meta.json"), f.meta)
	write(t, filepath.Join(dir, "libfake.dylib"), f.library)
	write(t, filepath.Join(dir, manifestName), encode(t, f.manifest))
	write(t, filepath.Join(home, activeName), []byte(f.manifest.ID+"\n"))
	return dir
}

func picture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = color.Gray{Y: 90}.Y
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func testLog() (*slog.Logger, *logBuffer) {
	b := &logBuffer{}
	return slog.New(slog.NewTextHandler(b, nil)), b
}

func shutdown() {
	useMu.Lock()
	defer useMu.Unlock()
	if s := current.Swap(nil); s != nil {
		s.stop()
	}
}

func shorten(t *testing.T, to timing) {
	t.Helper()
	was := timings
	timings = to
	t.Cleanup(func() { timings = was })
}
