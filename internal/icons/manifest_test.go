package icons

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const committed = "../../models/icons/advnt-screen-parser-v4.json"

func committedManifest(t *testing.T) *manifest {
	t.Helper()
	b, err := os.ReadFile(committed)
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func classOf(t *testing.T, m *manifest, name string) (int, class) {
	t.Helper()
	i := slices.IndexFunc(m.Classes, func(c class) bool { return c.Name == name })
	if i < 0 {
		t.Fatalf("no class %q", name)
	}
	return i, m.Classes[i]
}

func TestManifest(t *testing.T) {
	m := committedManifest(t)
	if len(m.Classes) != 99 || m.MinScore != 0.35 || m.NMSIoU != 0.45 || m.Threads != 2 || m.Orientation != "portrait" {
		t.Fatalf("committed manifest: %d classes, %+v", len(m.Classes), m)
	}
	if err := m.fits([]int64{1, 3, 800, 416}, []int64{1, 103, 6825}); err != nil {
		t.Fatal(err)
	}
	for name, shapes := range map[string][2][]int64{
		"one class short": {{1, 3, 800, 416}, {1, 102, 6825}},
		"one class over":  {{1, 3, 800, 416}, {1, 104, 6825}},
		"other input":     {{1, 3, 640, 640}, {1, 103, 6825}},
	} {
		if m.fits(shapes[0], shapes[1]) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	scoped, off := 0, 0
	for _, c := range m.Classes {
		if c.Off {
			off++
		}
		if len(c.Apps) > 0 {
			scoped++
		}
	}
	if scoped != 87 || off != 4 {
		t.Fatalf("%d classes scoped to apps, %d off", scoped, off)
	}
	for name, want := range map[string]class{
		"ios_close":    {Label: "close", MinScore: 0.6},
		"yt_search":    {Label: "search"},
		"tt_back":      {Label: "back"},
		"ig_menu":      {Label: "menu"},
		"yt_reels_tab": {Label: "shorts tab", Apps: []string{"com.google.ios.youtube"}},
		"x_back":       {Label: "back", Apps: []string{"com.atebits.Tweetie2"}},
		"tt_like":      {Label: "like", Apps: []string{"com.zhiliaoapp.musically"}},
		"ig_home_tab":  {Label: "home tab", Apps: []string{"com.burbn.instagram"}},
		"ios_app_x":    {Label: "app x", Off: true},
	} {
		_, c := classOf(t, m, name)
		if c.Label != want.Label || c.MinScore != want.MinScore || c.Off != want.Off || !slices.Equal(c.Apps, want.Apps) {
			t.Errorf("%s: %+v, want %+v", name, c, want)
		}
	}

	good := encode(t, newFixture(t, "https://example.com").manifest)
	if _, err := parseManifest(good); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ old, new, says string }{
		"unknown field":   {`"id":`, `"extra":1,"id":`, "extra"},
		"duplicate class": {`"name":"one_faint"`, `"name":"one_close"`, "twice"},
		"score over one":  {`"min_score":0.6`, `"min_score":1.5`, "outside 0..1"},
		"score under 0":   {`"min_score":0.6`, `"min_score":-0.1`, "outside 0..1"},
		"missing hash":    {`"member_sha256":"`, `"member_sha256":"x`, "member_sha256"},
		"no model hash":   {`/model.onnx","sha256":"`, `/model.onnx","sha256":"zz`, "files.model"},
		"path as id":      {`"id":"fake-v1"`, `"id":"../up"`, "folder name"},
		"path as file":    {`pkg/lib/libfake.dylib`, `pkg/..`, "installed file"},
		"file twice":      {`/meta.json`, `/model.onnx`, "installed file"},
		"no label":        {`"label":"close"`, `"label":" "`, "label"},
		"bad orientation": {`"orientation":"portrait"`, `"orientation":"up"`, "orientation"},
		"no classes":      {`"classes":[`, `"classes":[],"was":[`, "was"},
		"trailing text":   {`]}]}`, `]}]} {}`, "follows"},
	} {
		bad := bytes.Replace(good, []byte(c.old), []byte(c.new), 1)
		if bytes.Equal(bad, good) {
			t.Fatalf("%s: nothing replaced in %s", name, good)
		}
		if _, err := parseManifest(bad); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want %q", name, err, c.says)
		}
	}

	home := t.TempDir()
	if s := Check(home); s.Installed || s.String() != "off: not installed" {
		t.Fatalf("empty home: %v", s)
	}
	dir := newFixture(t, "https://example.com").place(t, home)
	if s := Check(home); !s.Installed || s.ID != "fake-v1" || s.String() != "installed" {
		t.Fatalf("installed home: %v", s)
	}
	write(t, filepath.Join(dir, "libfake.dylib"), []byte("short"))
	if s := Check(home); s.Installed || !strings.Contains(s.String(), "libfake.dylib is missing or has the wrong size") {
		t.Fatalf("damaged home: %v", s)
	}
	write(t, filepath.Join(home, activeName), []byte("../elsewhere"))
	if s := Check(home); s.Installed {
		t.Fatalf("a path as the install: %v", s)
	}
}
