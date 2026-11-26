package interrupt

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/DIYgod/pinky/internal/screen"
)

var (
	elementLine = regexp.MustCompile(`^(?:\[(\d+)\] (\S+)|ocr)(?: ("(?:[^"\\]|\\.)*"))?(?: @(-?\d+),(-?\d+))?(.*)$`)
	eventLine   = regexp.MustCompile(`^(toast|notification|dialog): (?:\S+ · )?("(?:[^"\\]|\\.)*")$`)
	headerSize  = regexp.MustCompile(`^screen (\d+)x(\d+)`)
	headerApp   = regexp.MustCompile(`app: ([^·]*?)(?: \(([^)]*)\))?(?: ·|$)`)
	sizeToken   = regexp.MustCompile(`size=(\d+)x(\d+)`)
)

const (
	defaultBoxW = 160
	defaultBoxH = 44
)

func ParseScreen(text string) (*screen.Screen, error) {
	s := &screen.Screen{Width: 400, Height: 800}
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || screen.IsIconLine(line) {
			continue
		}
		if m := headerSize.FindStringSubmatch(line); m != nil {
			s.Width, _ = strconv.Atoi(m[1])
			s.Height, _ = strconv.Atoi(m[2])
			if a := headerApp.FindStringSubmatch(line); a != nil {
				s.App = screen.App{Name: a[1], ID: a[2]}
			}
			continue
		}
		if m := eventLine.FindStringSubmatch(line); m != nil {
			t, _ := strconv.Unquote(m[2])
			s.Events = append(s.Events, screen.Event{Kind: screen.EventKind(m[1]), Text: t})
			continue
		}
		m := elementLine.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("line %d is not a screen line: %q", n+1, line)
		}
		e := screen.Element{Source: screen.SourceTree, Role: m[2]}
		if m[2] == "" {
			e.Source = screen.SourceOCR
		}
		e.Ref, _ = strconv.Atoi(m[1])
		if m[3] != "" {
			e.Label, _ = strconv.Unquote(m[3])
		}
		w, h := defaultBoxW, defaultBoxH
		if sz := sizeToken.FindStringSubmatch(m[6]); sz != nil {
			w, _ = strconv.Atoi(sz[1])
			h, _ = strconv.Atoi(sz[2])
		}
		if m[4] != "" {
			cx, _ := strconv.Atoi(m[4])
			cy, _ := strconv.Atoi(m[5])
			e.Bounds = screen.Rect{X: cx - w/2, Y: cy - h/2, W: w, H: h}
		}
		for _, f := range strings.Fields(sizeToken.ReplaceAllString(m[6], "")) {
			e.States = append(e.States, f)
		}
		s.Elements = append(s.Elements, e)
	}
	return s, nil
}
