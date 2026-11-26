//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/policy"
)

const phoneAsCLIEnv = "PINKY_PHONE_TEST_AS_CLI"

// TestMain lets the test binary stand in for pinkyctl inside a phone run:
// the run's `phone` shim executes this binary with the variable set.
func TestMain(m *testing.M) {
	if standInForIconDetector() {
		return
	}
	if os.Getenv(phoneAsCLIEnv) != "" {
		main()
		return
	}
	os.Exit(m.Run())
}

// phoneEnv is an in-process gateway with one demo iPhone, served on the
// socket of a temp PINKY_HOME.
type phoneEnv struct {
	g   *gateway.Gateway
	srv *mcp.Server
	dev *demo.Device
}

func newPhoneEnv(t *testing.T, opts ...func(*gateway.Options)) *phoneEnv {
	t.Helper()
	home := pinkyHome(t)
	t.Setenv(api.HomeEnv, home)
	t.Setenv(phoneAsCLIEnv, "1")
	t.Setenv(phoneHolderEnv, "")
	dirs, err := api.DefaultDirs()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := api.Listen(dirs)
	if err != nil {
		t.Fatal(err)
	}
	dev := demo.NewDevice("se", "iPhone SE", driver.IOS)
	pol := policy.Default()
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	o := gateway.Options{Providers: []driver.Provider{demo.NewProvider(driver.IOS, dev)}, Policy: pol,
		TrustNewAgents: true, SettleMax: 200 * time.Millisecond, PollInterval: 5 * time.Millisecond,
		MaxBlock: 2 * time.Second, DiscoverEvery: 20 * time.Millisecond}
	for _, f := range opts {
		f(&o)
	}
	g, err := gateway.New(o)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := api.EnsureToken(dirs.TokenFile())
	if err != nil {
		t.Fatal(err)
	}
	m := mcp.New(g, "test")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	srv := &api.Server{Gateway: g, MCP: m, Token: tok, Trusted: true, Version: "test"}
	go func() { srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done; g.Close() })
	return &phoneEnv{g: g, srv: m, dev: dev}
}

// phone runs one phone command in-process, as the CLI would.
func phone(args ...string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = phoneMain(args, phoneIO{in: strings.NewReader(""), out: &o, err: &e})
	return code, o.String(), e.String()
}

func (e *phoneEnv) view(t *testing.T) gateway.DeviceView {
	t.Helper()
	devs := e.g.Devices()
	if len(devs) != 1 {
		t.Fatalf("%d phones", len(devs))
	}
	return devs[0]
}

// hold makes an agent named name take the phone for task, as another MCP
// client would, and keep it until the test ends.
func (e *phoneEnv) hold(t *testing.T, name, task string) *sdk.ClientSession {
	t.Helper()
	cs, _, err := dialPhone(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "phone_connect", Arguments: map[string]any{"task": task}})
	if err != nil || res.IsError {
		t.Fatalf("connect: %v %+v", err, res)
	}
	return cs
}

func jsonTypes(t *testing.T, b []byte) map[string]string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	types := map[string]string{}
	for k, v := range m {
		switch v.(type) {
		case nil:
			types[k] = "null"
		case string:
			types[k] = "string"
		case bool:
			types[k] = "bool"
		case float64:
			types[k] = "number"
		default:
			types[k] = "other"
		}
	}
	return types
}

const statusFix = `fix: update docs/mcp.md "From a shell: pinkyctl phone" and the vendored control-local-iphone skill, then the golden`

