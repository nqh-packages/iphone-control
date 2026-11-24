package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/vault"
)

const canarySentinel = "S3NT1NEL-7f2a"

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type secretRig struct {
	g     *Gateway
	dev   *demo.Device
	v     *vault.Vault
	pol   *policy.Engine
	logs  *lockedBuffer
	dir   string
	s     *Session
	first *Result
}

func newSecretRig(t *testing.T, listed ...string) *secretRig {
	t.Helper()
	return newSecretRigWith(t, nil, listed...)
}

type wrappedProvider struct {
	driver.Provider
	wrap func(driver.Device) driver.Device
}

func (p wrappedProvider) Open(ctx context.Context, id string) (driver.Device, error) {
	d, err := p.Provider.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	return p.wrap(d), nil
}

func newSecretRigWith(t *testing.T, wrap func(driver.Device) driver.Device, listed ...string) *secretRig {
	t.Helper()
	f := policy.DefaultFile()
	f.CredentialApps = listed
	pol := policy.New(f, nil)
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	v := vault.New(pol.SecretApp)
	logs := &lockedBuffer{}
	dir := t.TempDir()
	g, dev := newTestGateway(t, func(o *Options) {
		o.Policy, o.Secrets, o.DataDir, o.NoScreenshots = pol, v, dir, false
		if wrap != nil {
			o.Providers = []driver.Provider{&wrappedProvider{o.Providers[0], wrap}}
		}
		o.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	dev.Show("account")
	s, first := connect(t, g, "Sign in to the test account")
	return &secretRig{g: g, dev: dev, v: v, pol: pol, logs: logs, dir: dir, s: s, first: first}
}

func (r *secretRig) stage(t *testing.T, app, name, value string) {
	t.Helper()
	if err := r.v.Deposit(app, 0, map[string][]byte{name: []byte(value)}); err != nil {
		t.Fatal(err)
	}
}

func (r *secretRig) typeRef(t *testing.T, label, text string, submit bool) *Result {
	t.Helper()
	obs := r.s.Observe(context.Background(), ObserveRequest{})
	return r.s.Act(context.Background(), Call{}, Action{Tool: "type", Ref: ref(t, obs, label), Text: text, Submit: submit})
}

func typedByDevice(dev *demo.Device, text string) int {
	n := 0
	for _, in := range dev.Inputs() {
		if in.Kind == "type" && in.Text == text {
			n++
		}
	}
	return n
}

func resultText(res *Result) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(res.Message + "\n" + res.Note + "\n" + res.Log + "\n" + strings.Join(res.Diff, "\n") + "\n")
	if res.Screen != nil {
		b.WriteString(res.Screen.Render() + "\n")
		for _, e := range res.Screen.Elements {
			b.WriteString(e.Label + "\n" + e.Content + "\n")
		}
	}
	for _, st := range res.Steps {
		b.WriteString(resultText(st))
	}
	return b.String()
}

func nonImageFiles(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || strings.HasSuffix(p, ".jpg") || strings.HasSuffix(p, ".png") {
			return nil
		}
		data, _ := os.ReadFile(p)
		b.Write(data)
		return nil
	})
	return b.String()
}

func coveredAt(img image.Image, r screen.Rect, screenWidth int) bool {
	if img == nil {
		return false
	}
	c, scale := r.Center(), float64(img.Bounds().Dx())/float64(screenWidth)
	return covered(img.At(int(float64(c.X)*scale), int(float64(c.Y)*scale)))
}

