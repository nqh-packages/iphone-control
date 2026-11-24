package interrupt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

func fixture(t *testing.T, name string) *screen.Screen {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "screens", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseScreen(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func builtin(t *testing.T) *Book {
	t.Helper()
	b, errs := Load(Builtin())
	if len(errs) > 0 {
		t.Fatalf("built-in books: %v", errs)
	}
	return b
}

func typedLease(now time.Time) Lease {
	return Lease{Expects: []string{"sign-in"}, Triggers: map[Trigger]time.Time{TriggerCredentialTyped: now.Add(-time.Minute)}, Fired: map[string]int{}, Now: now, CodeKept: true}
}

func TestBuiltinEntriesMatchTheirScreens(t *testing.T) {
	now := time.Now()
	cases := []struct {
		fixture, entry, tap string
	}{
		{"save-password", "save-password", "Not Now"},
		{"personalised-ads", "personalised-ads", "Turn Off Personalised Ads"},
		{"passkey-sheet", "passkey-sheet", "Cancel"},
		{"device-account-sheet", "device-account-sheet", "Use a different Apple Account"},
		{"saved-passwords-touch-id", "saved-passwords-touch-id", "Cancel"},
		{"sign-in-request", "sign-in-request", "Allow"},
		{"code-shown-done", "code-shown-done", "Done"},
		{"code-shown-ok", "code-shown-ok", "OK"},
		{"code-boxes", "code-boxes", ""},
		{"no-internet", "no-internet", ""},
		{"store-password-required", "store-password-required", ""},
	}
	book := builtin(t)
	for _, c := range cases {
		e, ok := book.Match(fixture(t, c.fixture), typedLease(now))
		if !ok || e.Name != c.entry {
			t.Errorf("%s: matched %v %v, want %s", c.fixture, e, ok, c.entry)
			continue
		}
		if c.tap != "" && (len(e.Steps) == 0 || e.Steps[len(e.Steps)-1].Label != c.tap) {
			t.Errorf("%s: steps %+v, want a tap on %q", c.fixture, e.Steps, c.tap)
		}
	}
}

func TestEveryBuiltinEntryHasAFixture(t *testing.T) {
	covered := map[string]bool{}
	for _, n := range []string{"save-password", "personalised-ads", "passkey-sheet", "device-account-sheet", "saved-passwords-touch-id",
		"sign-in-request", "code-shown-done", "code-shown-ok", "code-boxes", "no-internet", "store-password-required"} {
		covered[n] = true
	}
	for _, e := range builtin(t).Entries() {
		if !covered[e.Name] {
			t.Errorf("built-in entry %s has no fixture screen in TestBuiltinEntriesMatchTheirScreens", e.Name)
		}
	}
}

func TestMatchIsPure(t *testing.T) {
	book := builtin(t)
	s := fixture(t, "save-password")
	l := typedLease(time.Now())
	before := s.Render()
	a, _ := book.Match(s, l)
	b, _ := book.Match(s, l)
	if a != b || s.Render() != before || len(l.Fired) != 0 {
		t.Fatalf("Match changed its inputs or answered differently: %v %v", a, b)
	}
}

func TestLoaderEnforcesKindRules(t *testing.T) {
	cases := map[string]string{
		"dismiss-types":     "only taps",
		"consent-no-expect": "needs expect and after",
		"button-only":       "when needs texts",
		"type-account":      "not supported",
		"report-no-fix":     "`fix:`",
		"max-high":          "out of range",
	}
	for name, want := range cases {
		data, err := os.ReadFile(filepath.Join("testdata", "bad", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		book, errs := Load([]File{{Name: name + ".yaml", Data: data}})
		if len(errs) != 1 || !strings.Contains(errs[0].Rule, want) || errs[0].Fix == "" || errs[0].Line == 0 {
			t.Errorf("%s: errors %+v, want one containing %q with a fix and a line", name, errs, want)
		}
		if len(book.Entries()) != 0 {
			t.Errorf("%s: a refused entry loaded", name)
		}
	}
}

func TestConsentNeedsExpectAndTrigger(t *testing.T) {
	book := builtin(t)
	s := fixture(t, "sign-in-request")
	now := time.Now()
	good := typedLease(now)
	cases := []struct {
		name string
		edit func(*Lease)
		want bool
	}{
		{"declared and typed", func(*Lease) {}, true},
		{"not declared", func(l *Lease) { l.Expects = nil }, false},
		{"other expect", func(l *Lease) { l.Expects = []string{"store"} }, false},
		{"no trigger", func(l *Lease) { l.Triggers = nil }, false},
		{"trigger too old", func(l *Lease) { l.Triggers = map[Trigger]time.Time{TriggerCredentialTyped: now.Add(-6 * time.Minute)} }, false},
		{"already allowed once", func(l *Lease) { l.Fired = map[string]int{"sign-in-request": 1} }, false},
	}
	for _, c := range cases {
		l := good
		l.Expects = append([]string(nil), good.Expects...)
		l.Fired = map[string]int{}
		c.edit(&l)
		e, ok := book.Match(s, l)
		if ok != c.want || (ok && e.Name != "sign-in-request") {
			t.Errorf("%s: matched %v %v, want %v", c.name, e, ok, c.want)
		}
	}
}

func TestCredentialNeedsExpectAndStopsOnRejection(t *testing.T) {
	book := builtin(t)
	l := typedLease(time.Now())
	if e, ok := book.Match(fixture(t, "code-boxes"), l); !ok || e.Name != "code-boxes" {
		t.Fatalf("boxes: %v %v", e, ok)
	}
	if _, ok := book.Match(fixture(t, "code-boxes"), Lease{Fired: map[string]int{}, Now: time.Now()}); ok {
		t.Error("code boxes fired without expect")
	}
	if _, ok := book.Match(fixture(t, "code-boxes-rejected"), l); ok {
		t.Error("a rejected code was offered again")
	}
	if e, ok := book.Rejected(fixture(t, "code-boxes-rejected"), l); !ok || e.Name != "code-boxes" {
		t.Errorf("rejection not seen: %v %v", e, ok)
	}
}

func TestCodeDisplayWaitsForTheCodeToBeKept(t *testing.T) {
	book := builtin(t)
	l := typedLease(time.Now())
	l.CodeKept = false
	if e, ok := book.Match(fixture(t, "code-shown-done"), l); ok {
		t.Fatalf("dismissed a code that was not kept: %s", e.Name)
	}
	l.CodeKept = true
	if _, ok := book.Match(fixture(t, "code-shown-done"), l); !ok {
		t.Fatal("kept code display not dismissed")
	}
}

func TestNothingMatchesAPlainScreen(t *testing.T) {
	if e, ok := builtin(t).Match(fixture(t, "plain-screen"), typedLease(time.Now())); ok {
		t.Fatalf("fired %s on a plain screen with Cancel and Not Now buttons", e.Name)
	}
}

func TestMaxPerLease(t *testing.T) {
	book := builtin(t)
	l := typedLease(time.Now())
	l.Fired = map[string]int{"save-password": 2}
	if _, ok := book.Match(fixture(t, "save-password"), l); ok {
		t.Fatal("an entry fired past its max")
	}
}

func TestUserEntryShadowsBuiltinByName(t *testing.T) {
	user := File{Name: "mine.yaml", Data: []byte("entries:\n  - name: save-password\n    kind: dismiss\n    when:\n      texts: [\"Save Password?\"]\n    do:\n      - tap: \"Save Password\"\n")}
	book, errs := Load(append(Builtin(), user))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	e, ok := book.Match(fixture(t, "save-password"), typedLease(time.Now()))
	if !ok || e.Source != "mine.yaml" || e.Steps[0].Label != "Save Password" {
		t.Fatalf("user entry did not win: %+v", e)
	}
	n := 0
	for _, x := range book.Entries() {
		if x.Name == "save-password" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d entries named save-password", n)
	}
}

func TestParseScreenReadsWhatRenderWrites(t *testing.T) {
	want := &screen.Screen{Width: 400, Height: 800, App: screen.App{Name: "Safari", ID: "com.apple.mobilesafari"},
		Elements: []screen.Element{
			{Ref: 3, Role: "button", Label: "Not Now", Bounds: screen.Rect{X: 100, Y: 20, W: 160, H: 44}, Source: screen.SourceTree},
			{Label: "line", Bounds: screen.Rect{X: 100, Y: 90, W: 160, H: 44}, Source: screen.SourceOCR}}}
	got, err := ParseScreen(want.Render())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Elements) != 2 || got.Elements[0].Label != "Not Now" || got.Elements[1].Source != screen.SourceOCR || got.App.ID != "com.apple.mobilesafari" {
		t.Fatalf("parsed %+v", got)
	}
}
