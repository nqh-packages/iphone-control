package gateway

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/interrupt"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/vault"
)

type pageDef struct {
	app    screen.App
	els    []screen.Element
	dialog string
	on     map[string]string
}

type promptPhone struct {
	driver.Device
	mu          sync.Mutex
	page        string
	pages       map[string]pageDef
	taps        []string
	swipes      int
	enableAfter int
}

func (p *promptPhone) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, err := p.Device.Observe(ctx, opt)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	def := p.pages[p.page]
	c := *s
	c.Width, c.Height, c.App, c.Locked = 400, 800, def.app, false
	c.Elements = nil
	for _, e := range def.els {
		if p.swipes >= p.enableAfter {
			e.States = nil
		}
		c.Elements = append(c.Elements, e)
	}
	c.Events = nil
	if def.dialog != "" {
		c.Events = []screen.Event{{Kind: screen.EventDialog, Text: def.dialog}}
	}
	return &c, nil
}

func (p *promptPhone) Tap(ctx context.Context, pt screen.Point, opt driver.TapOptions) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.pages[p.page].els {
		if e.Bounds.Contains(pt) {
			p.taps = append(p.taps, e.Label)
			if next, ok := p.pages[p.page].on[e.Label]; ok {
				p.page = next
			}
			return nil
		}
	}
	return nil
}

func (p *promptPhone) Swipe(ctx context.Context, from, to screen.Point, dur time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.swipes++
	return nil
}

func (p *promptPhone) tapped() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.taps...)
}

func (p *promptPhone) current() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.page
}

func btn(label string, y int, states ...string) screen.Element {
	return screen.Element{Role: screen.RoleButton, Label: label, Bounds: screen.Rect{X: 100, Y: y, W: 200, H: 44}, States: states}
}

func txt(label string, y int) screen.Element {
	return screen.Element{Role: screen.RoleText, Label: label, Bounds: screen.Rect{X: 20, Y: y, W: 360, H: 30}}
}

var (
	mobileSafari = screen.App{Name: "Safari", ID: "com.apple.mobilesafari"}
	settingsApp  = screen.App{Name: "Settings", ID: "com.apple.Preferences"}
	appStoreApp  = screen.App{Name: "App Store", ID: "com.apple.AppStore"}
)

type promptRig struct {
	g     *Gateway
	s     *Session
	phone *promptPhone
}

func newPromptRig(t *testing.T, pages map[string]pageDef, book *interrupt.Book, expect ...string) *promptRig {
	t.Helper()
	phone := &promptPhone{page: "start", pages: pages}
	g, _ := newTestGateway(t, func(o *Options) {
		o.Interrupts = book
		o.Providers = []driver.Provider{&wrappedProvider{o.Providers[0], func(d driver.Device) driver.Device {
			phone.Device = d
			return phone
		}}}
	})
	s := g.OpenSession(agent)
	t.Cleanup(s.Close)
	res := s.Connect(context.Background(), ConnectRequest{Task: "interrupts", Expect: expect})
	if res.IsError() || res.Status != StatusSettled {
		t.Fatalf("connect: %+v", res)
	}
	return &promptRig{g: g, s: s, phone: phone}
}

func builtinBook(t *testing.T, extra ...string) *interrupt.Book {
	t.Helper()
	files := interrupt.Builtin()
	for i, y := range extra {
		files = append(files, interrupt.File{Name: "user" + string(rune('a'+i)) + ".yaml", Data: []byte(y)})
	}
	book, errs := interrupt.Load(files)
	if len(errs) > 0 {
		t.Fatalf("book: %v", errs)
	}
	return book
}

func savePasswordPages(next string) map[string]pageDef {
	return map[string]pageDef{
		"start": {app: mobileSafari, els: []screen.Element{txt("Save Password?", 100), btn("Save Password", 200), btn("Not Now", 260), btn("Continue", 400)}, on: map[string]string{"Not Now": next}},
		"after": {app: mobileSafari, els: []screen.Element{txt("Welcome", 100), btn("Continue", 400)}},
	}
}

func TestInterruptClearsBeforeAnAimedActionThenTellsTheAgent(t *testing.T) {
	r := newPromptRig(t, savePasswordPages("after"), builtinBook(t))
	obs := r.s.Observe(context.Background(), ObserveRequest{})
	res := r.s.Act(context.Background(), Call{}, Action{Tool: "tap", Ref: ref(t, obs, "Continue")})
	mustStatus(t, res, StatusStale)
	if !strings.Contains(res.Message, "handled: save-password") || strings.Join(r.phone.tapped(), ",") != "Not Now" || r.phone.current() != "after" {
		t.Fatalf("taps %v page %s: %+v", r.phone.tapped(), r.phone.current(), res)
	}
	obs = r.s.Observe(context.Background(), ObserveRequest{})
	mustStatus(t, r.s.Act(context.Background(), Call{}, Action{Tool: "tap", Ref: ref(t, obs, "Continue")}), StatusSettled)
}

