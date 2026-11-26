package screen

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// Icon is a detector's guess at an icon the element tree does not name.
// Label comes from the detector's closed set of meanings, never from text
// in the pixels. An Icon is not an Element: it has no ref, the policy and
// the interrupt book never see it, and an agent taps it by its point.
// Bounds are in the screen's pixel space, like Element.Bounds.
type Icon struct {
	Label  string
	Score  float32
	Bounds Rect
}

// IconRead says what a read's icon list means.
type IconRead uint8

const (
	// IconsNotLooked says no detector looked: none is installed, or the
	// read took no picture or may not use it. The list says nothing.
	IconsNotLooked IconRead = iota
	// IconsLooked says the list is the detector's whole answer.
	IconsLooked
	// IconsLate says a detector is installed but did not answer in time.
	IconsLate
)

const iconPrefix = "icon? "

// SetGuessedIcons records a read's icons. Only the driver's read calls it.
func (s *Screen) SetGuessedIcons(read IconRead, icons []Icon) {
	s.iconRead = read
	s.icons = nil
	if read == IconsLooked {
		s.icons = slices.Clone(icons)
	}
}

// IsIconLine reports whether a rendered screen line is an icon guess.
func IsIconLine(line string) bool { return strings.HasPrefix(line, iconPrefix) }

func (i Icon) line() string {
	c := i.Bounds.Center()
	return fmt.Sprintf("%s%q @%d,%d", iconPrefix, i.Label, c.X, c.Y)
}

func (s *Screen) shownIcons() []Icon {
	var out []Icon
	for _, i := range s.icons {
		c := i.Bounds.Center()
		covered := slices.ContainsFunc(s.Elements, func(e Element) bool {
			return (e.Hidden || e.Masked) && e.Bounds.Contains(c)
		})
		if !covered {
			out = append(out, i)
		}
	}
	slices.SortStableFunc(out, func(a, b Icon) int {
		ac, bc := a.Bounds.Center(), b.Bounds.Center()
		return cmp.Or(cmp.Compare(ac.Y, bc.Y), cmp.Compare(ac.X, bc.X))
	})
	return out
}

func sameIcon(a, b Icon) bool {
	if a.Label != b.Label {
		return false
	}
	side := min(a.Bounds.W, a.Bounds.H, b.Bounds.W, b.Bounds.H)
	ac, bc := a.Bounds.Center(), b.Bounds.Center()
	dx, dy := ac.X-bc.X, ac.Y-bc.Y
	return 4*(dx*dx+dy*dy) <= side*side
}

func iconDiff(prev, next *Screen) []string {
	if next.iconRead != IconsLooked {
		return nil
	}
	before, after := prev.shownIcons(), next.shownIcons()
	kept := make([]bool, len(before))
	var lines []string
	for _, n := range after {
		matched := false
		for i, p := range before {
			if !kept[i] && sameIcon(p, n) {
				kept[i], matched = true, true
				break
			}
		}
		if !matched {
			lines = append(lines, "+ "+n.line())
		}
	}
	if prev.iconRead == IconsLooked {
		for i, p := range before {
			if !kept[i] {
				lines = append(lines, "- "+p.line())
			}
		}
	}
	return lines
}
