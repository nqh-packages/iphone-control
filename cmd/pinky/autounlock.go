package main

import (
	"context"
	"errors"
	"os"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/secret"
)

// autoUnlockEnv turns on unlocking a locked phone from an agent's call, in
// the daemon, with the passcode `pinkyctl phone pin set` saved.
const autoUnlockEnv = "PINKY_AUTO_UNLOCK"

// autoUnlock is what the gateway's Options take for unlocking: the PIN source
// and the callback that hears each entry's outcome.
type autoUnlock struct {
	On      bool
	PIN     func(ctx context.Context, deviceID string) (string, error)
	Outcome func(deviceID string, err error)
}

// autoUnlockPINs builds the daemon's passcode store; tests replace it.
var autoUnlockPINs = secret.NewAgentPINs

// newAutoUnlock turns auto unlock on when PINKY_AUTO_UNLOCK=1 in a daemon with
// real phones. The desktop app (c.PIN set) keeps asking for Touch ID, and fake
// phones have nothing to unlock.
func newAutoUnlock(c gatewayConfig) autoUnlock {
	if os.Getenv(autoUnlockEnv) != "1" || c.Fake || c.PIN != nil {
		return autoUnlock{PIN: c.PIN}
	}
	p := autoUnlockPINs()
	return autoUnlock{On: true, PIN: p.PIN, Outcome: pinOutcome(p)}
}

// pinOutcome tells the PIN store how an entry ended: the phone refusing the
// PIN is kept for good, entering it (or finding the phone unlocked) clears
// the wait, and anything else leaves the store's wait in place.
func pinOutcome(p *secret.AgentPINs) func(string, error) {
	return func(deviceID string, err error) {
		switch {
		case err == nil:
			p.Entered(deviceID)
		case errors.Is(err, driver.ErrLocked):
			p.Refused(deviceID)
		}
	}
}
