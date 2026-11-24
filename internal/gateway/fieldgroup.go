package gateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

type groupStopped struct{ msg string }

func (e *groupStopped) Error() string { return e.msg }

type entryMode int

const (
	modeAdvance entryMode = iota
	modeTapEach
)

func findGroup(s *screen.Screen, ref int) []screen.Rect {
	if s == nil || ref <= 0 {
		return nil
	}
	for i, e := range s.Elements {
		if e.Ref != ref {
			continue
		}
		idx := policy.InputGroup(s, i)
		boxes := make([]screen.Rect, len(idx))
		for k, j := range idx {
			boxes[k] = s.Elements[j].Bounds
		}
		return boxes
	}
	return nil
}

func boxValue(e screen.Element) (string, bool) {
	if e.Content != "" {
		return strings.TrimSpace(e.Content), true
	}
	if _, v, ok := strings.Cut(e.Label, ", "); ok {
		return strings.TrimSpace(v), true
	}
	if e.Has(screen.StatePassword) {
		return "", false
	}
	if len([]rune(e.Label)) == 1 && e.Label >= "0" && e.Label <= "9" {
		return e.Label, true
	}
	return "", true
}

func readBoxes(ctx context.Context, dev driver.Device, boxes []screen.Rect) ([]string, bool, error) {
	s, err := dev.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		return nil, false, err
	}
	vals := make([]string, len(boxes))
	readable := false
	for k, b := range boxes {
		c := b.Center()
		for _, e := range s.Elements {
			if (e.Role == screen.RoleInput || e.Has(screen.StateEditable)) && e.Bounds.Contains(c) {
				v, ok := boxValue(e)
				vals[k], readable = v, readable || ok
				break
			}
		}
	}
	return vals, readable, nil
}

func prefixMatches(vals []string, digits []byte, n int) int {
	ok := 0
	for k := 0; k < n; k++ {
		if vals[k] == string(digits[k]) {
			ok++
		}
	}
	return ok
}

func typeKey(ctx context.Context, dev driver.Device, ch byte, submit bool) error {
	return dev.Type(ctx, string(ch), driver.TypeOptions{Append: true, ByKeys: true, Submit: submit})
}

func clearBoxes(ctx context.Context, dev driver.Device, boxes []screen.Rect) {
	for _, b := range boxes {
		if dev.Tap(ctx, b.Center(), driver.TapOptions{}) != nil {
			return
		}
		dev.Type(ctx, "", driver.TypeOptions{ByKeys: true})
	}
}

func fillPrefix(ctx context.Context, dev driver.Device, boxes []screen.Rect, digits []byte, mode entryMode) error {
	n := len(boxes)
	for k := 0; k < n-1; k++ {
		if k == 0 || mode == modeTapEach {
			if err := dev.Tap(ctx, boxes[k].Center(), driver.TapOptions{}); err != nil {
				return err
			}
		}
		if err := typeKey(ctx, dev, digits[k], false); err != nil {
			return err
		}
	}
	return nil
}

func typeGroup(ctx context.Context, dev driver.Device, boxes []screen.Rect, digits []byte, submit bool) error {
	n := len(boxes)
	if len(digits) != n {
		return &groupStopped{fmt.Sprintf("The page has %d code boxes and the code has %d digits. Nothing was typed.", n, len(digits))}
	}
	var used entryMode
	for _, mode := range []entryMode{modeAdvance, modeTapEach} {
		used = mode
		if err := fillPrefix(ctx, dev, boxes, digits, mode); err != nil {
			return err
		}
		vals, readable, err := readBoxes(ctx, dev, boxes)
		if err != nil {
			return err
		}
		if !readable {
			break
		}
		ok := prefixMatches(vals, digits, n-1)
		if ok == n-1 && vals[n-1] == "" {
			break
		}
		clearBoxes(ctx, dev, boxes)
		if mode == modeTapEach {
			return &groupStopped{fmt.Sprintf("Typing the code stopped before its last digit: %d of %d digits read back right. The code was not submitted. Observe the boxes before anything else; do not retype this code.", ok, n-1)}
		}
	}
	if used == modeTapEach {
		if err := dev.Tap(ctx, boxes[n-1].Center(), driver.TapOptions{}); err != nil {
			return err
		}
	}
	return typeKey(ctx, dev, digits[n-1], submit)
}

func boxesCleared(ctx context.Context, dev driver.Device, boxes []screen.Rect) bool {
	vals, readable, err := readBoxes(ctx, dev, boxes)
	if err != nil || !readable {
		return false
	}
	for _, v := range vals {
		if v != "" {
			return false
		}
	}
	return true
}
