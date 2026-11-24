package gateway

import (
	"errors"
	"fmt"
	"strings"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/vault"
)

const (
	noSecretStore   = "This Pinky has no secret store, so {{secret:NAME}} cannot be typed. Nothing was done. fix: run the fork's daemon (pinky daemon) or call phone_handoff."
	secretsNeedHide = "Pinky types no secret while \"Hide codes from agents\" is off, because the typed value could then show in a screenshot. Nothing was done. fix: the owner turns on Hide codes from agents in Pinky's Settings › Safety (mask_codes in rules.yaml)."
)

// secretReady checks a type step whose text is a secret reference, before
// the policy and any approval: the text is exactly one reference, a store
// exists, codes are masked (so pictures of a typed value get covered), and
// the value is staged for the app in front. It marks the policy input as a
// secret, so the policy judges the field and the app. Text that holds no
// reference passes untouched.
func (g *Gateway) secretReady(a Action, p *prep) *Result {
	name, isRef, err := vault.ParseRef(a.Text)
	if err != nil {
		return errResult(ErrInvalidArgument, "%v. Nothing was done.", err)
	}
	if !isRef {
		return nil
	}
	p.input.Secret = true
	switch {
	case g.opt.Secrets == nil:
		return &Result{Status: StatusBlocked, Rule: policy.RuleCredentials, Message: noSecretStore}
	case !g.policy.HideCodes():
		return &Result{Status: StatusBlocked, Rule: policy.RuleCredentials, Message: secretsNeedHide}
	}
	err = g.opt.Secrets.Staged(name, p.input.App)
	if err == nil || errors.Is(err, vault.ErrAppNotAllowed) {
		return nil
	}
	return errResult(ErrNotFound, "%s. Nothing was done.%s", err, secretFix(name, p.input.App, err))
}

// secretFix is the fix line for a secret that is not staged.
func secretFix(name string, app screen.App, err error) string {
	if !errors.Is(err, vault.ErrNotStaged) {
		return ""
	}
	return fmt.Sprintf(" fix: stage it again: cred run --login <id> --reason '...' --stdin '{password}' -- pinkyctl phone secret put -app %s %s", app.ID, name)
}

// secretRefused is the error for a secret that cannot be taken at the last
// moment. It counts as driver.ErrNotFound, so the agent hears that nothing
// was done.
type secretRefused struct{ msg string }

func (e *secretRefused) Error() string        { return e.msg }
func (e *secretRefused) Is(target error) bool { return target == driver.ErrNotFound }

// secretText returns what perform types: the action's text, or for a
// reference the staged value, taken now and so used up. A refusal wraps
// driver.ErrNotFound, so the agent hears that nothing was done.
func (g *Gateway) secretText(a Action, p *prep) (string, error) {
	name, isRef, _ := vault.ParseRef(a.Text)
	if !isRef {
		return a.Text, nil
	}
	if !p.input.Secret || g.opt.Secrets == nil {
		return "", &secretRefused{"a secret reference was not checked"}
	}
	value, err := g.opt.Secrets.TakeSecret(name, p.input.App)
	if err != nil {
		return "", &secretRefused{err.Error() + "." + secretFix(name, p.input.App, err)}
	}
	return value, nil
}

// maskRead hides the values typed lately from a read of the screen. Tests
// replace it to prove the canary can fail.
var maskRead = func(v *vault.Vault, s *screen.Screen) { v.Mask(s) }

// maskSecrets hides the values typed lately from a read of the screen.
func (g *Gateway) maskSecrets(s *screen.Screen) {
	if g.opt.Secrets != nil {
		maskRead(g.opt.Secrets, s)
	}
}

type scrubbed struct {
	msg string
	err error
}

func (e *scrubbed) Error() string { return e.msg }
func (e *scrubbed) Unwrap() error { return e.err }

// scrubSecret keeps a driver's error from carrying the value it was given:
// for a secret, the value in the message becomes dots.
func scrubSecret(err error, a Action, typed string) error {
	if err == nil || typed == a.Text || !strings.Contains(err.Error(), typed) {
		return err
	}
	return &scrubbed{msg: strings.ReplaceAll(err.Error(), typed, "••••"), err: err}
}