func TestPhoneStatusKeys(t *testing.T) {
	env := newPhoneEnv(t)
	golden, err := os.ReadFile("testdata/phone-status.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	want := jsonTypes(t, golden)
	check := func(out string) map[string]string {
		got := jsonTypes(t, []byte(out))
		for k, w := range want {
			g, ok := got[k]
			nullable := w == "null" || g == "null"
			if !ok || g != w && !(nullable && (g == "string" || g == "null") && (w == "string" || w == "null")) {
				t.Errorf("key %q is %q (present %v), golden has %q\n%s", k, g, ok, w, statusFix)
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("key %q is not in the golden\n%s", k, statusFix)
			}
		}
		return got
	}
	code, out, errOut := phone("status")
	if code != phoneExitOK || strings.Count(out, "\n") != 1 {
		t.Fatalf("status: code %d, out %q, err %q", code, out, errOut)
	}
	check(out)
	var s phoneStatus
	if err := json.Unmarshal([]byte(out), &s); err != nil || !s.Free || s.Device != "iPhone SE" || s.Health != "idle" {
		t.Fatalf("free phone: %+v %v", s, err)
	}

	env.hold(t, "holder", "hold")
	_, out, _ = phone("status")
	got := check(out)
	if got["holder"] != "string" || got["task"] != "string" {
		t.Fatalf("held phone status: %s", out)
	}
}

func TestPhoneForwardsUnknownTool(t *testing.T) {
	env := newPhoneEnv(t)
	type probeIn struct {
		X int `json:"x"`
	}
	sdk.AddTool(env.srv.SDK(), &sdk.Tool{Name: "phone_probe", Title: "Probe", Description: "A probe added by a test. It is not in any list."},
		func(_ context.Context, _ *sdk.CallToolRequest, in probeIn) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "settled · probe got x=" + string(rune('0'+in.X))}}}, nil, nil
		})
	code, out, errOut := phone("tools")
	if code != phoneExitOK || !strings.Contains(out, "phone_probe · Probe · A probe added by a test.") || !strings.Contains(out, "phone_observe") {
		t.Fatalf("tools: %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut = phone("tools", "--json", "probe")
	if code != phoneExitOK || !strings.Contains(out, `"name": "phone_probe"`) || !strings.Contains(out, `"inputSchema"`) {
		t.Fatalf("tools --json probe: %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut = phone("call", "probe", "x=1")
	if code != phoneExitOK || strings.TrimSpace(out) != "settled · probe got x=1" {
		t.Fatalf("call probe: %d\n%s\n%s", code, out, errOut)
	}
}

func TestPhoneExitCodes(t *testing.T) {
	env := newPhoneEnv(t)
	cases := []struct {
		name string
		do   func() (int, string)
		want int
	}{
		{"settled", func() (int, string) { c, o, e := phone("call", "observe"); return c, o + e }, phoneExitOK},
		{"tool error", func() (int, string) { c, o, e := phone("call", "tap"); return c, o + e }, phoneExitToolError},
		{"usage", func() (int, string) { c, o, e := phone("call", "-nosuchflag", "observe"); return c, o + e }, phoneExitUsage},
		{"unknown tool", func() (int, string) { c, o, e := phone("call", "nosuchtool"); return c, o + e }, phoneExitUsage},
	}
	for _, c := range cases {
		if got, out := c.do(); got != c.want {
			t.Errorf("%s: exit %d, want %d\n%s", c.name, got, c.want, out)
		}
	}
	if _, _, errOut := phone("call", "nosuchtool"); !strings.Contains(errOut, "\nfix: ") {
		t.Errorf("an unknown tool's error has no fix line: %q", errOut)
	}

	if err := env.g.TakeOver("se"); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := phone("call", "observe")
	if code != phoneExitPerson || !strings.HasPrefix(out, "paused") {
		t.Errorf("takeover: exit %d\n%s%s", code, out, errOut)
	}
	if err := env.g.HandBack("se", ""); err != nil {
		t.Fatal(err)
	}

	env.hold(t, "other", "hold")
	start := time.Now()
	code, out, errOut = phone("call", "-wait", "1", "observe")
	if code != phoneExitBusy || !strings.HasPrefix(out, "error: lease_held") || time.Since(start) < 900*time.Millisecond {
		t.Errorf("busy: exit %d after %v\n%s%s", code, time.Since(start), out, errOut)
	}
	if code, _, errOut := phone("run", "-wait", "1", "-e", "phone call observe"); code != phoneExitBusy {
		t.Errorf("run's script saw exit %d, want %d\n%s", code, phoneExitBusy, errOut)
	}
}

func TestPhoneExitNoGateway(t *testing.T) {
	home := pinkyHome(t)
	t.Setenv(api.HomeEnv, home)
	code, out, errOut := phone("call", "observe")
	if code != phoneExitNoGateway || out != "" || !strings.Contains(errOut, "\nfix: ") {
		t.Fatalf("no gateway: exit %d\n%s%s", code, out, errOut)
	}
	if code, _, _ := phone("status"); code != phoneExitNoGateway {
		t.Fatalf("status without a gateway: exit %d", code)
	}
}

func TestPhoneImageToFile(t *testing.T) {
	newPhoneEnv(t)
	t.Setenv("TMPDIR", t.TempDir())
	code, out, errOut := phone("call", "observe", "screenshot=true")
	if code != phoneExitOK {
		t.Fatalf("observe: %d\n%s%s", code, out, errOut)
	}
	var path string
	var size int
	for _, l := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(l, "image: "); ok {
			p, n, _ := strings.Cut(rest, " · ")
			path = p
			size, _ = strconv.Atoi(n)
		}
	}
	if path == "" || !strings.HasPrefix(path, os.Getenv("TMPDIR")) || !strings.HasSuffix(path, ".jpg") {
		t.Fatalf("no image line under $TMPDIR:\n%s", out)
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) != size || !bytes.HasPrefix(b, []byte{0xff, 0xd8}) {
		t.Fatalf("image file: %v, %d bytes of %d, starts % x", err, len(b), size, b[:min(len(b), 4)])
	}
	if len(out) > 4000 {
		t.Fatalf("stdout is %d bytes: the picture must not be inline", len(out))
	}
}

func TestPhoneRunOneLease(t *testing.T) {
	env := newPhoneEnv(t, func(o *gateway.Options) { o.LeaseReserve = 5 * time.Second })
	before := env.view(t).LeaseGen
	script := `phone call observe >/dev/null && phone call observe >/dev/null && phone call observe | head -1`
	code, out, errOut := phone("run", "-as", "ui-audit", "-e", script)
	if code != phoneExitOK || !strings.HasPrefix(out, "settled") || strings.Count(out, "\n") != 1 {
		t.Fatalf("run: exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "[code-mode: ") || !strings.Contains(errOut, " tool output -> ") {
		t.Fatalf("no code-mode line on stderr: %q", errOut)
	}
	v := env.view(t)
	if got := v.LeaseGen - before; got != 2 {
		t.Fatalf("LeaseGen moved by %d over three calls, want 2 (one lease started, one ended)", got)
	}
	if v.Lease.Holder != "" || v.Lease.ReservedFor != "ui-audit" {
		t.Fatalf("after the run: %+v", v.Lease)
	}
	_, out, _ = phone("status", "-as", "ui-audit")
	var s phoneStatus
	if err := json.Unmarshal([]byte(out), &s); err != nil || s.Holder != nil || s.ReservedFor == nil || *s.ReservedFor != "ui-audit" || !s.Free || s.ReservedLeftS < 1 {
		t.Fatalf("status for ui-audit: %s %v", out, err)
	}
	_, out, _ = phone("status")
	if err := json.Unmarshal([]byte(out), &s); err != nil || s.Free {
		t.Fatalf("status for another name must not be free while the phone is kept: %s", out)
	}
}

func TestPhoneRunReleasesOnSignal(t *testing.T) {
	env := newPhoneEnv(t, func(o *gateway.Options) { o.LeaseReserve = 5 * time.Second })
	type result struct {
		code     int
		out, err string
	}
	res := make(chan result, 1)
	go func() {
		c, o, e := phone("run", "-as", "sigrun", "-e", "phone call observe >/dev/null; echo ready; exec sleep 30")
		res <- result{c, o, e}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for env.view(t).Lease.Holder != "sigrun" {
		if time.Now().After(deadline) {
			t.Fatal("the run never took the phone")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-res:
		if r.code != 128+int(syscall.SIGTERM) {
			t.Fatalf("exit %d, want 143\n%s\n%s", r.code, r.out, r.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not end after SIGTERM")
	}
	if l := env.view(t).Lease; l.Holder != "" || l.ReservedFor != "sigrun" {
		t.Fatalf("after SIGTERM the phone must be released and kept for the run's name: %+v", l)
	}
}
