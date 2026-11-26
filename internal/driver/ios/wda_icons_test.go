package ios

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/icons"
	"github.com/DIYgod/pinky/internal/screen"
)

func TestWDAIcons(t *testing.T) {
	f := &fakeWDA{app: ethena, png: pngBytes(t)}
	srv := httptest.NewServer(f)
	defer srv.Close()
	var looks atomic.Int32
	var wait, ocrWait time.Duration
	answer := icons.Found{Read: screen.IconsLooked, Boxes: []icons.Box{
		{Label: "close", Score: 0.9, X: 0.75, Y: 0.45, W: 0.1, H: 0.1},
		{Label: "named by the tree", Score: 0.9, X: 0.2, Y: 0.075, W: 0.1, H: 0.05},
	}}
	d := &WDADevice{c: newWDAURL(srv.URL, ""), width: 100, height: 200, scale: 1,
		recognize: func([]byte) ([]ocrLine, error) {
			time.Sleep(ocrWait)
			return []ocrLine{{Text: "Total balance", Confidence: 1, X: 0.1, Y: 0.7, W: 0.5, H: 0.05}}, nil
		},
		look: func(_ context.Context, pic []byte) icons.Found {
			looks.Add(1)
			if len(pic) == 0 {
				t.Error("a look without a picture")
			}
			time.Sleep(wait)
			return answer
		}}
	defer d.trees.close()
	ctx := context.Background()
	read := func(what string, opt driver.ObserveOptions) *screen.Screen {
		t.Helper()
		s, err := d.Observe(ctx, opt)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return s
	}
	const line = `icon? "close" @80,100`

	s := read("with a picture", driver.ObserveOptions{OCR: true})
	if r := s.Render(); !strings.HasSuffix(r, "\n"+line) || strings.Contains(r, "named by the tree") || !strings.Contains(r, `ocr "Total balance"`) {
		t.Fatalf("icons:\n%s", r)
	}
	with := d.look
	d.look = nil
	absent := read("no detector", driver.ObserveOptions{OCR: true}).Render()
	d.look = func(context.Context, []byte) icons.Found { return icons.Found{} }
	if got := read("a detector that did not look", driver.ObserveOptions{OCR: true}).Render(); got != absent {
		t.Fatalf("differs:\n%s\n%s", got, absent)
	}
	if want := strings.TrimSuffix(s.Render(), "\n"+line); absent != want {
		t.Fatalf("without a detector the read is not the read without icons:\n%s\n%s", absent, want)
	}
	d.look = with

	n := looks.Load()
	read("without a picture", driver.ObserveOptions{})
	s = read("may not capture", driver.ObserveOptions{OCR: true, MayCapture: func(*screen.Screen) bool { return false }})
	if got := s.Render(); looks.Load() != n || strings.Contains(got, "icon") || strings.Contains(got, "Total balance") {
		t.Fatalf("%d looks where no picture may be used:\n%s", looks.Load()-n, got)
	}

	var asked atomic.Int32
	s = read("the app changed", driver.ObserveOptions{OCR: true, MayCapture: func(*screen.Screen) bool { return asked.Add(1) == 1 }})
	if got := s.Render(); looks.Load() != n+1 || strings.Contains(got, "icon") || strings.Contains(got, "Total balance") {
		t.Fatalf("a withdrawn picture (%d looks):\n%s", looks.Load()-n, got)
	}

	wait, ocrWait = 350*time.Millisecond, 300*time.Millisecond
	answer = icons.Found{Read: screen.IconsLate}
	start := time.Now()
	s = read("slow", driver.ObserveOptions{OCR: true})
	if took := time.Since(start); took > 600*time.Millisecond {
		t.Fatalf("the look and OCR did not run side by side: %v", took)
	}
	if got := s.Render(); !strings.Contains(s.Header(), "icons unread") || strings.Contains(got, "icon?") || !strings.Contains(got, "Total balance") {
		t.Fatalf("late:\n%s", got)
	}

	wait, ocrWait = 0, 0
	answer = icons.Found{Read: screen.IconsLooked, Boxes: []icons.Box{{Label: "back", Score: 0.5, X: 0.05, Y: 0.45, W: 0.1, H: 0.1}}}
	d.trees.slower("com.ethena.app", treeShallow, time.Now())
	s = read("treeless", driver.ObserveOptions{})
	if got := s.Render(); !s.TreeTooSlow || !strings.HasSuffix(got, "\n"+`icon? "back" @10,100`) {
		t.Fatalf("treeless:\n%s", got)
	}
}
