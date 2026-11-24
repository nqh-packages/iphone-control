package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

const shownCodeDigits = "482913"

type boxPage struct {
	driver.Device
	mu         sync.Mutex
	digits     string
	vals       [6]string
	focus      int
	advance    bool
	dropBox    int
	rejectLast bool
	keys       []string
	pasted     int
}

func newBoxPage(inner driver.Device) *boxPage {
	return &boxPage{Device: inner, digits: shownCodeDigits, dropBox: -1}
}

func (b *boxPage) elements() []screen.Element {
	els := []screen.Element{{Role: screen.RoleText, Label: "Enter the verification code", Bounds: screen.Rect{X: 20, Y: 60, W: 300, H: 24}}}
	for k, r := range b.digits {
		els = append(els, screen.Element{Role: screen.RoleText, Label: string(r), Bounds: screen.Rect{X: 20 + k*50, Y: 100, W: 30, H: 40}})
	}
	for k := range b.vals {
		els = append(els, screen.Element{Role: screen.RoleInput, Label: "Digit " + string(rune('1'+k)), Content: b.vals[k],
			Bounds: screen.Rect{X: 20 + k*50, Y: 300, W: 30, H: 40}})
	}
	return els
}

func (b *boxPage) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, err := b.Device.Observe(ctx, opt)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c := *s
	c.Elements = b.elements()
	return &c, nil
}

func (b *boxPage) Tap(ctx context.Context, p screen.Point, opt driver.TapOptions) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, e := range b.elements() {
		if e.Role == screen.RoleInput && e.Bounds.Contains(p) {
			b.focus = k - 1 - len(b.digits)
			return nil
		}
	}
	return nil
}

func (b *boxPage) Type(ctx context.Context, text string, opt driver.TypeOptions) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case text == "":
		b.vals[b.focus] = ""
	case !opt.ByKeys:
		b.pasted++
	default:
		b.keys = append(b.keys, text)
		if b.focus != b.dropBox {
			b.vals[b.focus] = text
		}
		if b.focus == 5 && b.rejectLast {
			b.vals = [6]string{}
			b.focus = 0
		} else if b.advance && b.focus < 5 {
			b.focus++
		}
	}
	return nil
}

func (b *boxPage) typedKeys() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.keys, "")
}

func newBoxRig(t *testing.T, setup func(*boxPage)) (*secretRig, *boxPage) {
	t.Helper()
	var page *boxPage
	r := newSecretRigWith(t, func(d driver.Device) driver.Device {
		page = newBoxPage(d)
		if setup != nil {
			setup(page)
		}
		return page
	})
	return r, page
}

func leaseOf(r *secretRig) *lease {
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	for _, l := range r.s.leases {
		return l
	}
	return nil
}

func enterCodeAt(t *testing.T, r *secretRig) *Result {
	t.Helper()
	obs := r.s.Observe(context.Background(), ObserveRequest{})
	return r.s.Act(context.Background(), Call{}, Action{Tool: "type", Ref: ref(t, obs, "Digit 1"), Text: "{{code}}"})
}

func TestCodeSlotFilledBeforeMaskingAndNeverInOutput(t *testing.T) {
	r, _ := newBoxRig(t, nil)
	obs := r.s.Observe(context.Background(), ObserveRequest{})
	blob, _ := json.Marshal(obs)
	if strings.Contains(string(blob)+resultText(obs), shownCodeDigits) {
		t.Fatalf("the code is in the observe result: %s", blob)
	}
	for _, e := range obs.Screen.Elements {
		if e.Role == screen.RoleText && len(e.Label) == 1 && e.Label >= "0" && e.Label <= "9" {
			t.Fatalf("a digit element was not masked: %+v", e)
		}
	}
	got, err := r.g.takeCode(leaseOf(r))
	if err != nil || string(got) != shownCodeDigits {
		t.Fatalf("slot = %q, %v", got, err)
	}
}

