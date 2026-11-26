package icons

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

func TestDecodeYOLO(t *testing.T) {
	m := committedManifest(t)
	closeAt, _ := classOf(t, m, "ios_close")
	search, _ := classOf(t, m, "yt_search")
	app, _ := classOf(t, m, "ios_app_x")
	const pw, ph = 375, 667
	scale, padX, padY := 416.0/375, 0.0, 30.0
	anchors := []struct {
		class        int
		score        float32
		cx, cy, w, h float64
	}{
		{closeAt, 0.7, 353, 41, 20, 20},
		{closeAt, 0.9, 352, 40, 20, 20},
		{search, 0.5, 352, 40, 20, 20},
		{closeAt, 0.45, 100, 300, 20, 20},
		{app, 0.95, 200, 500, 40, 40},
		{search, 0.2, 100, 100, 20, 20},
		{search, 0.36, 372, 665, 20, 20},
	}
	n := len(anchors)
	out := make([]float32, 103*n)
	for j, a := range anchors {
		out[j], out[n+j] = float32(a.cx*scale+padX), float32(a.cy*scale+padY)
		out[2*n+j], out[3*n+j] = float32(a.w*scale), float32(a.h*scale)
		out[(4+a.class)*n+j] = a.score
	}
	got, err := decodeYOLO(out, []int64{1, 103, int64(n)}, m, scale, padX, padY, pw, ph)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		class, why string
		x, y, w, h int
	}
	var rows []row
	for _, d := range got {
		r := d.box().rect(pw, ph)
		rows = append(rows, row{d.Class, d.Why, r.X, r.Y, r.W, r.H})
	}
	want := []row{
		{"ios_app_x", "off", 180, 480, 40, 40},
		{"ios_close", "", 342, 30, 20, 20},
		{"yt_search", "", 342, 30, 20, 20},
		{"ios_close", "below 0.60", 90, 290, 20, 20},
		{"yt_search", "", 362, 655, 13, 12},
	}
	if !slices.Equal(rows, want) {
		t.Fatalf("got  %+v\nwant %+v", rows, want)
	}
	if _, err := decodeYOLO(out, []int64{1, 102, int64(n)}, m, scale, padX, padY, pw, ph); err == nil {
		t.Fatal("a wrong shape was decoded")
	}
	if _, err := decodeYOLO(out[:10], []int64{1, 103, int64(n)}, m, scale, padX, padY, pw, ph); err == nil {
		t.Fatal("a short answer was decoded")
	}
}

func TestLetterbox(t *testing.T) {
	for _, size := range []image.Point{{375, 667}, {750, 1334}, {400, 400}} {
		img := image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
		draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		mark := image.Rect(size.X-90, 40, size.X-50, 80)
		draw.Draw(img, mark, image.NewUniform(color.RGBA{255, 0, 0, 255}), image.Point{}, draw.Src)
		const w, h = 416, 800
		tensor, scale, padX, padY := letterbox(img, w, h, 114)
		if len(tensor) != 3*w*h {
			t.Fatalf("%v: %d values", size, len(tensor))
		}
		var sx, sy, total float64
		for y := range h {
			for x := range w {
				p := y*w + x
				red := float64(tensor[p] - tensor[w*h+p])
				if red > 0 && tensor[p] > 0.9 {
					sx, sy, total = sx+red*(float64(x)+0.5), sy+red*(float64(y)+0.5), total+red
				}
			}
		}
		x, y := (sx/total-padX)/scale, (sy/total-padY)/scale
		cx, cy := float64(mark.Min.X+mark.Max.X)/2, float64(mark.Min.Y+mark.Max.Y)/2
		if math.Abs(x-cx) > 1 || math.Abs(y-cy) > 1 {
			t.Errorf("%v: the mark at %.0f,%.0f comes back at %.2f,%.2f", size, cx, cy, x, y)
		}
		if padY >= 1 && math.Abs(float64(tensor[0])-114.0/255) > 1e-6 {
			t.Errorf("%v: padding is %v", size, tensor[0])
		}
	}
}

func TestFoundFor(t *testing.T) {
	box := func(label string, score float32, x, y int, apps ...string) Box {
		return Box{Label: label, Score: score, Apps: apps, X: float64(x-10) / 400, Y: float64(y-10) / 800, W: 20.0 / 400, H: 20.0 / 800}
	}
	f := Found{Read: screen.IconsLooked, Orientation: "portrait", Overlap: 0.45, Boxes: []Box{
		box("weaker", 0.5, 301, 41),
		box("close", 0.9, 300, 40),
		box("other app", 0.9, 100, 200, "com.example.other"),
		box("this app", 0.8, 200, 200, "com.example.app"),
		box("under other app", 0.4, 100, 200),
		box("named", 0.9, 40, 300),
		box("unnamed", 0.9, 40, 400),
		box("in a web view", 0.9, 200, 450),
		box("read by ocr", 0.9, 40, 500),
		box("on keys", 0.9, 40, 700),
		box("covered", 0.9, 40, 10),
	}}
	small := func(label string, y int) screen.Element {
		return screen.Element{Ref: y, Role: screen.RoleButton, Label: label, Source: screen.SourceTree, Bounds: screen.Rect{X: 20, Y: y - 20, W: 40, H: 40}}
	}
	s := &screen.Screen{Width: 400, Height: 800, Orientation: "portrait", App: screen.App{ID: "com.example.app"},
		Keyboard: true, KeyboardFrame: screen.Rect{Y: 600, W: 400, H: 200}, Covered: []screen.Rect{{W: 400, H: 20}},
		Elements: []screen.Element{
			small("Search", 300), small(" ", 400),
			{Ref: 9, Role: screen.RoleOther, Label: "Page", Source: screen.SourceTree, Bounds: screen.Rect{Y: 420, W: 400, H: 170}},
			{Role: screen.RoleText, Label: "x", Source: screen.SourceOCR, Bounds: screen.Rect{X: 30, Y: 490, W: 20, H: 20}},
		}}
	labels := func(icons []screen.Icon) (out []string) {
		for _, i := range icons {
			out = append(out, i.Label)
		}
		slices.Sort(out)
		return out
	}
	want := []string{"close", "in a web view", "read by ocr", "this app", "under other app", "unnamed"}
	if got := labels(f.For(s)); !slices.Equal(got, want) {
		t.Fatalf("for the app: %q, want %q", got, want)
	}
	for _, i := range f.For(s) {
		if i.Label == "close" && i.Bounds != (screen.Rect{X: 290, Y: 30, W: 20, H: 20}) {
			t.Fatalf("placed at %+v", i.Bounds)
		}
	}
	other := *s
	other.App.ID = ""
	if got := labels(f.For(&other)); slices.Contains(got, "this app") || !slices.Contains(got, "close") {
		t.Fatalf("with no app: %q", got)
	}
	turned := *s
	turned.Orientation, turned.Width, turned.Height = "landscape", 800, 400
	if got := f.For(&turned); len(got) != 0 {
		t.Fatalf("a landscape screen: %v", got)
	}
	if got := (Found{}).For(s); len(got) != 0 {
		t.Fatalf("nothing found: %v", got)
	}
	why := f.sift("com.example.app")
	if why[0] != "overlaps a stronger guess" || !strings.HasPrefix(why[2], "not for this app (com.example.other)") {
		t.Fatalf("why: %q", why)
	}
}
