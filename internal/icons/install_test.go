package icons

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestInstall(t *testing.T) {
	var mu sync.Mutex
	served := map[string]int{}
	files := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		served[r.URL.Path]++
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	f := newFixture(t, srv.URL)
	files["/model.onnx"], files["/meta.json"], files["/rt.whl"] = f.model, f.meta, f.wheel
	hits := func() int {
		mu.Lock()
		defer mu.Unlock()
		return served["/model.onnx"] + served["/meta.json"] + served["/rt.whl"]
	}
	ctx := context.Background()
	home := filepath.Join(t.TempDir(), "icons")
	source := func(m *manifest) string {
		p := filepath.Join(t.TempDir(), "m.json")
		write(t, p, encode(t, m))
		return p
	}
	noParts := func(what string) {
		t.Helper()
		parts, _ := filepath.Glob(filepath.Join(home, "*", ".part-*"))
		more, _ := filepath.Glob(filepath.Join(home, ".part-*"))
		if len(parts)+len(more) != 0 {
			t.Fatalf("%s: left %v %v", what, parts, more)
		}
	}

	var out bytes.Buffer
	s, err := Install(ctx, source(f.manifest), home, &out)
	if err != nil || !s.Installed || s.ID != "fake-v1" || !Check(home).Installed {
		t.Fatalf("install: %v %v\n%s", s, err, &out)
	}
	for _, want := range []string{"weights: AGPL-3.0", "model.onnx", "(3 class names match the manifest)", "→ libfake.dylib", "self-test: blank picture → 0 icons", "installed: fake-v1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, &out)
		}
	}
	dir := filepath.Join(home, "fake-v1")
	if b, err := os.ReadFile(filepath.Join(dir, "libfake.dylib")); err != nil || !bytes.Equal(b, f.library) {
		t.Fatalf("runtime: %q %v", b, err)
	}
	noParts("install")

	n := hits()
	if _, err := Install(ctx, source(f.manifest), home, &out); err != nil || hits() != n {
		t.Fatalf("a second install fetched %d files again: %v", hits()-n, err)
	}
	write(t, filepath.Join(dir, "model.onnx"), []byte(strings.ToUpper(string(f.model))))
	if _, err := Install(ctx, source(f.manifest), home, &out); err != nil || hits() != n+1 {
		t.Fatalf("a changed file was not fetched again (%d fetches): %v", hits()-n, err)
	}

	next := *f.manifest
	next.ID = "fake-v2"
	fails := func(what, says string) {
		t.Helper()
		if _, err := Install(ctx, source(&next), home, &out); err == nil || !strings.Contains(err.Error(), says) {
			t.Fatalf("%s: %v, want %q", what, err, says)
		}
		if s := Check(home); !s.Installed || s.ID != "fake-v1" {
			t.Fatalf("%s: the earlier install is gone: %v", what, s)
		}
		noParts(what)
	}
	mu.Lock()
	files["/model.onnx"] = []byte(strings.ToUpper(string(f.model)))
	mu.Unlock()
	fails("another model", "model.onnx: got")
	mu.Lock()
	files["/model.onnx"] = append(bytes.Clone(f.model), "and more"...)
	mu.Unlock()
	fails("a longer model", "model.onnx: got")
	mu.Lock()
	files["/model.onnx"] = f.model
	delete(files, "/rt.whl")
	mu.Unlock()
	fails("no archive", "404")
	mu.Lock()
	files["/rt.whl"] = f.wheel
	mu.Unlock()
	next.Files.Runtime.Member = "pkg/lib/absent.dylib"
	fails("no member", "runtime archive")
	next.Files.Runtime = f.manifest.Files.Runtime
	next.Classes = append([]class(nil), f.manifest.Classes...)
	next.Classes[1].Name = "one_renamed"
	fails("other classes", `class 1 is "one_faint"`)

	write(t, filepath.Join(dir, "behaviour"), []byte("crash"))
	if _, err := Install(ctx, source(f.manifest), home, &out); err == nil || !strings.Contains(err.Error(), "self-test") {
		t.Fatalf("a failing detector installed: %v", err)
	}
	if s := Check(home); s.Installed {
		t.Fatalf("a reinstall that failed its self-test stays installed: %v", s)
	}
}
