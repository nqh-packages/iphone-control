package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/secret"
)

// unlockEstimate is what one unlock takes on the phone, and autoUnlockReserve
// the budget a call keeps for the read or action after it. unlockEstimate is
// a labelled calibration, not a decoded value: it is set from a measurement
// on the phone and is not unlockTimeout's ceiling.
const (
	unlockEstimate    = 12 * time.Second
	autoUnlockReserve = 8 * time.Second
)

// autoUnlocker is the gateway's state for unlocking without the person: when
// each phone's lock was last read for it, and whose turn it is to unlock.
type autoUnlocker struct {
	mu      sync.Mutex
	checked map[string]time.Time
	turns   map[string]chan struct{}
}

func (a *autoUnlocker) turn(id string) chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.turns == nil {
		a.turns = map[string]chan struct{}{}
	}
	if a.turns[id] == nil {
		a.turns[id] = make(chan struct{}, 1)
	}
	return a.turns[id]
}

func (a *autoUnlocker) lockReadRecently(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Since(a.checked[id]) < wakeFresh
}

func (a *autoUnlocker) markLockRead(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.checked == nil {
		a.checked = map[string]time.Time{}
	}
	a.checked[id] = time.Now()
}

// autoUnlock unlocks the lease's phone when Options.AutoUnlock is on and the
// phone is locked, nobody holds it, and the call has time; it reports whether
// the phone is unlocked now. It must be called without d.act: it goes through
// Unlock, which takes it. Why it did not unlock goes to the agent's next
// result (l.bgNote).
func (g *Gateway) autoUnlock(ctx context.Context, l *lease) bool {
	if !g.opt.AutoUnlock || g.opt.PIN == nil || !g.autoUnlockWelcome(l) {
		return false
	}
	d := l.dev
	if !g.lockedForAutoUnlock(ctx, d) {
		return true
	}
	if left, ok := budgetLeft(ctx); ok && left < unlockEstimate+autoUnlockReserve {
		g.noteLease(l, "This call has too little time left to unlock the phone. Pinky unlocks it at the start of your next call; call phone_observe.")
		return false
	}
	turn := g.auto.turn(d.id)
	select {
	case turn <- struct{}{}:
		defer func() { <-turn }()
	case <-ctx.Done():
		return false
	}
	if !g.cachedLocked(d) {
		return true
	}
	err := g.Unlock(d.id)
	g.log.Info("auto unlock", "device", d.id, "ok", err == nil, "why", causeOf(err, "").why)
	if err != nil {
		g.noteLease(l, unlockNote(d.name(), d.id, err))
		return false
	}
	return true
}

// autoUnlockWelcome reports whether an unlock may happen for this lease at
// all: not while the person holds the phone, used it in the last minute, or
// the agent may only watch.
func (g *Gateway) autoUnlockWelcome(l *lease) bool {
	d := l.dev
	g.mu.Lock()
	away := !l.ended && l.bg == nil && !d.person && time.Since(d.personAt) >= personRecent
	g.mu.Unlock()
	return away && g.scopeOf(l.session).Mode != ScopeWatchOnly
}

func (g *Gateway) cachedLocked(d *device) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return d.locked
}

// lockedForAutoUnlock reports whether the phone is locked: the cached state,
// or one fresh read of it when the cache says unlocked and no read was made
// within wakeFresh.
func (g *Gateway) lockedForAutoUnlock(ctx context.Context, d *device) bool {
	if g.cachedLocked(d) {
		return true
	}
	if g.auto.lockReadRecently(d.id) {
		return false
	}
	g.checkLock(ctx, d)
	g.auto.markLockRead(d.id)
	return g.cachedLocked(d)
}

func (g *Gateway) noteLease(l *lease, note string) {
	g.mu.Lock()
	if !strings.Contains(l.bgNote, note) {
		l.bgNote = joinNote(l.bgNote, note)
	}
	g.mu.Unlock()
}

// pinOutcome tells Options.PINOutcome how an entry of a saved PIN ended.
func (g *Gateway) pinOutcome(deviceID string, err error) {
	if g.opt.PINOutcome != nil {
		g.opt.PINOutcome(deviceID, err)
	}
}

// unlockCause says why an unlock did not happen: a fixed code safe to log, the
// reason in words, and the error whose fix line applies.
type unlockCause struct {
	why, what string
	fix       error
}

func causeOf(err error, name string) unlockCause {
	switch {
	case err == nil:
		return unlockCause{}
	case errors.Is(err, secret.ErrNotFound):
		return unlockCause{"no_pin", fmt.Sprintf("no passcode is saved for %s", name), secret.ErrNotFound}
	case errors.Is(err, secret.ErrPINRefused):
		return unlockCause{"pin_refused", "the phone refused the saved passcode earlier, and Pinky never enters it twice", secret.ErrPINRefused}
	case errors.Is(err, ErrWrongPIN), errors.Is(err, driver.ErrLocked):
		return unlockCause{"wrong_pin", "the phone refused the saved passcode, and Pinky will not enter it again", secret.ErrPINRefused}
	case errors.Is(err, secret.ErrPINBackoff):
		return unlockCause{"pin_backoff", "the last try stopped before the passcode was entered, and Pinky waits a minute", secret.ErrPINBackoff}
	case errors.Is(err, driver.ErrPINNotEntered):
		return unlockCause{"not_entered", "Pinky could not get to the passcode screen and entered nothing, and it waits a minute", secret.ErrPINBackoff}
	case errors.Is(err, secret.ErrKeychainLocked):
		return unlockCause{"keychain_locked", "the Mac's login keychain is locked, so the saved passcode cannot be read", secret.ErrKeychainLocked}
	case errors.Is(err, secret.ErrKeychainDenied):
		return unlockCause{"keychain_denied", "the keychain does not let this build of pinky read the saved passcode without a prompt", secret.ErrKeychainDenied}
	}
	return unlockCause{why: "unavailable"}
}

// unlockNote words an unlock that did not happen for the agent, with a fix
// line. It never quotes the error, which may come from a driver.
func unlockNote(name, deviceID string, err error) string {
	c := causeOf(err, name)
	if c.fix == nil {
		return "Pinky could not unlock it. fix: call phone_handoff so the person can unlock it."
	}
	return fmt.Sprintf("Pinky did not unlock it: %s. fix: %s.", c.what, secret.PINFix(c.fix, deviceID))
}
