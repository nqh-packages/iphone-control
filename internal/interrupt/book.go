package interrupt

import (
	"time"

	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

type Kind string

const (
	Dismiss    Kind = "dismiss"
	Consent    Kind = "consent"
	Credential Kind = "credential"
	Report     Kind = "report"
)

type Shape string

const (
	ShapeCodeShown Shape = "code_shown"
	ShapeCodeField Shape = "code_field"
)

type Trigger string

const TriggerCredentialTyped Trigger = "credential_typed"

type Op string

const (
	OpTap                Op = "tap"
	OpScrollUntilEnabled Op = "scroll_until_enabled"
	OpWait               Op = "wait"
	OpTypeCode           Op = "type_code"
)

type When struct {
	Apps   []string
	Texts  []string
	Any    []string
	Button string
	Shape  Shape
}

type Step struct {
	Op    Op
	Label string
	Wait  time.Duration
}

type Entry struct {
	Name     string
	Kind     Kind
	When     When
	Steps    []Step
	Expect   string
	After    Trigger
	Window   time.Duration
	Max      int
	Rejected []string
	Report   string
	Source   string
}

type Lease struct {
	Expects  []string
	Triggers map[Trigger]time.Time
	Fired    map[string]int
	CodeKept bool
	Now      time.Time
}

type Book struct{ entries []Entry }

func (b *Book) Entries() []Entry {
	if b == nil {
		return nil
	}
	return append([]Entry(nil), b.entries...)
}

func HasShape(s *screen.Screen, sh Shape) bool {
	if s == nil {
		return false
	}
	switch sh {
	case ShapeCodeShown:
		for _, e := range s.Elements {
			if e.Masked {
				return true
			}
		}
		return len(policy.FindCodes(s.Elements)) > 0
	case ShapeCodeField:
		for i := range s.Elements {
			if policy.InputGroup(s, i) != nil {
				return true
			}
		}
	}
	return false
}

func (w When) matches(s *screen.Screen) bool {
	if len(w.Apps) > 0 && !appIn(s.App, w.Apps) {
		return false
	}
	for _, t := range w.Texts {
		if !Shows(s, t) {
			return false
		}
	}
	if len(w.Any) > 0 {
		any := false
		for _, t := range w.Any {
			any = any || Shows(s, t)
		}
		if !any {
			return false
		}
	}
	if w.Button != "" {
		if _, ok := Find(s, w.Button); !ok {
			return false
		}
	}
	return w.Shape == "" || HasShape(s, w.Shape)
}

func appIn(a screen.App, ids []string) bool {
	for _, id := range ids {
		if id == a.ID || id == a.Name {
			return true
		}
	}
	return false
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (e *Entry) rejectedOn(s *screen.Screen) bool {
	for _, t := range e.Rejected {
		if Shows(s, t) {
			return true
		}
	}
	return false
}

func (b *Book) ready(e *Entry, l Lease) bool {
	if l.Fired[e.Name] >= e.Max {
		return false
	}
	switch e.Kind {
	case Consent:
		at, ok := l.Triggers[e.After]
		if !ok || !has(l.Expects, e.Expect) || l.Now.Sub(at) > e.Window || l.Now.Before(at) {
			return false
		}
	case Credential:
		if !has(l.Expects, e.Expect) {
			return false
		}
	}
	return e.When.Shape != ShapeCodeShown || l.CodeKept
}

func (b *Book) Match(s *screen.Screen, l Lease) (*Entry, bool) {
	if b == nil || s == nil {
		return nil, false
	}
	for i := range b.entries {
		e := &b.entries[i]
		if !e.When.matches(s) || !b.ready(e, l) || e.rejectedOn(s) {
			continue
		}
		return e, true
	}
	return nil, false
}

func (b *Book) Rejected(s *screen.Screen, l Lease) (*Entry, bool) {
	if b == nil || s == nil {
		return nil, false
	}
	for i := range b.entries {
		e := &b.entries[i]
		if e.Kind == Credential && has(l.Expects, e.Expect) && e.When.matches(s) && e.rejectedOn(s) {
			return e, true
		}
	}
	return nil, false
}
