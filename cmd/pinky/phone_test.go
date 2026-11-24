//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
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

func newPhoneEnv(t *testing.T) *phoneEnv {
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
	g, err := gateway.New(gateway.Options{Providers: []driver.Provider{demo.NewProvider(driver.IOS, dev)}, Policy: pol,
		TrustNewAgents: true, SettleMax: 200 * time.Millisecond, PollInterval: 5 * time.Millisecond,
		MaxBlock: 2 * time.Second, DiscoverEvery: 20 * time.Millisecond, LeaseReserve: 3 * time.Second})
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
}