// canary types a sentinel secret into a password field, a code field and a
// batch step on the demo phone, and returns every reader that shows it:
// the agent's results and screens, the history rows and files, the export,
// the log, the approvals, and the screenshot where the typed code sits (by
// the fill of its rectangle, since bytes of a picture prove nothing).
func canary(t *testing.T) []string {
	t.Helper()
	r := newSecretRig(t, demo.Dinely)
	r.dev.OnInput(func(in demo.Input) {
		if in.Kind == "type" {
			r.dev.Notify(demo.Dinely, "Sign-in attempt "+in.Text)
		}
	})
	ctx := context.Background()
	for _, n := range []string{"pw", "code", "batch"} {
		r.stage(t, demo.Dinely, n, canarySentinel)
	}
	res := []*Result{r.typeRef(t, "Password", vault.Ref("pw"), false)}
	r.g.Flush()
	res = append(res, r.typeRef(t, "Verification code", vault.Ref("code"), false))
	r.g.Flush()
	obs := r.s.Observe(ctx, ObserveRequest{})
	res = append(res, obs, r.s.Batch(ctx, BatchRequest{Steps: []Action{{Tool: "type", Ref: ref(t, obs, "Password"), Text: vault.Ref("batch")}}}))
	for i, x := range res[:2] {
		if x.IsError() || x.Status != StatusSettled {
			t.Fatalf("type %d did not run: %+v", i, x)
		}
	}
	if n := typedByDevice(r.dev, canarySentinel); n != 3 {
		t.Fatalf("the phone received the secret %d times, want 3", n)
	}
	shot := r.s.Observe(ctx, ObserveRequest{Screenshot: true})
	res = append(res, shot)

	var leaks []string
	check := func(reader, text string) {
		if strings.Contains(text, canarySentinel) {
			leaks = append(leaks, reader)
		}
	}
	for i, x := range res {
		check(fmt.Sprintf("agent result %d", i), resultText(x))
	}
	r.g.Flush()
	tasks, err := r.g.History(ctx, HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := json.Marshal(tasks)
	check("history rows", string(rows))
	check("history files", nonImageFiles(t, r.dir))
	needs, _ := json.Marshal(r.g.Needs())
	check("approvals", string(needs))
	check("log output", r.logs.String())
	out := t.TempDir()
	if _, err := r.g.ExportHistory(ctx, nil, out); err != nil {
		t.Fatal(err)
	}
	check("export", nonImageFiles(t, out))

	var otp *screen.Element
	for i, e := range shot.Screen.Elements {
		if e.Masked && !e.Bounds.Empty() {
			otp = &shot.Screen.Elements[i]
		}
	}
	if otp == nil {
		leaks = append(leaks, "screenshot: the typed code field is not marked Masked, so nothing covers it")
	} else if shot.Screenshot == nil || !coveredAt(shot.Screenshot, otp.Bounds, shot.Screen.Width) {
		leaks = append(leaks, "screenshot: the typed code field is not covered")
	}
	if otp != nil {
		leaks = append(leaks, historyShotLeaks(t, r.g, tasks, otp.Bounds, shot.Screen.Width)...)
	}
	return leaks
}

func historyShotLeaks(t *testing.T, g *Gateway, tasks []TaskView, field screen.Rect, screenWidth int) []string {
	t.Helper()
	for _, task := range tasks {
		for _, st := range task.Steps {
			if st.Tool != "phone_type" || !strings.Contains(st.Args, vault.Ref("code")) || st.After == "" {
				continue
			}
			p, err := g.MediaPath(context.Background(), st.After)
			if err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			img, _, err := image.Decode(f)
			if err != nil {
				t.Fatal(err)
			}
			if !coveredAt(img, field, screenWidth) {
				return []string{"history screenshot: the typed code field is not covered"}
			}
			return nil
		}
	}
	return []string{"history: no screenshot of the typed code step"}
}

// TestSecretCanary: a sentinel typed by reference reaches the phone and no
// reader shows it. fix: each name below is the reader that leaked.
func TestSecretCanary(t *testing.T) {
	if leaks := canary(t); len(leaks) > 0 {
		t.Fatalf("the secret leaked through: %s", strings.Join(leaks, "; "))
	}
}

// TestSecretCanaryFailsWithoutMasking proves the canary can fail: with the
// masking of reads switched off it must find the sentinel.
func TestSecretCanaryFailsWithoutMasking(t *testing.T) {
	was := maskRead
	maskRead = func(*vault.Vault, *screen.Screen) {}
	t.Cleanup(func() { maskRead = was })
	leaks := strings.Join(canary(t), "; ")
	for _, want := range []string{"agent result", "screenshot", "history"} {
		if !strings.Contains(leaks, want) {
			t.Errorf("the canary did not notice a leak into %q (found: %q)", want, leaks)
		}
	}
}

// TestSecretRefusalsKeepTheSecret: every refusal happens before the secret
// is taken, so a refused step leaves it staged, and a used one is gone.
func TestSecretRefusalsKeepTheSecret(t *testing.T) {
	r := newSecretRig(t, demo.Dinely)
	r.stage(t, demo.Dinely, "pw", canarySentinel)
	blocked := func(res *Result, rule, why string) {
		t.Helper()
		if res.Status != StatusBlocked || res.Rule != rule || !strings.Contains(res.Message, why) || strings.Contains(resultText(res), canarySentinel) {
			t.Fatalf("want blocked by %s with %q: %+v", rule, why, res)
		}
	}

	blocked(r.typeRef(t, "Email", vault.Ref("pw"), false), policy.RuleCredentials, "password or one-time-code field")
	blocked(r.typeRef(t, "Password", "hunter2", false), policy.RuleCredentials, "never types into password")

	r.pol.SetHideCodes(false)
	blocked(r.typeRef(t, "Password", vault.Ref("pw"), false), policy.RuleCredentials, "Hide codes from agents")
	r.pol.SetHideCodes(true)

	if res := r.typeRef(t, "Password", "user "+vault.Ref("pw"), false); res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "whole text") {
		t.Fatalf("mixed text: %+v", res)
	}

	r.dev.Show("chat")
	blocked(r.typeRef(t, "Message", vault.Ref("pw"), true), policy.RuleCredentials, "password or one-time-code field")
	if len(r.dev.Inputs()) != 0 {
		t.Fatalf("something reached the phone: %+v", r.dev.Inputs())
	}
	if err := r.v.Staged("pw", screen.App{ID: demo.Dinely}); err != nil {
		t.Fatalf("a refused step used the secret up: %v", err)
	}

	r.dev.Show("account")
	res := r.typeRef(t, "Password", vault.Ref("pw"), false)
	if res.IsError() || res.Status != StatusSettled || typedByDevice(r.dev, canarySentinel) != 1 {
		t.Fatalf("the accepted step: %+v inputs %+v", res, r.dev.Inputs())
	}
	again := r.typeRef(t, "Password", vault.Ref("pw"), false)
	if again.Error != ErrNotFound || !strings.Contains(again.Message, "fix: stage it again") || typedByDevice(r.dev, canarySentinel) != 1 {
		t.Fatalf("a second use: %+v", again)
	}
}

