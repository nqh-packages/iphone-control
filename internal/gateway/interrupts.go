package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/interrupt"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

const (
	maxInterruptsPerCall = 3
	maxScrollSteps       = 5
	interruptUnknownFix  = "fix: add an entry to $PINKY_HOME/interrupts.d/<name>.yaml and check it with `pinkyctl phone interrupts match <file> <screen.txt>`, or tap a button yourself."
)

type interruptState struct {
	mu         sync.Mutex
	expects    []string
	triggers   map[interrupt.Trigger]time.Time
	fired      map[string]int
	rejectSeen map[string]bool
}

type unknownPrompt struct {
	title   string
	buttons []string
}

type cleared struct {
	handled []string
	stuck   string
	reports []string
	unknown *unknownPrompt
}

func (c cleared) acted() bool { return len(c.handled) > 0 }

func (c *cleared) merge(o cleared) {
	c.handled = append(c.handled, o.handled...)
	if o.stuck != "" {
		c.stuck = o.stuck
	}
	for _, r := range o.reports {
		if !containsString(c.reports, r) {
			c.reports = append(c.reports, r)
		}
	}
	if o.unknown != nil {
		c.unknown = o.unknown
	}
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (c cleared) note() string {
	var parts []string
	if len(c.handled) > 0 {
		parts = append(parts, "handled: "+strings.Join(c.handled, ", "))
	}
	if c.stuck != "" {
		parts = append(parts, "stuck: "+c.stuck)
	}
	parts = append(parts, c.reports...)
	if c.unknown != nil {
		parts = append(parts, c.unknown.line())
	}
	return strings.Join(parts, " · ")
}

func (u *unknownPrompt) line() string {
	buttons := make([]string, len(u.buttons))
	for i, b := range u.buttons {
		buttons[i] = fmt.Sprintf("%q", b)
	}
	return fmt.Sprintf("interrupt: %q · buttons: %s", u.title, strings.Join(buttons, ", "))
}

func (g *Gateway) leaseView(l *lease) interrupt.Lease {
	l.intr.mu.Lock()
	v := interrupt.Lease{Expects: append([]string(nil), l.intr.expects...), Now: time.Now(),
		Triggers: map[interrupt.Trigger]time.Time{}, Fired: map[string]int{}}
	for k, t := range l.intr.triggers {
		v.Triggers[k] = t
	}
	for k, n := range l.intr.fired {
		v.Fired[k] = n
	}
	l.intr.mu.Unlock()
	l.code.mu.Lock()
	v.CodeKept = l.code.cur != nil || len(l.code.spent) > 0
	l.code.mu.Unlock()
	return v
}

func (g *Gateway) markCredentialTyped(l *lease) {
	if l == nil {
		return
	}
	l.intr.mu.Lock()
	defer l.intr.mu.Unlock()
	if l.intr.triggers == nil {
		l.intr.triggers = map[interrupt.Trigger]time.Time{}
	}
	l.intr.triggers[interrupt.TriggerCredentialTyped] = time.Now()
}

func (g *Gateway) setExpects(l *lease, expects []string) {
	l.intr.mu.Lock()
	defer l.intr.mu.Unlock()
	l.intr.expects = nil
	for _, e := range expects {
		if e = strings.TrimSpace(e); e != "" && !containsString(l.intr.expects, e) {
			l.intr.expects = append(l.intr.expects, e)
		}
	}
}

func (g *Gateway) countFired(l *lease, name string) {
	l.intr.mu.Lock()
	defer l.intr.mu.Unlock()
	if l.intr.fired == nil {
		l.intr.fired = map[string]int{}
	}
	l.intr.fired[name]++
}

func (g *Gateway) countRejectionOnce(l *lease, name string) {
	l.intr.mu.Lock()
	seen := l.intr.rejectSeen[name]
	if l.intr.rejectSeen == nil {
		l.intr.rejectSeen = map[string]bool{}
	}
	l.intr.rejectSeen[name] = true
	l.intr.mu.Unlock()
	if !seen {
		g.codeRejected(l)
	}
}

func (g *Gateway) interruptView(l *lease, cur *screen.Screen) *screen.Screen {
	v := *cur
	g.mu.Lock()
	v.Events = append(append([]screen.Event(nil), cur.Events...), l.dev.events...)
	g.mu.Unlock()
	return &v
}

func unknownDialog(s *screen.Screen) *unknownPrompt {
	title := ""
	for _, ev := range s.Events {
		if ev.Kind == screen.EventDialog {
			title = strings.TrimSpace(ev.Text)
			break
		}
	}
	if title == "" {
		return nil
	}
	if i := strings.IndexAny(title, "\n"); i >= 0 {
		title = title[:i]
	}
	u := &unknownPrompt{title: title}
	for _, e := range s.Elements {
		if e.Role == screen.RoleButton && strings.TrimSpace(e.Label) != "" {
			u.buttons = append(u.buttons, strings.TrimSpace(e.Label))
		}
	}
	return u
}

func (g *Gateway) clearInterrupts(ctx context.Context, l *lease, cur *screen.Screen) (cleared, *screen.Screen) {
	var out cleared
	book := g.opt.Interrupts
	if book == nil || cur == nil || cur.Locked {
		return out, cur
	}
	ran := map[string]bool{}
	for range maxInterruptsPerCall + 1 {
		view := g.interruptView(l, cur)
		lv := g.leaseView(l)
		if rej, ok := book.Rejected(view, lv); ok {
			g.countRejectionOnce(l, rej.Name)
		}
		e, ok := book.Match(view, lv)
		if !ok {
			out.unknown = unknownDialog(view)
			return out, cur
		}
		if e.Kind == interrupt.Report {
			out.reports = append(out.reports, "interrupt: "+e.Name+" · "+e.Report)
			return out, cur
		}
		if ran[e.Name] || len(ran) >= maxInterruptsPerCall {
			out.stuck = e.Name
			return out, cur
		}
		ran[e.Name] = true
		g.countFired(l, e.Name)
		next, why := g.runEntry(ctx, l, e, cur)
		if next != nil {
			cur = next
		}
		if why != "" {
			out.stuck = e.Name + " (" + why + ")"
			return out, cur
		}
		out.handled = append(out.handled, e.Name)
	}
	return out, cur
}

func (g *Gateway) stepCall(name string) Call {
	return Call{Intent: "interrupt " + name, noAsk: true, noInterrupts: true}
}

func (g *Gateway) reread(ctx context.Context, l *lease, cur *screen.Screen) *screen.Screen {
	if next, err := g.observeOCR(ctx, l.dev, ocrAll); err == nil {
		return next
	}
	return cur
}

func (g *Gateway) runStep(ctx context.Context, l *lease, name string, a Action) string {
	res := g.act(ctx, l, g.stepCall(name), a)
	if res.IsError() || res.Status != StatusSettled {
		msg := strings.TrimSpace(res.Message)
		if msg == "" {
			msg = string(res.Status)
		}
		return msg
	}
	return ""
}

func (g *Gateway) runEntry(ctx context.Context, l *lease, e *interrupt.Entry, cur *screen.Screen) (*screen.Screen, string) {
	for _, st := range e.Steps {
		switch st.Op {
		case interrupt.OpWait:
			sleepCtx(ctx, st.Wait, nil)
		case interrupt.OpTap:
			el, ok := interrupt.Find(cur, st.Label)
			if !ok {
				return cur, "no " + st.Label + " on screen"
			}
			c := el.Bounds.Center()
			if why := g.runStep(ctx, l, e.Name, Action{Tool: "tap", Point: &c}); why != "" {
				return cur, why
			}
			cur = g.reread(ctx, l, cur)
		case interrupt.OpScrollUntilEnabled:
			for i := 0; ; i++ {
				if el, ok := interrupt.Find(cur, st.Label); ok && !el.Has(screen.StateDisabled) {
					break
				}
				if i >= maxScrollSteps {
					return cur, st.Label + " stayed disabled"
				}
				if why := g.runStep(ctx, l, e.Name, Action{Tool: "swipe", Direction: "up"}); why != "" {
					return cur, why
				}
				cur = g.reread(ctx, l, cur)
			}
		case interrupt.OpTypeCode:
			ref := firstBoxRef(cur)
			if ref == 0 {
				return cur, "no code boxes on screen"
			}
			if why := g.runStep(ctx, l, e.Name, Action{Tool: "type", Ref: ref, Text: "{{code}}"}); why != "" {
				return cur, why
			}
			cur = g.reread(ctx, l, cur)
		}
	}
	return cur, ""
}

func firstBoxRef(s *screen.Screen) int {
	for i := range s.Elements {
		if idx := policy.InputGroup(s, i); len(idx) > 0 {
			return s.Elements[idx[0]].Ref
		}
	}
	return 0
}

func (g *Gateway) act(ctx context.Context, l *lease, c Call, a Action) *Result {
	if g.opt.Interrupts == nil || c.noInterrupts || c.ApprovalID != "" {
		return g.actCore(ctx, l, c, a)
	}
	g.mu.Lock()
	cached, dialogUp := l.dev.last, l.dev.dialogUp
	g.mu.Unlock()
	if cached == nil || cached.Locked {
		return g.actCore(ctx, l, c, a)
	}
	book := g.opt.Interrupts
	view := g.interruptView(l, cached)
	if _, hit := book.Match(view, g.leaseView(l)); !hit && !dialogUp {
		return g.actCore(ctx, l, c, a)
	}
	fresh := g.reread(ctx, l, cached)
	out, last := g.clearInterrupts(ctx, l, fresh)
	switch {
	case aimed(a) && out.unknown != nil:
		return g.attachScreen(l, statusResult(StatusStale, "%s\nNothing was done: a prompt no interrupt entry covers is in front. %s", out.note(), interruptUnknownFix), last)
	case aimed(a) && (out.acted() || out.stuck != ""):
		return g.attachScreen(l, statusResult(StatusStale, "%s\nNothing was done: the screen changed under the call. Read it again, then aim again.", out.note()), last)
	}
	res := g.actCore(ctx, l, c, a)
	if note := out.note(); note != "" && res != nil {
		res.Message = strings.TrimSpace(res.Message + "\n" + note)
	}
	return res
}

func hasDialogEvent(evs []screen.Event) bool {
	for _, ev := range evs {
		if ev.Kind == screen.EventDialog {
			return true
		}
	}
	return false
}
