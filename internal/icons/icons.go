// Package icons finds the icons in a screen picture with a detector that is
// installed as data: a manifest names the model, the runtime that runs it,
// what each of its classes means and when a guess is worth showing. The
// detector runs in a process of its own, so that its crash or hang costs
// only the icons.
//
// NOTICE: the letterbox, score floor and overlap defaults follow
// icon_detector.py of advnt-screen-parser (MIT). No code of it is copied,
// and no model weights are part of this repository or the program.
package icons

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"image"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

const lookBudget = 350 * time.Millisecond

// Box is one guess in a picture that passed the manifest's thresholds: a
// meaning, a score, the apps it may be shown in (empty: any), and a box
// from 0 to 1 from the picture's top-left corner.
type Box struct {
	Label      string
	Score      float32
	Apps       []string
	X, Y, W, H float64
}

// Found is what one look at a picture saw, before it knows the screen. The
// zero value did not look.
type Found struct {
	Read screen.IconRead
	// Orientation is the only screen orientation the detector reads; empty:
	// any.
	Orientation string
	// Overlap is how much two boxes may overlap (intersection over union)
	// before only the stronger one is kept; 0 keeps both.
	Overlap float64
	Boxes   []Box
}

func (m *manifest) reads(c image.Config) bool {
	return m.Orientation == "" || orientationOf(c.Width, c.Height) == m.Orientation
}

func orientationOf(w, h int) string {
	if w > h {
		return "landscape"
	}
	return "portrait"
}

// Look finds icons in pic, an encoded upright screen picture (JPEG, PNG or
// BMP). It returns within its budget and never fails: with no detector in
// use, or a picture of an orientation the detector does not read, it did
// not look; a detector that is starting, busy, broken or slow is late. The
// picture is held in memory only.
func Look(ctx context.Context, pic []byte) Found {
	s := current.Load()
	if s == nil || len(pic) == 0 {
		return Found{}
	}
	deadline := time.NewTimer(lookBudget)
	defer deadline.Stop()
	if c, _, err := image.DecodeConfig(bytes.NewReader(pic)); err == nil && !s.m.reads(c) {
		return Found{}
	}
	late := Found{Read: screen.IconsLate}
	r := &request{pic: pic, done: make(chan outcome, 1)}
	if len(pic) > maxPicture || !s.offer(r) {
		return late
	}
	select {
	case a := <-r.done:
		if a.err != nil {
			return late
		}
		f := Found{Read: screen.IconsLooked, Orientation: s.m.Orientation, Overlap: s.m.NMSIoU}
		for _, d := range a.found {
			if d.Why == "" {
				f.Boxes = append(f.Boxes, d.box())
			}
		}
		return f
	case <-deadline.C:
	case <-ctx.Done():
	}
	return late
}

func (d detection) box() Box {
	return Box{Label: d.Label, Score: d.Score, Apps: d.Apps, X: d.X, Y: d.Y, W: d.W, H: d.H}
}

func (b Box) rect(width, height int) screen.Rect {
	at := func(v float64, size int) int { return int(math.Round(v * float64(size))) }
	x, y := at(b.X, width), at(b.Y, height)
	return screen.Rect{X: x, Y: y, W: at(b.X+b.W, width) - x, H: at(b.Y+b.H, height) - y}
}

func (f Found) sift(app string) []string {
	why := make([]string, len(f.Boxes))
	order := make([]int, len(f.Boxes))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(f.Boxes[b].Score, f.Boxes[a].Score) })
	var kept []Box
	for _, i := range order {
		b := f.Boxes[i]
		switch {
		case len(b.Apps) > 0 && !slices.Contains(b.Apps, app):
			why[i] = "not for this app (" + strings.Join(b.Apps, ", ") + ")"
		case f.Overlap > 0 && slices.ContainsFunc(kept, func(k Box) bool {
			return iou(k.X, k.Y, k.X+k.W, k.Y+k.H, b.X, b.Y, b.X+b.W, b.Y+b.H) > f.Overlap
		}):
			why[i] = "overlaps a stronger guess"
		default:
			kept = append(kept, b)
		}
	}
	return why
}

