//go:build !darwin

package secret

// NewAgentPINs has no keychain to keep passcodes in on this system: every
// call answers ErrUnsupported.
func NewAgentPINs() *AgentPINs { return NewAgentPINsOn(unsupportedPINs{}) }

type unsupportedPINs struct{}

func (unsupportedPINs) Save(string, string) error   { return ErrUnsupported }
func (unsupportedPINs) Delete(string) error         { return ErrUnsupported }
func (unsupportedPINs) Exists(string) (bool, error) { return false, ErrUnsupported }
func (unsupportedPINs) Read(string) (string, error) { return "", ErrUnsupported }

// NewAgentLogins has no keychain to keep passwords in on this system.
func NewAgentLogins() PINStore { return unsupportedPINs{} }
