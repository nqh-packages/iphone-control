package secret

import (
	"context"
	"errors"
	"sync"
	"time"
)

// AgentService is the keychain service of the passcodes the fork's daemon
// enters by itself. It is not Service: a passcode saved in Pinky.app can never
// be read here, and these are never read with Touch ID. Only the pinky binary
// that saved an item may read it (the item's access list), and reading never
// shows a prompt.
const AgentService = "app.pinky.agent"

// Errors of AgentPINs. ErrNotFound means nothing is saved.
var (
	ErrPINRefused     = errors.New("secret: the phone refused this saved passcode; Pinky will not enter it again")
	ErrPINBackoff     = errors.New("secret: the last unlock stopped before the passcode was entered; Pinky waits before trying again")
	ErrKeychainLocked = errors.New("secret: the Mac's login keychain is locked, so the saved passcode cannot be read")
	ErrKeychainDenied = errors.New("secret: the keychain did not let this build of pinky read the passcode without a prompt")
)

// PINState is what can be known about a saved passcode without reading it.
type PINState struct{ Saved, Refused bool }

// PINStore keeps AgentPINs' items. None of its calls may ask the person for
// anything: Exists and Read answer ErrKeychainLocked or ErrKeychainDenied
// instead.
type PINStore interface {
	Save(account, value string) error
	Delete(account string) error
	Exists(account string) (bool, error)
	Read(account string) (string, error)
}

// pinBackoff is how long a passcode that was handed out but never entered
// waits before it is handed out again.
const pinBackoff = time.Minute

const (
	pinMinLen = 4
	pinMaxLen = 64
)

// ErrPINInvalid is the answer of AgentPINs.Save to a passcode the phone's pad
// cannot take.
var ErrPINInvalid = errors.New("secret: a passcode is 4 to 64 printable ASCII characters without spaces")

func refusedAccount(deviceID string) string { return "refused:" + deviceID }

func validPIN(pin []byte) bool {
	if len(pin) < pinMinLen || len(pin) > pinMaxLen {
		return false
	}
	for _, c := range pin {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

// AgentPINs keeps phone passcodes for unlocking without a person. It owns
// the attempt state: a passcode the phone refused stays refused, also across
// restarts, until a new one is saved; one that was handed out and not
// entered waits pinBackoff.
type AgentPINs struct {
	store PINStore
	now   func() time.Time

	mu      sync.Mutex
	next    map[string]time.Time
	refused map[string]bool
}

// NewAgentPINsOn keeps passcodes in store.
func NewAgentPINsOn(store PINStore) *AgentPINs {
	return &AgentPINs{store: store, now: time.Now, next: map[string]time.Time{}, refused: map[string]bool{}}
}

// Save keeps a device's passcode and forgets that an earlier one was refused.
// It fails for a passcode the phone's pad cannot take.
func (p *AgentPINs) Save(deviceID string, pin []byte) error {
	if !validPIN(pin) {
		return ErrPINInvalid
	}
	if err := p.store.Save(PINAccount(deviceID), string(pin)); err != nil {
		return err
	}
	if err := p.store.Delete(refusedAccount(deviceID)); err != nil {
		return err
	}
	p.forget(deviceID)
	return nil
}

// Delete forgets a device's passcode and its refusal.
func (p *AgentPINs) Delete(deviceID string) error {
	if err := p.store.Delete(PINAccount(deviceID)); err != nil {
		return err
	}
	if err := p.store.Delete(refusedAccount(deviceID)); err != nil {
		return err
	}
	p.forget(deviceID)
	return nil
}

func (p *AgentPINs) forget(deviceID string) {
	p.mu.Lock()
	delete(p.next, deviceID)
	delete(p.refused, deviceID)
	p.mu.Unlock()
}

// State says whether a passcode is saved and whether the phone refused it,
// without reading the passcode.
func (p *AgentPINs) State(deviceID string) (PINState, error) {
	saved, err := p.store.Exists(PINAccount(deviceID))
	if err != nil || !saved {
		return PINState{}, err
	}
	refused, err := p.store.Exists(refusedAccount(deviceID))
	p.mu.Lock()
	defer p.mu.Unlock()
	return PINState{Saved: true, Refused: refused || p.refused[deviceID]}, err
}

// PIN hands out the passcode for gateway.Options.PIN. It answers ErrNotFound
// when nothing is saved, ErrPINRefused after the phone refused it,
// ErrPINBackoff while an earlier handout is unanswered, and the keychain's
// errors when the passcode cannot be read without a prompt. A passcode handed
// out counts as tried until Entered says it need not be.
func (p *AgentPINs) PIN(ctx context.Context, deviceID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	st, err := p.State(deviceID)
	switch {
	case err != nil:
		return "", err
	case !st.Saved:
		return "", ErrNotFound
	case st.Refused:
		return "", ErrPINRefused
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.now().Before(p.next[deviceID]) {
		return "", ErrPINBackoff
	}
	pin, err := p.store.Read(PINAccount(deviceID))
	if err != nil {
		return "", err
	}
	p.next[deviceID] = p.now().Add(pinBackoff)
	return pin, nil
}

// Refused records that the phone refused the passcode just handed out. The
// refusal is kept in the store, so it holds after a restart, and in memory in
// case the store cannot take it. Only Save clears it.
func (p *AgentPINs) Refused(deviceID string) {
	p.mu.Lock()
	p.refused[deviceID] = true
	p.mu.Unlock()
	_ = p.store.Save(refusedAccount(deviceID), "1")
}

// Entered records that the passcode just handed out unlocked the phone, or
// was not needed, so it need not wait before the next handout.
func (p *AgentPINs) Entered(deviceID string) {
	p.mu.Lock()
	delete(p.next, deviceID)
	p.mu.Unlock()
}

// PINFix says how to get past one of AgentPINs' errors for a device, in the
// words of a fix: line. It is empty for an error it knows no fix for.
func PINFix(err error, deviceID string) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "the owner runs `pinkyctl phone pin set -device " + deviceID + "`"
	case errors.Is(err, ErrKeychainLocked):
		return "unlock the Mac's login keychain: log in on the Mac, or turn off Lock after sleep for it"
	case errors.Is(err, ErrKeychainDenied):
		return "the owner runs `pinkyctl phone pin set -device " + deviceID + "` with this build of pinky"
	case errors.Is(err, ErrPINRefused):
		return "check the passcode on the phone, then the owner runs `pinkyctl phone pin set -device " + deviceID + "`"
	case errors.Is(err, ErrPINBackoff):
		return "call again in a minute"
	case errors.Is(err, ErrUnsupported):
		return "use a Mac: only its keychain keeps the passcode"
	}
	return ""
}
