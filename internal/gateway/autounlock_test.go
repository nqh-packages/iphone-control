package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/secret"
)

const unlockSentinel = "S3NT1NEL-7f2a"

// unlockDev is a demo phone that unlocks with one PIN, counts every PIN
// entered and can be told to stop before entering it.
type unlockDev struct {
	*demo.Device

	mu      sync.Mutex
	pin     string
	tries   int
	failure error
	slow    time.Duration
}

func (x *unlockDev) Unlock(_ context.Context, pin string) error {
	x.mu.Lock()
	x.tries++
	right, failure, slow := pin == x.pin, x.failure, x.slow
	x.mu.Unlock()
	time.Sleep(slow)
	if failure != nil {
		return failure
	}
	if !right {
		return fmt.Errorf("still locked: %w", driver.ErrLocked)
	}
	x.SetLocked(false)
	return nil
}

func (x *unlockDev) attempts() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.tries
}

type unlockProvider struct {
	*demo.Provider
	x *unlockDev
}

func (p unlockProvider) Open(context.Context, string) (driver.Device, error) { return p.x, nil }

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// pinOutcomeOf is the daemon's reading of how an entry ended (cmd/pinky).
func pinOutcomeOf(p *secret.AgentPINs) func(string, error) {
	return func(id string, err error) {
		switch {
		case err == nil:
			p.Entered(id)
		case errors.Is(err, driver.ErrLocked):
			p.Refused(id)
		}
	}
}

type unlockRig struct {
	t     *testing.T
	x     *unlockDev
	g     *Gateway
	store *secret.MemoryPINStore
	pins  *secret.AgentPINs
	logs  *syncBuf
	dir   string
	opts  []func(*Options)
}

// newUnlockRig runs a gateway with AutoUnlock on over a locked phone whose
// PIN is unlockSentinel, with that PIN saved in a memory store.
func newUnlockRig(t *testing.T, opts ...func(*Options)) *unlockRig {
	t.Helper()
	x := &unlockDev{Device: demo.NewDevice("pixel", "Pixel 9", driver.Android), pin: unlockSentinel}
	r := &unlockRig{t: t, x: x, store: &secret.MemoryPINStore{}, logs: &syncBuf{}, dir: t.TempDir(), opts: opts}
	if err := secret.NewAgentPINsOn(r.store).Save("pixel", []byte(unlockSentinel)); err != nil {
		t.Fatal(err)
	}
	r.start()
	x.SetLocked(true)
	return r
}

