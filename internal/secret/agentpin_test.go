package secret

import (
	"context"
	"errors"
	"testing"
	"time"
)

const sentinelPIN = "S3NT1NEL-7f2a"

func newTestPINs(store PINStore) (*AgentPINs, *time.Time) {
	now := time.Unix(1_000_000, 0)
	p := NewAgentPINsOn(store)
	p.now = func() time.Time { return now }
	return p, &now
}

func TestAgentPINSaveThenHandOut(t *testing.T) {
	p, _ := newTestPINs(&MemoryPINStore{})
	ctx := context.Background()
	if _, err := p.PIN(ctx, "se"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PIN before Save = %v, want ErrNotFound", err)
	}
	if st, err := p.State("se"); err != nil || st != (PINState{}) {
		t.Fatalf("State before Save = %+v, %v", st, err)
	}
	if err := p.Save("se", []byte(sentinelPIN)); err != nil {
		t.Fatal(err)
	}
	if st, err := p.State("se"); err != nil || st != (PINState{Saved: true}) {
		t.Fatalf("State after Save = %+v, %v", st, err)
	}
	got, err := p.PIN(ctx, "se")
	if err != nil || got != sentinelPIN {
		t.Fatalf("PIN = %q, %v", got, err)
	}
}

func TestAgentPINSaveRefusesWhatThePadCannotType(t *testing.T) {
	p, _ := newTestPINs(&MemoryPINStore{})
	for _, pin := range []string{"", "123", "12 34", "12\n34", "1234é", "12\t4"} {
		if err := p.Save("se", []byte(pin)); !errors.Is(err, ErrPINInvalid) {
			t.Errorf("Save(%q) = %v, want ErrPINInvalid", pin, err)
		}
	}
	if st, _ := p.State("se"); st.Saved {
		t.Fatal("a refused passcode was saved")
	}
}

func TestAgentPINWaitsAMinuteAfterAHandoutThatWasNotAnswered(t *testing.T) {
	store := &MemoryPINStore{}
	p, now := newTestPINs(store)
	ctx := context.Background()
	p.Save("se", []byte(sentinelPIN))
	if _, err := p.PIN(ctx, "se"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.PIN(ctx, "se"); !errors.Is(err, ErrPINBackoff) {
		t.Fatalf("second PIN = %v, want ErrPINBackoff", err)
	}
	if store.Reads() != 1 {
		t.Fatalf("the passcode was read %d times", store.Reads())
	}
	*now = now.Add(pinBackoff + time.Second)
	if _, err := p.PIN(ctx, "se"); err != nil {
		t.Fatalf("PIN after the wait = %v", err)
	}
}

func TestAgentPINEnteredClearsTheWait(t *testing.T) {
	p, _ := newTestPINs(&MemoryPINStore{})
	ctx := context.Background()
	p.Save("se", []byte(sentinelPIN))
	p.PIN(ctx, "se")
	p.Entered("se")
	if _, err := p.PIN(ctx, "se"); err != nil {
		t.Fatalf("PIN after Entered = %v", err)
	}
}

func TestAgentPINRefusedHoldsAcrossRestartsUntilSave(t *testing.T) {
	store := &MemoryPINStore{}
	p, _ := newTestPINs(store)
	ctx := context.Background()
	p.Save("se", []byte(sentinelPIN))
	p.PIN(ctx, "se")
	p.Refused("se")

	restarted, now := newTestPINs(store)
	*now = now.Add(24 * time.Hour)
	if _, err := restarted.PIN(ctx, "se"); !errors.Is(err, ErrPINRefused) {
		t.Fatalf("PIN after a refusal and a restart = %v, want ErrPINRefused", err)
	}
	if st, _ := restarted.State("se"); st != (PINState{Saved: true, Refused: true}) {
		t.Fatalf("State = %+v", st)
	}
	if store.Reads() != 1 {
		t.Fatalf("a refused passcode was read again: %d reads", store.Reads())
	}

	if err := restarted.Save("se", []byte("4711")); err != nil {
		t.Fatal(err)
	}
	if got, err := restarted.PIN(ctx, "se"); err != nil || got != "4711" {
		t.Fatalf("PIN after a new Save = %q, %v", got, err)
	}
}

func TestAgentPINRefusalHoldsEvenWhenTheStoreCannotKeepIt(t *testing.T) {
	store := &failingMarkerStore{}
	p, now := newTestPINs(store)
	ctx := context.Background()
	p.Save("se", []byte(sentinelPIN))
	p.PIN(ctx, "se")
	p.Refused("se")
	*now = now.Add(time.Hour)
	if _, err := p.PIN(ctx, "se"); !errors.Is(err, ErrPINRefused) {
		t.Fatalf("PIN = %v, want ErrPINRefused", err)
	}
}

type failingMarkerStore struct{ MemoryPINStore }

func (s *failingMarkerStore) Save(account, value string) error {
	if account == refusedAccount("se") {
		return ErrKeychainLocked
	}
	return s.MemoryPINStore.Save(account, value)
}

func TestAgentPINKeychainErrorsStayDistinct(t *testing.T) {
	for _, want := range []error{ErrKeychainLocked, ErrKeychainDenied} {
		for _, on := range []string{"exists", "read"} {
			store := &MemoryPINStore{}
			p, _ := newTestPINs(store)
			p.Save("se", []byte(sentinelPIN))
			if on == "exists" {
				store.ExistsErr = want
			} else {
				store.ReadErr = want
			}
			_, err := p.PIN(context.Background(), "se")
			if !errors.Is(err, want) || errors.Is(err, ErrNotFound) {
				t.Errorf("%s answering %v: PIN = %v", on, want, err)
			}
		}
	}
}

func TestAgentPINDeleteForgetsTheRefusal(t *testing.T) {
	p, _ := newTestPINs(&MemoryPINStore{})
	p.Save("se", []byte(sentinelPIN))
	p.Refused("se")
	if err := p.Delete("se"); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.State("se"); st != (PINState{}) {
		t.Fatalf("State after Delete = %+v", st)
	}
	p.Save("se", []byte(sentinelPIN))
	if _, err := p.PIN(context.Background(), "se"); err != nil {
		t.Fatalf("PIN after Delete and Save = %v", err)
	}
}

func TestAgentPINServiceIsNotPinkyAppsService(t *testing.T) {
	if AgentService == Service {
		t.Fatal("the daemon's passcodes share Pinky.app's keychain service")
	}
}
