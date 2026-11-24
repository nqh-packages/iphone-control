package vault

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

const (
	// DefaultTTL is how long a staged secret lasts when the stager names no
	// lifetime, and MaxTTL the longest it may ask for.
	DefaultTTL = 120 * time.Second
	MaxTTL     = 300 * time.Second

	typedFor  = 10 * time.Minute
	minLength = 4
	maxSlots  = 16
	maskDots  = "••••"
)

var (
	// ErrNotStaged is returned when no unused, unexpired secret has the name.
	ErrNotStaged = errors.New("vault: secret not staged")
	// ErrWrongApp is returned when the secret was staged for another app than
	// the one in front.
	ErrWrongApp = errors.New("vault: secret staged for another app")
	// ErrAppNotAllowed is returned when the app is not one the owner lets
	// agents type secrets into.
	ErrAppNotAllowed = errors.New("vault: app not allowed for secrets")
	// ErrBadSecret is returned for a name or value that cannot be kept: an
	// invalid name, or a value too short to mask on the screen.
	ErrBadSecret = errors.New("vault: secret cannot be kept")
)

type refusal struct {
	kind error
	msg  string
}

func (r *refusal) Error() string        { return r.msg }
func (r *refusal) Is(target error) bool { return target == r.kind }

type slot struct {
	app     string
	value   []byte
	expires time.Time
}

type typed struct {
	value []byte
	until time.Time
}

// Vault keeps staged secrets: each is bound to one app by bundle id, used
// once and forgotten after its lifetime. It also remembers, only to mask
// them, the values it handed out lately. It is safe for concurrent use.
type Vault struct {
	mu      sync.Mutex
	slots   map[string]*slot
	typed   []typed
	now     func() time.Time
	allowed func(appID string) bool
}

// New returns an empty vault. allowed is the policy's credential_apps check
// (Engine.SecretApp); it is asked again at Put and at TakeSecret, so
// removing an app from the list stops a secret already staged for it.
func New(allowed func(appID string) bool) *Vault {
	return &Vault{slots: map[string]*slot{}, now: time.Now, allowed: allowed}
}

// Put stages values by name for one app, for ttl (DefaultTTL when zero, at
// most MaxTTL), each used once. It takes copies of the values, which the
// caller should wipe. A name staged again replaces the earlier value.
func (v *Vault) Put(app string, ttl time.Duration, named map[string][]byte) error {
	if app == "" || !v.allowed(app) {
		return &refusal{ErrAppNotAllowed, fmt.Sprintf("%s is not on credential_apps", app)}
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	ttl = min(ttl, MaxTTL)
	for name, val := range named {
		if !ValidName(name) {
			return &refusal{ErrBadSecret, errBadRef}
		}
		if len(val) < minLength {
			return &refusal{ErrBadSecret, fmt.Sprintf("secret %s is shorter than %d characters, which Pinky cannot mask on the screen", name, minLength)}
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	v.pruneLocked(now)
	for name := range named {
		if _, replaced := v.slots[name]; !replaced && len(v.slots) >= maxSlots {
			return &refusal{ErrBadSecret, "too many secrets are staged; use or wait out the others"}
		}
	}
	for name, val := range named {
		if old := v.slots[name]; old != nil {
			wipe(old.value)
		}
		v.slots[name] = &slot{app: app, value: append([]byte(nil), val...), expires: now.Add(ttl)}
	}
	return nil
}

// Staged reports whether TakeSecret would hand out name for app now,
// without using it: nil, or ErrNotStaged, ErrWrongApp or ErrAppNotAllowed.
func (v *Vault) Staged(name string, app screen.App) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, err := v.checkLocked(name, app)
	return err
}

// TakeSecret hands out name's value for app once and forgets it. The value
// is kept for typedFor so Mask can hide it. App matching is by bundle id,
// exactly.
func (v *Vault) TakeSecret(name string, app screen.App) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s, err := v.checkLocked(name, app)
	if err != nil {
		return "", err
	}
	delete(v.slots, name)
	v.typed = append(v.typed, typed{value: s.value, until: v.now().Add(typedFor)})
	return string(s.value), nil
}

func (v *Vault) checkLocked(name string, app screen.App) (*slot, error) {
	v.pruneLocked(v.now())
	if app.ID == "" || !v.allowed(app.ID) {
		return nil, &refusal{ErrAppNotAllowed, fmt.Sprintf("%s is not on credential_apps", appLabel(app))}
	}
	s := v.slots[name]
	if s == nil {
		return nil, &refusal{ErrNotStaged, fmt.Sprintf("no secret named %s is staged for %s (staged secrets last %d s and are used once)", name, app.ID, int(DefaultTTL/time.Second))}
	}
	if s.app != app.ID {
		return nil, &refusal{ErrWrongApp, fmt.Sprintf("%s was staged for %s, but %s is in front", name, s.app, app.ID)}
	}
	return s, nil
}

func (v *Vault) pruneLocked(now time.Time) {
	for name, s := range v.slots {
		if !now.Before(s.expires) {
			wipe(s.value)
			delete(v.slots, name)
		}
	}
	kept := v.typed[:0]
	for _, t := range v.typed {
		if now.Before(t.until) {
			kept = append(kept, t)
		} else {
			wipe(t.value)
		}
	}
	for i := len(kept); i < len(v.typed); i++ {
		v.typed[i] = typed{}
	}
	v.typed = kept
}

// Mask replaces, wherever it shows in a read, every value typed in the last
// ten minutes with "••••": Element.Label (iOS puts a field's typed value
// there), Element.Content, OCR elements and Event.Text. It marks the changed
// elements Masked, so pictures of them are covered. Element.ID and Path are
// platform identifiers, never typed text, and are left alone. The screen gets
// new element and event slices when anything changes. It reports how many
// elements and events it changed.
func (v *Vault) Mask(s *screen.Screen) int {
	if s == nil {
		return 0
	}
	v.mu.Lock()
	v.pruneLocked(v.now())
	values := make([]string, len(v.typed))
	for i, t := range v.typed {
		values[i] = string(t.value)
	}
	v.mu.Unlock()
	if len(values) == 0 {
		return 0
	}
	n := 0
	var els []screen.Element
	for i, e := range s.Elements {
		label, content := maskText(e.Label, values), maskText(e.Content, values)
		if label == e.Label && content == e.Content {
			continue
		}
		if els == nil {
			els = append([]screen.Element(nil), s.Elements...)
		}
		els[i].Label, els[i].Content, els[i].Masked = label, content, true
		n++
	}
	if els != nil {
		s.Elements = els
	}
	var evs []screen.Event
	for i, ev := range s.Events {
		text := maskText(ev.Text, values)
		if text == ev.Text {
			continue
		}
		if evs == nil {
			evs = append([]screen.Event(nil), s.Events...)
		}
		evs[i].Text = text
		n++
	}
	if evs != nil {
		s.Events = evs
	}
	return n
}

func maskText(text string, values []string) string {
	for _, val := range values {
		if text != "" && strings.Contains(text, val) {
			text = strings.ReplaceAll(text, val, maskDots)
		}
	}
	return text
}

func appLabel(app screen.App) string {
	if app.ID != "" {
		return app.ID
	}
	if app.Name != "" {
		return app.Name
	}
	return "this app"
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