func TestCodeSlotSingleUseExpiryResendAndNewest(t *testing.T) {
	l := &lease{}
	g := &Gateway{}
	now := time.Unix(1000, 0)
	l.code.now = func() time.Time { return now }
	raw := func(code string) *screen.Screen {
		return &screen.Screen{Elements: append(digitEls(code), screen.Element{Role: screen.RoleText, Label: "Enter the verification code", Bounds: screen.Rect{X: 20, Y: 60, W: 300, H: 24}})}
	}
	g.keepShownCode(l, raw("111222"))
	g.keepShownCode(l, raw("333444"))
	got, err := g.takeCode(l)
	if err != nil || string(got) != "333444" {
		t.Fatalf("the newest code must win: %q %v", got, err)
	}
	if _, err := g.takeCode(l); err == nil {
		t.Fatal("a code was handed out twice")
	}
	g.keepShownCode(l, raw("333444"))
	if _, err := g.takeCode(l); err == nil {
		t.Fatal("a used code came back from the screen")
	}
	g.keepShownCode(l, raw("555666"))
	now = now.Add(codeLifetime + time.Second)
	if _, err := g.takeCode(l); err == nil {
		t.Fatal("an expired code was handed out")
	}
	g.keepShownCode(l, raw("777888"))
	g.codeResent(l)
	if _, err := g.takeCode(l); err == nil {
		t.Fatal("a code seen before a resend was handed out")
	}
	g.keepShownCode(l, raw("777888"))
	if _, err := g.takeCode(l); err == nil {
		t.Fatal("the code the resend replaced came back")
	}
	if !isResend(&screen.Element{Label: "Resend code"}) || isResend(&screen.Element{Label: "Continue"}) {
		t.Fatal("isResend misjudges labels")
	}
}

func digitEls(code string) []screen.Element {
	var els []screen.Element
	for k, r := range code {
		els = append(els, screen.Element{Role: screen.RoleText, Label: string(r), Bounds: screen.Rect{X: 20 + k*50, Y: 100, W: 30, H: 40}})
	}
	return els
}

func TestCodeEntryTypesByKeysNotPaste(t *testing.T) {
	for _, advance := range []bool{true, false} {
		r, page := newBoxRig(t, func(p *boxPage) { p.advance = advance })
		res := enterCodeAt(t, r)
		if res.IsError() || res.Status != StatusSettled {
			t.Fatalf("advance=%v: %+v", advance, res)
		}
		want := shownCodeDigits
		if !advance {
			want = shownCodeDigits[:5] + shownCodeDigits
		}
		if page.typedKeys() != want || page.pasted != 0 || strings.Join(page.vals[:], "") != shownCodeDigits {
			t.Fatalf("advance=%v: keys %q pasted %d boxes %q", advance, page.typedKeys(), page.pasted, page.vals)
		}
		if strings.Contains(resultText(res), shownCodeDigits) {
			t.Fatalf("the code is in the result: %s", resultText(res))
		}
	}
}

func TestCodeEntryStopsBeforeLastDigitOnMismatch(t *testing.T) {
	r, page := newBoxRig(t, func(p *boxPage) { p.advance = true; p.dropBox = 2 })
	res := enterCodeAt(t, r)
	if res.Status != StatusUncertain {
		t.Fatalf("status %q: %+v", res.Status, res)
	}
	for _, k := range page.keys {
		if k == "3" {
			t.Fatalf("the last digit was sent: %q", page.typedKeys())
		}
	}
	msg := resultText(res)
	if strings.Contains(msg, shownCodeDigits) || !strings.Contains(msg, "of 5") {
		t.Fatalf("the message must carry counts only: %q", msg)
	}
}

func TestCodeEntryNeverRetypesARejectedCode(t *testing.T) {
	r, page := newBoxRig(t, func(p *boxPage) { p.advance = true; p.rejectLast = true })
	if res := enterCodeAt(t, r); res.IsError() {
		t.Fatalf("first entry: %+v", res)
	}
	if page.typedKeys() != shownCodeDigits {
		t.Fatalf("keys %q", page.typedKeys())
	}
	l := leaseOf(r)
	l.code.mu.Lock()
	rejected := l.code.rejected
	l.code.mu.Unlock()
	if rejected != 1 {
		t.Fatalf("rejected counter = %d, want 1", rejected)
	}
	before := len(page.keys)
	res := enterCodeAt(t, r)
	if !res.IsError() {
		t.Fatalf("the rejected code was typed again: %+v", res)
	}
	if len(page.keys) != before {
		t.Fatalf("keys were sent for a rejected code: %q", page.typedKeys())
	}
}
