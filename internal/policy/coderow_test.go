package policy

import (
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

func digitRowEls(digits string, withLabel bool) []screen.Element {
	var els []screen.Element
	if withLabel {
		els = append(els, screen.Element{Role: screen.RoleText, Label: "Enter the verification code", Bounds: screen.Rect{X: 40, Y: 100, W: 300, H: 24}})
	}
	for k, r := range digits {
		els = append(els, screen.Element{Role: screen.RoleText, Label: string(r), Bounds: screen.Rect{X: 40 + k*50, Y: 200, W: 30, H: 40}})
	}
	return els
}

func TestFindCodesDigitRow(t *testing.T) {
	spans := FindCodes(digitRowEls("482913", true))
	if len(spans) != 1 || !spans[0].Row || string(spans[0].Digits) != "482913" {
		t.Fatalf("FindCodes = %+v", spans)
	}
	if got := FindCodes(digitRowEls("482913", false)); len(got) != 0 {
		t.Fatalf("a digit row with nothing about a code is not a code: %+v", got)
	}
	if got := FindCodes(digitRowEls("123", true)); len(got) != 0 {
		t.Fatalf("three digits are not a code: %+v", got)
	}
}

func TestFindCodesOrdersByX(t *testing.T) {
	els := digitRowEls("482913", true)
	els[1], els[6] = els[6], els[1]
	spans := FindCodes(els)
	if len(spans) != 1 || string(spans[0].Digits) != "482913" {
		t.Fatalf("digits not in x order: %+v", spans)
	}
}

func TestFindCodesLabel(t *testing.T) {
	els := []screen.Element{{Role: screen.RoleText, Label: "Your verification code is 482913", Bounds: screen.Rect{X: 10, Y: 10, W: 300, H: 20}}}
	spans := FindCodes(els)
	if len(spans) != 1 || spans[0].Row || string(spans[0].Digits) != "482913" {
		t.Fatalf("FindCodes = %+v", spans)
	}
}

func TestMaskDigitRow(t *testing.T) {
	els := digitRowEls("482913", true)
	out, n := MaskElements(els)
	if n != 6 {
		t.Fatalf("masked %d elements, want 6", n)
	}
	for _, e := range out[1:] {
		if !e.Masked || strings.ContainsAny(e.Label, "0123456789") {
			t.Fatalf("digit element not masked: %+v", e)
		}
	}
	lab := []screen.Element{{Role: screen.RoleText, Label: "Your verification code is 482913", Bounds: screen.Rect{X: 10, Y: 10, W: 300, H: 20}}}
	if out, n := MaskElements(lab); n != 1 || strings.Contains(out[0].Label, "482913") {
		t.Fatalf("a normal six-digit label is no longer masked: %+v", out)
	}
}

func TestInputGroup(t *testing.T) {
	s := &screen.Screen{Width: 400, Height: 800}
	for k := 0; k < 6; k++ {
		s.Elements = append(s.Elements, screen.Element{Ref: k + 1, Role: screen.RoleInput, Bounds: screen.Rect{X: 20 + k*60, Y: 300, W: 44, H: 52}})
	}
	if g := InputGroup(s, 2); len(g) != 6 || g[0] != 0 {
		t.Fatalf("InputGroup = %v", g)
	}
	s.Elements = s.Elements[:3]
	if g := InputGroup(s, 0); g != nil {
		t.Fatalf("three inputs are no group: %v", g)
	}
}
