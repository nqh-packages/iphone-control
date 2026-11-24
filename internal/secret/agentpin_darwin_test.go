//go:build darwin

package secret

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
)

const testAgentService = "app.pinky.agent.test"

type statusRow struct {
	name   string
	status int32
	locked bool
	want   error
}

func TestAgentPINKeychainStatusMapping(t *testing.T) {
	s := mustSecurity(t)
	for _, r := range []statusRow{
		{"not found", errSecItemNotFound, false, ErrNotFound},
		{"locked keychain", errSecInteractionNotAllowed, true, ErrKeychainLocked},
		{"a prompt the access list would need", errSecInteractionNotAllowed, false, ErrKeychainDenied},
		{"auth failed", errSecAuthFailed, true, ErrKeychainDenied},
		{"cancelled", errSecUserCanceled, true, ErrKeychainDenied},
		{"ok", errSecSuccess, false, nil},
	} {
		got := pinStatusError(s, "read", r.status, func() bool { return r.locked })
		if !errors.Is(got, r.want) || (r.want == nil && got != nil) {
			t.Errorf("%s: got %v, want %v", r.name, got, r.want)
		}
		if r.want != nil && r.want != ErrNotFound && errors.Is(got, ErrNotFound) {
			t.Errorf("%s: became ErrNotFound", r.name)
		}
	}
}

func testAgentPINs(t *testing.T) (*AgentPINs, string) {
	t.Helper()
	if err := setInteractionAllowed(false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { setInteractionAllowed(true) })
	var b [6]byte
	rand.Read(b[:])
	id := "test-" + hex.EncodeToString(b[:])
	p := NewAgentPINsOn(newAgentKeychain(testAgentService))
	t.Cleanup(func() {
		if err := p.Delete(id); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return p, id
}

func TestAgentPINKeychainRoundTrip(t *testing.T) {
	p, id := testAgentPINs(t)
	if st, err := p.State(id); err != nil || st.Saved {
		t.Fatalf("State before Save = %+v, %v", st, err)
	}
	err := p.Save(id, []byte(sentinelPIN))
	skipIfLocked(t, err)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := p.State(id); err != nil || !st.Saved || st.Refused {
		t.Fatalf("State after Save = %+v, %v", st, err)
	}
	got, err := p.PIN(context.Background(), id)
	if err != nil || got != sentinelPIN {
		t.Fatalf("PIN = %q, %v", got, err)
	}
	p.Refused(id)

	again := NewAgentPINsOn(newAgentKeychain(testAgentService))
	if _, err := again.PIN(context.Background(), id); !errors.Is(err, ErrPINRefused) {
		t.Fatalf("a new AgentPINs after a refusal = %v, want ErrPINRefused", err)
	}
	if err := again.Save(id, []byte("4711")); err != nil {
		t.Fatal(err)
	}
	if got, err := again.PIN(context.Background(), id); err != nil || got != "4711" {
		t.Fatalf("PIN after a new Save = %q, %v", got, err)
	}
}

func TestAgentPINKeychainKeepsItsServiceApart(t *testing.T) {
	p, id := testAgentPINs(t)
	err := p.Save(id, []byte(sentinelPIN))
	skipIfLocked(t, err)
	if err != nil {
		t.Fatal(err)
	}
	if System().Has(PINAccount(id)) {
		t.Fatal("Pinky.app's keychain service sees the daemon's passcode")
	}
}