func TestWaitForClearsAnInterruptInItsPoll(t *testing.T) {
	r := newPromptRig(t, savePasswordPages("after"), builtinBook(t))
	res := r.s.WaitFor(context.Background(), WaitForRequest{Text: "Welcome", TimeoutMS: 3000})
	if res.Matched == nil || !*res.Matched || !strings.Contains(res.Message, "handled: save-password") {
		t.Fatalf("wait_for: %+v", res)
	}
	if strings.Join(r.phone.tapped(), ",") != "Not Now" {
		t.Fatalf("taps %v", r.phone.tapped())
	}
}

func TestInterruptStepsGoThroughPolicy(t *testing.T) {
	pages := map[string]pageDef{
		"start": {app: settingsApp, els: []screen.Element{txt("Danger prompt", 100), btn("Erase iPhone", 200)}},
	}
	book := builtinBook(t, "entries:\n  - name: danger\n    kind: dismiss\n    when:\n      texts: [\"Danger prompt\"]\n    do:\n      - tap: \"Erase iPhone\"\n")
	r := newPromptRig(t, pages, book)
	res := r.s.WaitFor(context.Background(), WaitForRequest{Text: "Never", TimeoutMS: 400})
	if len(r.phone.tapped()) != 0 || !strings.Contains(res.Message, "stuck: danger") {
		t.Fatalf("the gate did not stop the step: taps %v, %s", r.phone.tapped(), res.Message)
	}
}

func TestInterruptStepsStopAtABlockedApp(t *testing.T) {
	pages := map[string]pageDef{
		"start": {app: screen.App{Name: "Dinely", ID: demo.Dinely}, els: []screen.Element{txt("Save Password?", 100), btn("Not Now", 260)}},
	}
	r := newPromptRig(t, pages, builtinBook(t))
	r.g.policy.AddApps(policy.CatBlocked, policy.CategoryEntries{IDs: []string{demo.Dinely}})
	res := r.s.WaitFor(context.Background(), WaitForRequest{Text: "Never", TimeoutMS: 400})
	if len(r.phone.tapped()) != 0 {
		t.Fatalf("a step tapped in a blocked app: %v %s", r.phone.tapped(), res.Message)
	}
}

func TestInterruptCapsStopALoop(t *testing.T) {
	r := newPromptRig(t, savePasswordPages("start"), builtinBook(t))
	res := r.s.WaitFor(context.Background(), WaitForRequest{Text: "Never", TimeoutMS: 800})
	if n := len(r.phone.tapped()); n != 2 {
		t.Fatalf("taps %d, want the entry's max of 2: %s", n, res.Message)
	}
}

func TestUnknownDialogReturnsStaleWithItsButtons(t *testing.T) {
	pages := map[string]pageDef{
		"start": {app: settingsApp, dialog: "Turn On iCloud Keychain?", els: []screen.Element{txt("Turn On iCloud Keychain?", 100), btn("Not Now", 200), btn("Turn On", 260), btn("Back", 400)}},
	}
	r := newPromptRig(t, pages, builtinBook(t))
	obs := r.s.Observe(context.Background(), ObserveRequest{})
	res := r.s.Act(context.Background(), Call{}, Action{Tool: "tap", Ref: ref(t, obs, "Back")})
	mustStatus(t, res, StatusStale)
	if !strings.Contains(res.Message, `interrupt: "Turn On iCloud Keychain?"`) || !strings.Contains(res.Message, `buttons: "Not Now", "Turn On", "Back"`) ||
		!strings.Contains(res.Message, "fix: add an entry") || len(r.phone.tapped()) != 0 {
		t.Fatalf("unknown dialog: taps %v %s", r.phone.tapped(), res.Message)
	}
}