// TestSecretStagedForAnotherApp: a secret staged for one listed app is not
// typed into another, and stays staged for its own.
func TestSecretStagedForAnotherApp(t *testing.T) {
	r := newSecretRig(t, demo.Dinely, demo.Chatter)
	r.stage(t, demo.Chatter, "pw", canarySentinel)
	res := r.typeRef(t, "Password", vault.Ref("pw"), false)
	if res.Error != ErrNotFound || strings.Contains(res.Message, demo.Chatter) || !strings.Contains(res.Message, "fix: stage it again") || typedByDevice(r.dev, canarySentinel) != 0 {
		t.Fatalf("typed into the wrong app, or told which app the name was staged for: %+v", res)
	}
	if err := r.v.Staged("pw", screen.App{ID: demo.Chatter}); err != nil {
		t.Fatalf("the secret was used up: %v", err)
	}
}

// TestSecretNotListed: an app the owner did not list refuses a reference
// with a reason that names credential_apps, and the secret cannot even be
// staged for it.
func TestSecretNotListed(t *testing.T) {
	r := newSecretRig(t)
	if err := r.v.Deposit(demo.Dinely, 0, map[string][]byte{"pw": []byte(canarySentinel)}); err == nil {
		t.Fatal("a secret was staged for an app that is not listed")
	}
	res := r.typeRef(t, "Password", vault.Ref("pw"), false)
	if res.Status != StatusBlocked || !strings.Contains(res.Message, "credential_apps") {
		t.Fatalf("%+v", res)
	}
}

