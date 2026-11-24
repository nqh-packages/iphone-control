//go:build darwin

package secret

import (
	"errors"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// NewAgentPINs keeps passcodes in the login keychain under AgentService.
func NewAgentPINs() *AgentPINs { return NewAgentPINsOn(newAgentKeychain(AgentService)) }

// agentKeychain is the keychain store whose reads never ask the person: a
// locked keychain, or an access list that does not trust this build of
// pinky, answers with an error at once.
type agentKeychain struct{ keychain }

func newAgentKeychain(service string) agentKeychain { return agentKeychain{keychain{service: service}} }

const kSecUnlockStateStatus = 1

var errNoQuietReads = errors.New("secret: this macOS cannot turn the keychain's prompts off, so the saved passcode is not read")

var loadKeychainStatus = sync.OnceValue(func() (fns struct {
	copyDefault func(*cf) int32
	getStatus   func(cf, *uint32) int32
}) {
	lib, err := purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return
	}
	for name, fptr := range map[string]any{"SecKeychainCopyDefault": &fns.copyDefault, "SecKeychainGetStatus": &fns.getStatus} {
		sym, err := purego.Dlsym(lib, name)
		if err != nil || sym == 0 {
			fns.copyDefault, fns.getStatus = nil, nil
			return
		}
		purego.RegisterFunc(fptr, sym)
	}
	return
})

func defaultKeychainLocked(s *security) bool {
	fns := loadKeychainStatus()
	if fns.copyDefault == nil {
		return false
	}
	var kc cf
	if fns.copyDefault(&kc) != errSecSuccess || kc == 0 {
		return false
	}
	defer s.CFRelease(kc)
	var state uint32
	return fns.getStatus(kc, &state) == errSecSuccess && state&kSecUnlockStateStatus == 0
}

func pinStatusError(s *security, op string, status int32, locked func() bool) error {
	switch status {
	case errSecInteractionNotAllowed:
		if locked() {
			return ErrKeychainLocked
		}
		return ErrKeychainDenied
	case errSecAuthFailed, errSecUserCanceled:
		return ErrKeychainDenied
	}
	return s.statusError(op, status)
}

func (k agentKeychain) quiet(s *security, o *objects, q cf) error {
	if s.kSecUseAuthenticationUI == 0 {
		return errNoQuietReads
	}
	o.set(q, s.kSecUseAuthenticationUI, s.kSecUseAuthenticationUIFail)
	return nil
}

// Exists reports whether the account has an item, from its attributes
// alone: it does not read the value, and never shows a prompt.
func (k agentKeychain) Exists(account string) (bool, error) {
	s, err := loadSecurity()
	if err != nil {
		return false, err
	}
	o := &objects{s: s}
	defer o.release()
	q := k.query(o, account)
	o.set(q, s.kSecMatchLimit, s.kSecMatchLimitOne)
	if err := k.quiet(s, o, q); err != nil {
		return false, err
	}
	err = pinStatusError(s, "find", s.SecItemCopyMatching(q, nil), func() bool { return defaultKeychainLocked(s) })
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Read returns the account's value without ever showing a prompt.
func (k agentKeychain) Read(account string) (string, error) {
	s, err := loadSecurity()
	if err != nil {
		return "", err
	}
	o := &objects{s: s}
	defer o.release()
	q := k.query(o, account)
	o.set(q, s.kSecReturnData, s.cfTrue)
	o.set(q, s.kSecMatchLimit, s.kSecMatchLimitOne)
	if err := k.quiet(s, o, q); err != nil {
		return "", err
	}
	var data cf
	if err := pinStatusError(s, "read", s.SecItemCopyMatching(q, &data), func() bool { return defaultKeychainLocked(s) }); err != nil {
		return "", err
	}
	if data == 0 {
		return "", errors.New("secret: keychain read: no data")
	}
	defer s.CFRelease(data)
	n := s.CFDataGetLength(data)
	if n == 0 {
		return "", nil
	}
	return string(unsafe.Slice((*byte)(s.CFDataGetBytePtr(data)), n)), nil
}