func (r *unlockRig) start() {
	r.t.Helper()
	r.pins = secret.NewAgentPINsOn(r.store)
	o := Options{
		Providers: []driver.Provider{unlockProvider{demo.NewProvider(driver.Android, r.x.Device), r.x}},
		Policy:    testPolicy(r.t), TrustNewAgents: true, DataDir: r.dir, NoScreenshots: true,
		ApprovalTimeout: 3 * time.Second, MaxBlock: 2 * time.Second, SettleMax: 200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, DiscoverEvery: time.Hour,
		AutoUnlock: true, PIN: r.pins.PIN, PINOutcome: pinOutcomeOf(r.pins),
		Logger: slog.New(slog.NewTextHandler(io.MultiWriter(r.logs), &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, f := range r.opts {
		f(&o)
	}
	g, err := New(o)
	if err != nil {
		r.t.Fatal(err)
	}
	r.g = g
	r.t.Cleanup(func() { g.Close() })
}

func (r *unlockRig) restart() {
	r.g.Close()
	r.start()
}

func (r *unlockRig) connect(task string) (*Session, *Result) {
	r.t.Helper()
	s := r.g.OpenSession(agent)
	r.t.Cleanup(s.Close)
	return s, s.Connect(context.Background(), ConnectRequest{Task: task})
}

func (r *unlockRig) cacheLocked() {
	r.g.mu.Lock()
	d := r.g.devices["pixel"]
	r.g.mu.Unlock()
	r.g.setLocked(d, true)
}

func (r *unlockRig) seenLocked() bool { return cachedLocked(r.g) }

func TestAutoUnlockOffLeavesTheLockedAnswerAsItWas(t *testing.T) {
	plain := newUnlockRig(t, func(o *Options) { o.AutoUnlock, o.PIN, o.PINOutcome = false, nil, nil })
	_, base := plain.connect("Lunch")
	withPIN := newUnlockRig(t, func(o *Options) { o.AutoUnlock = false })
	_, res := withPIN.connect("Lunch")
	if res.Status != StatusLocked || res.Message != base.Message {
		t.Fatalf("with AutoUnlock off the answer changed:\n%q\n%q", res.Message, base.Message)
	}
	if plain.x.attempts()+withPIN.x.attempts() != 0 {
		t.Fatal("a PIN was entered with AutoUnlock off")
	}
}

func TestAutoUnlockConnectOnALockedPhone(t *testing.T) {
	r := newUnlockRig(t)
	_, res := r.connect("Lunch")
	mustStatus(t, res, StatusSettled)
	if r.x.attempts() != 1 {
		t.Fatalf("PINs entered: %d", r.x.attempts())
	}
	var steps []string
	for _, tk := range r.g.Tasks() {
		for _, st := range tk.Steps {
			steps = append(steps, st.Text)
		}
	}
	if !strings.Contains(strings.Join(steps, "|"), "Unlocked from the computer") {
		t.Fatalf("history steps: %q", steps)
	}
}

func TestAutoUnlockObserveAfterTheScreenLockedAgain(t *testing.T) {
	r := newUnlockRig(t)
	r.x.SetLocked(false)
	s, res := r.connect("Lunch")
	mustStatus(t, res, StatusSettled)
	r.x.SetLocked(true)
	r.g.auto = autoUnlocker{}
	mustStatus(t, s.Observe(context.Background(), ObserveRequest{}), StatusSettled)
	if r.x.attempts() != 1 {
		t.Fatalf("PINs entered: %d", r.x.attempts())
	}
}

func TestAutoUnlockActOnAPhoneTheGatewayKnowsIsLocked(t *testing.T) {
	r := newUnlockRig(t)
	r.x.SetLocked(false)
	s, first := r.connect("Lunch")
	dinely := ref(t, first, "Dinely")
	r.x.SetLocked(true)
	r.cacheLocked()

	done := make(chan *Result, 1)
	go func() { done <- tap(s, dinely) }()
	select {
	case res := <-done:
		if res.IsError() || res.Status == StatusLocked {
			t.Fatalf("the action did not run: %s %q", res.Status, res.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call hung: autoUnlock ran while the device's action lock was held")
	}
	if r.x.attempts() != 1 || r.seenLocked() {
		t.Fatalf("PINs entered %d, gateway thinks locked: %v", r.x.attempts(), r.seenLocked())
	}
}

func TestAutoUnlockWrongPINIsEnteredOnceEver(t *testing.T) {
	r := newUnlockRig(t)
	r.x.pin = "4711"
	s, res := r.connect("Lunch")
	mustStatus(t, res, StatusLocked)
	if !strings.Contains(res.Message, "fix:") || !strings.Contains(res.Message, "pinkyctl phone pin set -device pixel") {
		t.Fatalf("no fix line: %q", res.Message)
	}
	for i := 0; i < 3; i++ {
		res := s.Observe(context.Background(), ObserveRequest{})
		if !strings.Contains(res.Message, "pinkyctl phone pin set -device pixel") {
			t.Fatalf("call %d lost the fix line: %q", i, res.Message)
		}
		tap(s, 1)
	}
	if r.x.attempts() != 1 {
		t.Fatalf("PINs entered: %d, want 1", r.x.attempts())
	}

	r.restart()
	s2, res := r.connect("Again")
	mustStatus(t, res, StatusLocked)
	s2.Observe(context.Background(), ObserveRequest{})
	if r.x.attempts() != 1 {
		t.Fatalf("PINs entered after a restart: %d, want 1", r.x.attempts())
	}

	if err := r.pins.Save("pixel", []byte("4711")); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, s2.Observe(context.Background(), ObserveRequest{}), StatusSettled)
	if r.x.attempts() != 2 {
		t.Fatalf("PINs entered after a new Save: %d, want 2", r.x.attempts())
	}
}

func TestAutoUnlockPINNotEnteredIsNotTriedAgainAtOnce(t *testing.T) {
	r := newUnlockRig(t)
	r.x.failure = fmt.Errorf("no pad: %w", driver.ErrPINNotEntered)
	s, res := r.connect("Lunch")
	mustStatus(t, res, StatusLocked)
	if !strings.Contains(res.Message, "fix: call again in a minute") {
		t.Fatalf("message: %q", res.Message)
	}
	s.Observe(context.Background(), ObserveRequest{})
	tap(s, 1)
	if r.x.attempts() != 1 {
		t.Fatalf("PINs entered: %d, want 1", r.x.attempts())
	}
	if st, _ := r.pins.State("pixel"); st.Refused {
		t.Fatal("a PIN that was never entered was marked refused")
	}
}

func TestAutoUnlockWithNoSavedPINAnswersLockedAtOnce(t *testing.T) {
	r := newUnlockRig(t)
	r.pins.Delete("pixel")
	start := time.Now()
	_, res := r.connect("Lunch")
	mustStatus(t, res, StatusLocked)
	if time.Since(start) > time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if !strings.Contains(res.Message, "no passcode is saved for Pixel 9") || !strings.Contains(res.Message, "fix: the owner runs `pinkyctl phone pin set -device pixel`") {
		t.Fatalf("message: %q", res.Message)
	}
	if r.x.attempts() != 0 {
		t.Fatal("a PIN was entered")
	}
}

func TestAutoUnlockKeychainErrorsAreToldApart(t *testing.T) {
	notes := map[string]bool{}
	for _, e := range []error{secret.ErrKeychainLocked, secret.ErrKeychainDenied} {
		r := newUnlockRig(t)
		r.store.ReadErr = e
		_, res := r.connect("Lunch")
		mustStatus(t, res, StatusLocked)
		if r.x.attempts() != 0 || strings.Contains(res.Message, "no passcode is saved") {
			t.Fatalf("%v: attempts %d, message %q", e, r.x.attempts(), res.Message)
		}
		notes[res.Message] = true
	}
	if len(notes) != 2 {
		t.Fatal("a locked keychain and a denied one read the same")
	}
}

func TestAutoUnlockLeavesThePersonsPhoneAlone(t *testing.T) {
	r := newUnlockRig(t)
	r.x.SetLocked(false)
	s, _ := r.connect("Lunch")
	r.x.SetLocked(true)
	r.cacheLocked()
	if err := r.g.TakeOver("pixel"); err != nil {
		t.Fatal(err)
	}
	s.Observe(context.Background(), ObserveRequest{})
	if r.x.attempts() != 0 {
		t.Fatalf("a PIN was entered while the person held the phone: %d", r.x.attempts())
	}
}

func TestAutoUnlockWaitsAMinuteAfterThePersonTouchedThePhone(t *testing.T) {
	r := newUnlockRig(t)
	if err := r.g.PersonTouched("pixel"); err != nil {
		t.Fatal(err)
	}
	r.g.mu.Lock()
	r.g.devices["pixel"].personAt = time.Now()
	r.g.mu.Unlock()
	r.connect("Lunch")
	if r.x.attempts() != 0 {
		t.Fatalf("a PIN was entered right after the person's touch: %d", r.x.attempts())
	}
}

func TestAutoUnlockNotForAnAgentThatMayOnlyWatch(t *testing.T) {
	r := newUnlockRig(t)
	r.g.SetAgentScope(agent.Name, Scope{Mode: ScopeWatchOnly})
	_, res := r.connect("Watch")
	if res.Status == StatusSettled || r.x.attempts() != 0 {
		t.Fatalf("status %s, PINs entered %d", res.Status, r.x.attempts())
	}
}

func TestAutoUnlockWithTooLittleTimeLeftDoesNotTry(t *testing.T) {
	r := newUnlockRig(t, func(o *Options) { o.CallBudget = unlockEstimate })
	_, res := r.connect("Lunch")
	mustStatus(t, res, StatusLocked)
	if r.x.attempts() != 0 || !strings.Contains(res.Message, "next call") {
		t.Fatalf("attempts %d, message %q", r.x.attempts(), res.Message)
	}
}

func TestAutoUnlockTwoCallsAtOnceEnterTheOnePIN(t *testing.T) {
	r := newUnlockRig(t)
	r.x.slow = 150 * time.Millisecond
	s, _ := r.connect("Lunch")
	results := make([]*Result, 3)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.Observe(context.Background(), ObserveRequest{})
		}()
	}
	wg.Wait()
	for i, res := range results {
		if res.IsError() || res.Status != StatusSettled {
			t.Errorf("call %d: %s %q", i, res.Status, res.Message)
		}
	}
	if r.x.attempts() != 1 {
		t.Fatalf("PINs entered by overlapping calls: %d, want 1", r.x.attempts())
	}
}

func TestAutoUnlockAfterAnUnlockThatNeededNoPINTheNextOneIsNotHeldBack(t *testing.T) {
	r := newUnlockRig(t)
	r.x.SetLocked(false)
	if err := r.g.Unlock("pixel"); err != nil {
		t.Fatal(err)
	}
	r.x.SetLocked(true)
	_, res := r.connect("Lunch")
	mustStatus(t, res, StatusSettled)
	if r.x.attempts() != 1 {
		t.Fatalf("PINs entered: %d", r.x.attempts())
	}
}

func TestAutoUnlockPINReachesNoLogStepResultOrError(t *testing.T) {
	r := newUnlockRig(t)
	var texts []string
	record := func(res *Result) {
		b, _ := json.Marshal(res)
		texts = append(texts, string(b), res.Message)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := r.g.Subscribe(ctx)

	s, res := r.connect("Lunch")
	record(res)
	r.x.SetLocked(true)
	r.cacheLocked()
	record(tap(s, ref(t, res, "Dinely")))
	r.x.pin = "4711"
	r.x.SetLocked(true)
	r.pins.Save("pixel", []byte(unlockSentinel))
	r.cacheLocked()
	record(s.Observe(context.Background(), ObserveRequest{}))
	record(s.Observe(context.Background(), ObserveRequest{}))
	r.x.pin = unlockSentinel
	r.x.failure = fmt.Errorf("no pad: %w", driver.ErrPINNotEntered)
	r.pins.Save("pixel", []byte(unlockSentinel))
	record(s.Observe(context.Background(), ObserveRequest{}))
	r.store.ReadErr = secret.ErrKeychainDenied
	record(s.Observe(context.Background(), ObserveRequest{}))
	if err := r.g.Unlock("pixel"); err != nil {
		texts = append(texts, err.Error())
	}
	cancel()
	for e := range events {
		b, _ := json.Marshal(e)
		texts = append(texts, string(b))
	}
	tasks, _ := json.Marshal(r.g.Tasks())
	hist, _ := r.g.History(context.Background(), HistoryQuery{})
	h, _ := json.Marshal(hist)
	needs, _ := json.Marshal(r.g.Needs())
	texts = append(texts, r.logs.String(), string(tasks), string(h), string(needs))
	for i, text := range texts {
		if strings.Contains(text, unlockSentinel) {
			t.Fatalf("the PIN reached surface %d: %.300s", i, text)
		}
	}
	if !strings.Contains(r.logs.String(), "auto unlock") {
		t.Fatal("the canary read no log of an unlock: the check proves nothing")
	}
}