// TestSecretExpired: a staged secret that outlived its lifetime is not
// typed, and the agent is told how to stage it again.
func TestSecretExpired(t *testing.T) {
	r := newSecretRig(t, demo.Dinely)
	if err := r.v.Deposit(demo.Dinely, time.Millisecond, map[string][]byte{"pw": []byte(canarySentinel)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	res := r.typeRef(t, "Password", vault.Ref("pw"), false)
	if res.Error != ErrNotFound || !strings.Contains(res.Message, "fix: stage it again") || typedByDevice(r.dev, canarySentinel) != 0 {
		t.Fatalf("%+v", res)
	}
}

// TestSecretNoStore: a gateway with no vault refuses every reference.
func TestSecretNoStore(t *testing.T) {
	f := policy.DefaultFile()
	f.CredentialApps = []string{demo.Dinely}
	g, dev := newTestGateway(t, func(o *Options) { o.Policy = policy.New(f, nil) })
	dev.Show("account")
	s, _ := connect(t, g, "Sign in")
	obs := s.Observe(context.Background(), ObserveRequest{})
	res := s.Act(context.Background(), Call{}, Action{Tool: "type", Ref: ref(t, obs, "Password"), Text: vault.Ref("pw")})
	if res.Status != StatusBlocked || !strings.Contains(res.Message, "no secret store") || len(dev.Inputs()) != 0 {
		t.Fatalf("%+v", res)
	}
}

// TestSecretApproval: an action that asks shows the approval card the
// reference only, and types the value once the person allows it.
func TestSecretApproval(t *testing.T) {
	r := newSecretRig(t, demo.Dinely)
	if err := r.pol.SetPreset(policy.PresetEvery); err != nil {
		t.Fatal(err)
	}
	r.stage(t, demo.Dinely, "pw", canarySentinel)
	done := make(chan *Result, 1)
	obs := r.s.Observe(context.Background(), ObserveRequest{})
	go func() {
		done <- r.s.Act(context.Background(), Call{}, Action{Tool: "type", Ref: ref(t, obs, "Password"), Text: vault.Ref("pw")})
	}()
	n := waitNeed(t, r.g)
	card, _ := json.Marshal(struct{ Title, Detail, Rule string }{n.Title, n.Detail, n.Rule})
	if strings.Contains(string(card), canarySentinel) || n.Rule != policy.RuleEvery {
		t.Fatalf("approval card: %s", card)
	}
	if err := r.g.Decide(n.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if res.IsError() || strings.Contains(resultText(res), canarySentinel) || typedByDevice(r.dev, canarySentinel) != 1 {
		t.Fatalf("%+v inputs %+v", res, r.dev.Inputs())
	}
}

// TestSecretIsTakenBeforeTheFocusingTap: a secret that cannot be taken at
// the last moment (it expired, or was used) fails before anything reaches
// the phone, the focusing tap included.
func TestSecretIsTakenBeforeTheFocusingTap(t *testing.T) {
	g, dev := newTestGateway(t, func(o *Options) {
		f := policy.DefaultFile()
		f.CredentialApps = []string{demo.Dinely}
		pol := policy.New(f, nil)
		o.Policy, o.Secrets = pol, vault.New(pol.SecretApp)
	})
	field := screen.Element{Role: screen.RoleInput, Label: "Password", States: []string{screen.StatePassword}}
	p := &prep{hasPt: true, point: screen.Point{X: 100, Y: 100}, target: &field}
	p.input.Secret, p.input.App = true, screen.App{ID: demo.Dinely}
	err := g.perform(context.Background(), dev, Action{Tool: "type", Text: vault.Ref("pw")}, p)
	if !errors.Is(err, driver.ErrNotFound) || len(dev.Inputs()) != 0 {
		t.Fatalf("err %v, the phone got %+v", err, dev.Inputs())
	}
	if strings.Contains(err.Error(), canarySentinel) {
		t.Fatalf("the error holds a value: %v", err)
	}
}

type failingTyper struct {
	driver.Device
	err error
}

func (f failingTyper) Type(context.Context, string, driver.TypeOptions) error { return f.err }

// TestSecretDriverErrorNeverTravels: a driver error that quotes a character
// of the value or its length reaches no reader; the agent hears one fixed
// sentence, and the class of the error still decides the result.
func TestSecretDriverErrorNeverTravels(t *testing.T) {
	const leakA, leakB = "'ä' at offset 3", "(11 characters where 14 were expected)"
	cause := fmt.Errorf("type: after typing by keys the field reads otherwise than the text %s: first non-ASCII %s: %s", leakB, leakA, "the keyboard did not answer")
	r := newSecretRigWith(t, func(d driver.Device) driver.Device { return failingTyper{d, cause} }, demo.Dinely)
	r.stage(t, demo.Dinely, "pw", "pässword-"+canarySentinel)
	res := r.typeRef(t, "Password", vault.Ref("pw"), false)
	r.g.Flush()
	tasks, err := r.g.History(context.Background(), HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := json.Marshal(tasks)
	seen := resultText(res) + string(rows) + nonImageFiles(t, r.dir) + r.logs.String()
	for _, leak := range []string{"ä", "characters where", "offset", "reads otherwise"} {
		if strings.Contains(seen, leak) {
			t.Errorf("the driver's error text %q travelled: %s", leak, seen)
		}
	}
	if !strings.Contains(res.Message, secretTypeFailed) || res.Status != StatusUncertain {
		t.Fatalf("want the fixed sentence and an uncertain result: %+v", res)
	}
}
