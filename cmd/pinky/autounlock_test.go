package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/secret"
)

func autoUnlockWith(t *testing.T, env string, c gatewayConfig) (autoUnlock, *secret.AgentPINs, *secret.MemoryPINStore) {
	t.Helper()
	t.Setenv(autoUnlockEnv, env)
	store := &secret.MemoryPINStore{}
	pins := secret.NewAgentPINsOn(store)
	old := autoUnlockPINs
	autoUnlockPINs = func() *secret.AgentPINs { return pins }
	t.Cleanup(func() { autoUnlockPINs = old })
	return newAutoUnlock(c), pins, store
}

func TestDaemonAutoUnlockIsOffUnlessAskedFor(t *testing.T) {
	for name, tc := range map[string]struct {
		env string
		c   gatewayConfig
	}{
		"unset":           {"", gatewayConfig{}},
		"not exactly one": {"true", gatewayConfig{}},
		"fake phones":     {"1", gatewayConfig{Fake: true}},
		"the desktop app": {"1", gatewayConfig{PIN: func(context.Context, string) (string, error) { return "", nil }}},
	} {
		au, _, _ := autoUnlockWith(t, tc.env, tc.c)
		if au.On || au.Outcome != nil || (tc.c.PIN == nil) != (au.PIN == nil) {
			t.Errorf("%s: %+v", name, au)
		}
	}
}

func TestDaemonAutoUnlockUsesTheSavedPasscodeAndLatchesARefusal(t *testing.T) {
	au, pins, store := autoUnlockWith(t, "1", gatewayConfig{})
	if !au.On || au.PIN == nil || au.Outcome == nil {
		t.Fatalf("%+v", au)
	}
	ctx := context.Background()
	if _, err := au.PIN(ctx, "se"); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("PIN with nothing saved: %v", err)
	}
	pins.Save("se", []byte("4711"))
	if got, err := au.PIN(ctx, "se"); err != nil || got != "4711" {
		t.Fatalf("PIN = %q, %v", got, err)
	}
	au.Outcome("se", fmt.Errorf("still locked: %w", driver.ErrLocked))
	if _, err := au.PIN(ctx, "se"); !errors.Is(err, secret.ErrPINRefused) {
		t.Fatalf("PIN after the phone refused it: %v", err)
	}
	if st, _ := secret.NewAgentPINsOn(store).State("se"); !st.Refused {
		t.Fatal("the refusal was not kept in the store, so a restart would forget it")
	}
}

func TestDaemonAutoUnlockOutcomesThatAreNotRefusalsDoNotLatch(t *testing.T) {
	au, pins, _ := autoUnlockWith(t, "1", gatewayConfig{})
	pins.Save("se", []byte("4711"))
	ctx := context.Background()
	au.PIN(ctx, "se")
	au.Outcome("se", fmt.Errorf("no pad: %w", driver.ErrPINNotEntered))
	if _, err := au.PIN(ctx, "se"); !errors.Is(err, secret.ErrPINBackoff) {
		t.Fatalf("after a PIN that was not entered: %v, want ErrPINBackoff", err)
	}
	au.Outcome("se", nil)
	if got, err := au.PIN(ctx, "se"); err != nil || got != "4711" {
		t.Fatalf("after the phone unlocked: %q, %v", got, err)
	}
	if st, _ := pins.State("se"); st.Refused {
		t.Fatal("a PIN that was not refused is marked refused")
	}
}
