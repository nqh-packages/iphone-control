package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

const (
	codeLifetime = 120 * time.Second
	maxRejected  = 2
)

var (
	errNoCode       = errors.New("the phone showed no one-time code lately, or it was used or replaced. fix: observe the phone while the code is on screen, then type {{code}} again")
	errCodeOld      = errors.New("the code the phone showed is too old. fix: ask for a new code, observe while it is on screen, then type {{code}} again")
	errCodeRejected = errors.New("the page rejected the codes typed so far. fix: ask the owner; Pinky types no more codes in this task")
)

var resendWords = []string{"resend", "send again", "send a new", "new code", "get a new", "didn't get", "didn’t get", "not receive"}

type shownCode struct {
	digits []byte
	seenAt time.Time
	mark   [32]byte
}

type codeSlot struct {
	mu       sync.Mutex
	key      [32]byte
	keyed    bool
	cur      *shownCode
	spent    map[[32]byte]bool
	rejected int
	now      func() time.Time
}

func (c *codeSlot) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *codeSlot) markOf(digits []byte) [32]byte {
	if !c.keyed {
		rand.Read(c.key[:])
		c.keyed = true
	}
	h := hmac.New(sha256.New, c.key[:])
	h.Write(digits)
	var m [32]byte
	copy(m[:], h.Sum(nil))
	return m
}

func (c *codeSlot) drop() {
	if c.cur != nil {
		clear(c.cur.digits)
		c.cur = nil
	}
}

func (g *Gateway) keepShownCode(l *lease, raw *screen.Screen) {
	if l == nil || raw == nil {
		return
	}
	var found []byte
	for _, sp := range policy.FindCodes(raw.Elements) {
		if found != nil && string(found) != string(sp.Digits) {
			return
		}
		found = sp.Digits
	}
	if found == nil {
		return
	}
	c := &l.code
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.markOf(found)
	if c.spent[m] || (c.cur != nil && c.cur.mark == m) {
		return
	}
	c.drop()
	c.cur = &shownCode{digits: append([]byte(nil), found...), seenAt: c.clock(), mark: m}
}

func (g *Gateway) peekCode(l *lease) error {
	c := &l.code
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.checkLocked()
}

func (c *codeSlot) checkLocked() error {
	switch {
	case c.rejected >= maxRejected:
		return errCodeRejected
	case c.cur == nil:
		return errNoCode
	case c.clock().Sub(c.cur.seenAt) > codeLifetime:
		return errCodeOld
	}
	return nil
}

func (g *Gateway) takeCode(l *lease) ([]byte, error) {
	c := &l.code
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkLocked(); err != nil {
		return nil, err
	}
	if c.spent == nil {
		c.spent = map[[32]byte]bool{}
	}
	c.spent[c.cur.mark] = true
	digits := append([]byte(nil), c.cur.digits...)
	c.drop()
	return digits, nil
}

func (g *Gateway) codeResent(l *lease) {
	c := &l.code
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil {
		if c.spent == nil {
			c.spent = map[[32]byte]bool{}
		}
		c.spent[c.cur.mark] = true
	}
	c.drop()
}

func (g *Gateway) codeRejected(l *lease) {
	c := &l.code
	c.mu.Lock()
	c.rejected++
	c.mu.Unlock()
}

func isResend(e *screen.Element) bool {
	if e == nil {
		return false
	}
	label := strings.ToLower(e.Label)
	for _, w := range resendWords {
		if strings.Contains(label, w) {
			return true
		}
	}
	return false
}

func (g *Gateway) keepCodeOf(d *device, raw *screen.Screen) {
	g.mu.Lock()
	l := d.lease
	g.mu.Unlock()
	g.keepShownCode(l, raw)
}

func (g *Gateway) enterCode(ctx context.Context, dev driver.Device, a Action, p *prep, text string) error {
	if len(p.boxes) == 0 {
		return scrubSecret(dev.Type(ctx, text, driver.TypeOptions{Append: a.Append, Submit: a.Submit}), a, text)
	}
	if err := typeGroup(ctx, dev, p.boxes, []byte(text), a.Submit); err != nil {
		var gs *groupStopped
		if errors.As(err, &gs) {
			return err
		}
		return scrubSecret(err, a, text)
	}
	if sleepCtx(ctx, g.opt.PollInterval, nil) && boxesCleared(ctx, dev, p.boxes) {
		g.codeRejected(p.lease)
	}
	return nil
}