func TestConsentNeedsDeclaredExpectAndATypedCredential(t *testing.T) {
	pages := map[string]pageDef{
		"start": {app: screen.App{Name: "SpringBoard", ID: "com.apple.springboard"}, dialog: "Sign In Requested",
			els: []screen.Element{txt("Sign In Requested", 100), btn("Allow", 200), btn("Don't Allow", 260)}, on: map[string]string{"Allow": "done"}},
		"done": {app: mobileSafari, els: []screen.Element{txt("Welcome", 100)}},
	}
	wait := func(r *promptRig) { r.s.WaitFor(context.Background(), WaitForRequest{Text: "Welcome", TimeoutMS: 400}) }

	plain := newPromptRig(t, pages, builtinBook(t))
	plain.g.markCredentialTyped(leaseOf2(plain))
	wait(plain)
	if len(plain.phone.tapped()) != 0 {
		t.Fatalf("allowed without -expect: %v", plain.phone.tapped())
	}

	declared := newPromptRig(t, pages, builtinBook(t), "sign-in")
	wait(declared)
	if len(declared.phone.tapped()) != 0 {
		t.Fatalf("allowed with no credential typed: %v", declared.phone.tapped())
	}

	both := newPromptRig(t, pages, builtinBook(t), "sign-in")
	both.g.markCredentialTyped(leaseOf2(both))
	res := both.s.WaitFor(context.Background(), WaitForRequest{Text: "Welcome", TimeoutMS: 3000})
	if strings.Join(both.phone.tapped(), ",") != "Allow" || res.Matched == nil || !*res.Matched {
		t.Fatalf("consent did not fire: taps %v %+v", both.phone.tapped(), res)
	}
}

func leaseOf2(r *promptRig) *lease {
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	for _, l := range r.s.leases {
		return l
	}
	return nil
}

func TestScrollUntilEnabledThenTap(t *testing.T) {
	pages := map[string]pageDef{
		"start": {app: appStoreApp, els: []screen.Element{txt("Personalised Ads", 100), btn("Turn Off Personalised Ads", 700, screen.StateDisabled)},
			on: map[string]string{"Turn Off Personalised Ads": "after"}},
		"after": {app: appStoreApp, els: []screen.Element{txt("Welcome", 100)}},
	}
	r := newPromptRig(t, pages, builtinBook(t))
	r.phone.enableAfter = 2
	res := r.s.WaitFor(context.Background(), WaitForRequest{Text: "Welcome", TimeoutMS: 5000})
	if res.Matched == nil || !*res.Matched || r.phone.swipes != 2 || strings.Join(r.phone.tapped(), ",") != "Turn Off Personalised Ads" {
		t.Fatalf("swipes %d taps %v: %+v", r.phone.swipes, r.phone.tapped(), res)
	}
}

func TestReportEntryAddsItsFixAndStopsNothing(t *testing.T) {
	pages := map[string]pageDef{
		"start": {app: mobileSafari, els: []screen.Element{txt("Safari cannot open the page because your iPhone is not connected to the internet.", 100)}},
	}
	r := newPromptRig(t, pages, builtinBook(t))
	res := r.s.WaitFor(context.Background(), WaitForRequest{Text: "Never", TimeoutMS: 300})
	if !strings.Contains(res.Message, "interrupt: no-internet") || !strings.Contains(res.Message, "fix:") {
		t.Fatalf("no report: %s", res.Message)
	}
}

func TestSecretTypingMarksTheCredentialTrigger(t *testing.T) {
	r := newSecretRig(t, demo.Dinely)
	r.stage(t, demo.Dinely, "pw", "hunter2-sentinel")
	l := leaseOf(r)
	if lv := r.g.leaseView(l); len(lv.Triggers) != 0 {
		t.Fatalf("trigger set early: %v", lv.Triggers)
	}
	res := r.typeRef(t, "Password", vault.Ref("pw"), false)
	if res.IsError() || res.Status != StatusSettled {
		t.Fatalf("type: %+v", res)
	}
	if _, ok := r.g.leaseView(l).Triggers[interrupt.TriggerCredentialTyped]; !ok {
		t.Fatal("typing a secret did not mark credential_typed")
	}
}

func TestCodeBoxesEntryTypesTheShownCodeOnce(t *testing.T) {
	r, page := newBoxRig(t, func(p *boxPage) { p.advance = true })
	r.g.opt.Interrupts = builtinBook(t)
	r.g.setExpects(leaseOf(r), []string{"sign-in"})
	r.s.Observe(context.Background(), ObserveRequest{})
	res := r.s.WaitFor(context.Background(), WaitForRequest{Text: "Never", TimeoutMS: 600})
	if page.typedKeys() != shownCodeDigits || !strings.Contains(res.Message, "handled: code-boxes") {
		t.Fatalf("keys %q: %s", page.typedKeys(), res.Message)
	}
	if strings.Contains(resultText(res), shownCodeDigits) {
		t.Fatalf("the code is in the result: %s", resultText(res))
	}
}

func TestCodeBoxesAreLeftAloneWithoutExpect(t *testing.T) {
	r, page := newBoxRig(t, func(p *boxPage) { p.advance = true })
	r.g.opt.Interrupts = builtinBook(t)
	r.s.Observe(context.Background(), ObserveRequest{})
	r.s.WaitFor(context.Background(), WaitForRequest{Text: "Never", TimeoutMS: 300})
	if page.typedKeys() != "" {
		t.Fatalf("typed a code for a task that did not declare sign-in: %q", page.typedKeys())
	}
}
