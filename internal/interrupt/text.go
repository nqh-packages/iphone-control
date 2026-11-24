package interrupt

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/DIYgod/pinky/internal/screen"
)

func ContainsText(label, text string) bool {
	l, t := strings.ToLower(label), strings.ToLower(text)
	if t == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(t)
	last, _ := utf8.DecodeLastRuneInString(t)
	for i := 0; i <= len(l); {
		j := strings.Index(l[i:], t)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(t)
		before, _ := utf8.DecodeLastRuneInString(l[:start])
		after, _ := utf8.DecodeRuneInString(l[end:])
		if !(wordRune(before) && wordRune(first)) && !(wordRune(last) && wordRune(after)) {
			return true
		}
		_, size := utf8.DecodeRuneInString(l[start:])
		i = start + size
	}
	return false
}

func wordRune(r rune) bool {
	if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
		return false
	}
	return !unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul,
		unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar)
}

func Shows(s *screen.Screen, text string) bool {
	if s == nil {
		return false
	}
	for _, e := range s.Elements {
		if ContainsText(e.Label, text) {
			return true
		}
	}
	for _, ev := range s.Events {
		if ContainsText(ev.Text, text) {
			return true
		}
	}
	return false
}

func Find(s *screen.Screen, label string) (screen.Element, bool) {
	if s == nil {
		return screen.Element{}, false
	}
	var loose *screen.Element
	for i := range s.Elements {
		e := s.Elements[i]
		if e.Bounds.Empty() {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(e.Label), label) {
			return e, true
		}
		if loose == nil && ContainsText(e.Label, label) {
			loose = &s.Elements[i]
		}
	}
	if loose != nil {
		return *loose, true
	}
	return screen.Element{}, false
}