// For places f's boxes on s and returns the guesses worth showing: none on
// a screen of an orientation the detector does not read; else those for the
// app in front, the stronger of two that overlap, and none whose centre
// lies on the keyboard, on a covered part of the screen, or in a labelled
// tree element smaller than a sixth of the screen, which the tree already
// names.
func (f Found) For(s *screen.Screen) []screen.Icon {
	if s.Width <= 0 || s.Height <= 0 || f.Orientation != "" && s.Orientation != f.Orientation {
		return nil
	}
	why := f.sift(s.App.ID)
	var out []screen.Icon
	for i, b := range f.Boxes {
		r := b.rect(s.Width, s.Height)
		c := r.Center()
		if why[i] != "" || s.Keyboard && s.KeyboardFrame.Contains(c) ||
			slices.ContainsFunc(s.Covered, func(v screen.Rect) bool { return v.Contains(c) }) ||
			slices.ContainsFunc(s.Elements, func(e screen.Element) bool {
				return e.Source != screen.SourceOCR && strings.TrimSpace(e.Label) != "" &&
					e.Bounds.W*e.Bounds.H*6 < s.Width*s.Height && e.Bounds.Contains(c)
			}) {
			continue
		}
		out = append(out, screen.Icon{Label: b.Label, Score: b.Score, Bounds: r})
	}
	return out
}

// Verdict is one raw detection and what a read would do with it.
type Verdict struct {
	Class, Label string
	Score        float32
	X, Y, W, H   float64
	Kept         bool
	// Why says why the detection was dropped; empty when it was kept.
	Why string
}

// Bounds is the verdict's box in a picture of the given size, placed as a
// read places it.
func (v Verdict) Bounds(width, height int) screen.Rect {
	return Box{X: v.X, Y: v.Y, W: v.W, H: v.H}.rect(width, height)
}

// Explain looks at a saved picture of app with the detector in use, waiting
// as long as ctx allows, and gives a verdict on every detection, strongest
// first, with how long the look took. It judges what Look and For do
// without an element tree.
func Explain(ctx context.Context, pic []byte, app string) ([]Verdict, time.Duration, error) {
	s := current.Load()
	if s == nil {
		return nil, 0, errors.New("no detector is in use")
	}
	c, _, err := image.DecodeConfig(bytes.NewReader(pic))
	if err != nil {
		return nil, 0, errors.New("not a JPEG, PNG or BMP picture")
	}
	if len(pic) > maxPicture {
		return nil, 0, errors.New("the picture is over 32 MB")
	}
	r := &request{pic: pic, done: make(chan outcome, 1)}
	if err := s.offerWhenFree(ctx, r); err != nil {
		return nil, 0, err
	}
	start := time.Now()
	var a outcome
	select {
	case a = <-r.done:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	took := time.Since(start)
	if a.err != nil {
		return nil, took, a.err
	}
	unread := ""
	if !s.m.reads(c) {
		unread = "the detector does not read a " + orientationOf(c.Width, c.Height) + " picture"
	}
	passed := Found{Overlap: s.m.NMSIoU}
	var at []int
	out := make([]Verdict, len(a.found))
	for i, d := range a.found {
		out[i] = Verdict{Class: d.Class, Label: d.Label, Score: d.Score, X: d.X, Y: d.Y, W: d.W, H: d.H, Why: cmp.Or(unread, d.Why)}
		if out[i].Why == "" {
			passed.Boxes, at = append(passed.Boxes, d.box()), append(at, i)
		}
	}
	for j, why := range passed.sift(app) {
		out[at[j]].Why = why
	}
	for i := range out {
		out[i].Kept = out[i].Why == ""
	}
	return out, took, nil
}
