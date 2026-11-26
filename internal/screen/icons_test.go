package screen

import (
	"slices"
	"strings"
	"testing"
)

func iconAt(label string, x, y int) Icon {
	return Icon{Label: label, Score: 0.9, Bounds: Rect{X: x - 10, Y: y - 10, W: 20, H: 20}}
}

func iconScreen(read IconRead, icons ...Icon) *Screen {
	s := &Screen{Width: 375, Height: 667, App: App{Name: "Safari", ID: "com.apple.mobilesafari"}, Orientation: "portrait",
		Elements: []Element{{Ref: 1, Role: RoleButton, Label: "Tabs", Bounds: Rect{X: 320, Y: 620, W: 40, H: 40}, Source: SourceTree}}}
	s.SetGuessedIcons(read, icons)
	return s
}

func TestIconLines(t *testing.T) {
	plain := iconScreen(IconsNotLooked)
	bare := &Screen{Width: 375, Height: 667, App: plain.App, Orientation: "portrait", Elements: plain.Elements}
	if plain.Render() != bare.Render() {
		t.Fatalf("a read that did not look differs:\n%s\n%s", plain.Render(), bare.Render())
	}
	if got := iconScreen(IconsNotLooked, iconAt("close", 10, 10)).Render(); got != bare.Render() {
		t.Fatalf("icons of a read that did not look are shown:\n%s", got)
	}
	s := iconScreen(IconsLooked, iconAt("search", 30, 40), iconAt("close", 352, 40), iconAt("back", 30, 600))
	want := bare.Render() + "\nicon? \"search\" @30,40\nicon? \"close\" @352,40\nicon? \"back\" @30,600"
	if got := s.Render(); got != want {
		t.Fatalf("render:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(s.Header(), "icons") {
		t.Fatalf("header %q", s.Header())
	}
	late := iconScreen(IconsLate, iconAt("close", 10, 10))
	if want := bare.Header() + " · icons unread"; late.Header() != want {
		t.Fatalf("late header %q, want %q", late.Header(), want)
	}
	if strings.Contains(late.Render(), "icon?") {
		t.Fatalf("a late read shows icons:\n%s", late.Render())
	}
	for _, l := range strings.Split(s.Render(), "\n") {
		if IsIconLine(l) != strings.HasPrefix(l, "icon? ") {
			t.Errorf("IsIconLine(%q)", l)
		}
	}
}

func TestIconCover(t *testing.T) {
	s := iconScreen(IconsLooked, iconAt("close", 50, 100), iconAt("back", 50, 200), iconAt("menu", 50, 300))
	s.Elements = append(s.Elements,
		Element{Ref: 2, Label: "hidden", Bounds: Rect{X: 0, Y: 80, W: 375, H: 40}, Hidden: true},
		Element{Ref: 3, Label: "••••", Bounds: Rect{X: 0, Y: 180, W: 375, H: 40}, Masked: true})
	var shown []string
	for _, l := range strings.Split(s.Render(), "\n") {
		if IsIconLine(l) {
			shown = append(shown, l)
		}
	}
	if !slices.Equal(shown, []string{`icon? "menu" @50,300`}) {
		t.Fatalf("shown %q", shown)
	}
	cp := *s
	if !strings.Contains(cp.Render(), `icon? "menu"`) {
		t.Fatal("a copied screen lost its icons")
	}
}

func TestIconDiff(t *testing.T) {
	icons := []Icon{iconAt("close", 352, 40), iconAt("search", 30, 40)}
	prev := iconScreen(IconsLooked, icons...)
	jitter := iconScreen(IconsLooked, iconAt("close", 354, 41), iconAt("search", 29, 38))
	if lines, full := Diff(prev, jitter); full || len(lines) != 0 {
		t.Fatalf("jitter: %q %v", lines, full)
	}
	moved := iconScreen(IconsLooked, iconAt("close", 352, 40), iconAt("search", 200, 40))
	lines, full := Diff(prev, moved)
	if full || !slices.Equal(lines, []string{`+ icon? "search" @200,40`, `- icon? "search" @30,40`}) {
		t.Fatalf("moved: %q %v", lines, full)
	}
	for name, next := range map[string]*Screen{"not looked": iconScreen(IconsNotLooked), "late": iconScreen(IconsLate)} {
		lines, _ := Diff(prev, next)
		for _, l := range lines {
			if strings.Contains(l, "icon?") {
				t.Errorf("%s: %q reports an icon", name, lines)
			}
		}
	}
	lines, full = Diff(iconScreen(IconsLate), iconScreen(IconsLooked, iconAt("close", 352, 40)))
	if full || !slices.Contains(lines, `+ icon? "close" @352,40`) || slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "- ") }) {
		t.Fatalf("late to looked: %q %v", lines, full)
	}
	many := []Icon{iconAt("a", 10, 10), iconAt("b", 60, 10), iconAt("c", 110, 10)}
	lines, full = Diff(iconScreen(IconsLooked), iconScreen(IconsLooked, many...))
	if full || len(lines) != 3 {
		t.Fatalf("the length check must count icons: %q %v", lines, full)
	}
	other := []Icon{iconAt("d", 10, 400), iconAt("e", 60, 400), iconAt("f", 110, 400)}
	if _, full := Diff(iconScreen(IconsLooked, many...), iconScreen(IconsLooked, other...)); !full {
		t.Fatal("a diff longer than the screen is sent in full")
	}
}
