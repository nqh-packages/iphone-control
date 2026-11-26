//go:build !windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/api"
)

const fakeIconsEnv = "PINKY_PHONE_TEST_ICONS"

func standInForIconDetector() bool {
	if os.Getenv(fakeIconsEnv) == "" || len(os.Args) != 5 || strings.Join(os.Args[1:4], " ") != "phone icons serve" {
		return false
	}
	say := func(v any) {
		b, _ := json.Marshal(v)
		os.Stdout.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(b))), b...))
	}
	say(map[string]any{"ready": true, "classes": 2, "input": []int{800, 416}})
	box := func(class, label string, score float64, x, y float64, why string, apps ...string) map[string]any {
		return map[string]any{"class": class, "label": label, "score": score, "apps": apps, "why": why,
			"x": (x - 10) / 375, "y": (y - 10) / 667, "w": 20.0 / 375, "h": 20.0 / 667}
	}
	for {
		var head [8]byte
		if _, err := io.ReadFull(os.Stdin, head[:]); err != nil {
			return true
		}
		io.CopyN(io.Discard, os.Stdin, int64(binary.BigEndian.Uint32(head[:4]))-4)
		say(map[string]any{"seq": binary.BigEndian.Uint32(head[4:]), "found": []any{
			box("one_close", "close", 0.87, 352, 40, ""),
			box("two_tab", "shorts tab", 0.61, 187, 610, "", "com.example.other"),
			box("one_close", "close", 0.41, 22, 40, "below 0.60"),
		}})
	}
}

func fakeIconInstall(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, "data", "icons", "fake-v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"model.onnx", "meta.json", "libfake.dylib"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	file := func(name string) string {
		return fmt.Sprintf(`"url":"https://example.com/%s","sha256":"%x","size":%d`, name, sha256.Sum256([]byte(name)), len(name))
	}
	lib := "libfake.dylib"
	manifest := fmt.Sprintf(`{"id":"fake-v1","licence":{"code":"MIT","weights":"AGPL-3.0"},"files":{"model":{%s},"meta":{%s},
"runtime":{%s,"member":%q,"member_sha256":"%x","member_size":%d}},"input":{"width":416,"height":800,"pad":114},
"orientation":"portrait","min_score":0.35,"nms_iou":0.45,"threads":1,
"classes":[{"name":"one_close","label":"close"},{"name":"two_tab","label":"shorts tab"}]}`,
		file("model.onnx"), file("meta.json"), file("rt.whl"), lib, sha256.Sum256([]byte(lib)), len(lib))
	for path, text := range map[string]string{filepath.Join(dir, "manifest.json"): manifest, filepath.Join(home, "data", "icons", "active"): "fake-v1\n"} {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPhoneIcons(t *testing.T) {
	home := pinkyHome(t)
	t.Setenv(api.HomeEnv, home)
	t.Setenv(fakeIconsEnv, "1")
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 375, 667))); err != nil {
		t.Fatal(err)
	}
	pic := filepath.Join(home, "shot.png")
	if err := os.WriteFile(pic, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := phone("icons", pic)
	if code != phoneExitToolError || out != "" || !strings.Contains(errOut, "not installed") ||
		!strings.Contains(errOut, "fix: run `pinkyctl phone icons install <manifest>`") {
		t.Fatalf("no install: %d %q %q", code, out, errOut)
	}
	if code, out, errOut = phone("icons", "status"); code != phoneExitOK || out != "{\"icons\":\"off: not installed\"}\n" {
		t.Fatalf("status without install: %d %q %q", code, out, errOut)
	}
	if code, _, errOut = phone("icons", "install", filepath.Join(home, "absent.json")); code != phoneExitToolError || !strings.Contains(errOut, "absent.json") {
		t.Fatalf("install of nothing: %d %q", code, errOut)
	}
	for _, args := range [][]string{{"icons", pic, pic}, {"icons", "-all"}, {"icons", "-app", "x"}, {"icons", pic, "-wait", "3"},
		{"icons", "install"}, {"icons", filepath.Join(home, "absent.png")}} {
		if code, _, errOut = phone(args...); code != phoneExitUsage {
			t.Errorf("%q: code %d %q", args, code, errOut)
		}
	}
	if code, out, _ = phone("icons", "help"); code != phoneExitOK || !strings.Contains(out, "agent-feedback submit --tool phone-icons") {
		t.Fatalf("help: %d %q", code, out)
	}

	fakeIconInstall(t, home)
	if code, out, errOut = phone("icons", "status"); code != phoneExitOK || out != "{\"icons\":\"installed\",\"id\":\"fake-v1\"}\n" {
		t.Fatalf("status: %d %q %q", code, out, errOut)
	}
	code, out, errOut = phone("icons", pic, "-app", "com.example.app", "-all")
	want := []string{
		`icon? "close" 0.87 @352,40         kept      (class one_close)`,
		`-     "shorts tab" 0.61 @187,610   dropped   not for this app (com.example.other) (class two_tab)`,
		`-     "close" 0.41 @22,40          dropped   below 0.60 (class one_close)`,
		`icons: 1 kept, 2 dropped · `,
	}
	got := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if code != phoneExitOK || len(got) != len(want) {
		t.Fatalf("-all: %d %q %q", code, out, errOut)
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w) {
			t.Errorf("-all line %d:\n%q\nwant\n%q", i, got[i], w)
		}
	}
	code, out, _ = phone("icons", "-app", "com.example.other", pic)
	if code != phoneExitOK || !strings.Contains(out, `"shorts tab"`) || strings.Contains(out, "dropped   ") || !strings.Contains(out, "icons: 2 kept, 1 dropped") {
		t.Fatalf("flags first, kept only: %d %q", code, out)
	}
}

func TestPhoneIconsLive(t *testing.T) {
	newPhoneEnv(t)
	code, out, errOut := phone("icons")
	if code != phoneExitOK || strings.Count(out, "\n") != 1 || !strings.HasPrefix(out, "screen ") {
		t.Fatalf("live: %d %q %q", code, out, errOut)
	}
	read := "settled · iPhone SE\nscreen 375x667 · portrait · keyboard hidden\n[1] button \"icon? no\" @1,2\nicon? \"close\" @352,40\nocr \"x\" @3,4\n"
	if got := iconLines(read); got != "screen 375x667 · portrait · keyboard hidden\nicon? \"close\" @352,40\n" {
		t.Fatalf("filtered %q", got)
	}
	if got := iconLines("error: lease_held\n"); got != "error: lease_held\n" {
		t.Fatalf("an answer without a screen: %q", got)
	}
}
