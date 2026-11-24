package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/gateway"
)

// pinkyctl phone drives the phone from a shell through Pinky's own phone_*
// MCP tools: status reads who has the phone, tools lists the live tool
// catalog, call makes one tool call, and run holds one MCP session and one
// lease for a whole shell script. Nothing here lists a tool: the catalog is
// the gateway's tools/list, so a tool added to the server works at once. The
// CLI never starts a daemon.

// Exit codes of pinkyctl phone, one table for call and for its own failures.
// run returns the script's own status.
const (
	phoneExitOK        = 0 // the tool answered with a status
	phoneExitToolError = 1 // the tool answered an error
	phoneExitUsage     = 2
	phoneExitNoGateway = 3
	phoneExitBusy      = 4 // lease_held after -wait ran out
	phoneExitPerson    = 5 // the person has the phone or must act
)

// phoneHolderDefault is the agent name phone commands show in Pinky without
// -as or IPHONE_LEASE_HOLDER.
const phoneHolderDefault = "Pinky CLI"

// phoneRunEnv names the unix socket of the phone run a script is part of:
// phone call inside the script goes through that run's session.
const phoneRunEnv = "PINKY_PHONE_RUN"

// phoneHolderEnv names the agent, when -as is not given.
const phoneHolderEnv = "IPHONE_LEASE_HOLDER"

// errPhoneNoGateway is a missing gateway socket: exit 3.
var errPhoneNoGateway = errors.New("no gateway")

// phoneIO is where a phone command reads and writes.
type phoneIO struct {
	in       io.Reader
	out, err io.Writer
}

// runPhone is `pinkyctl phone status|tools|call|run`.
func runPhone(args []string) error {
	if code := phoneMain(args, phoneIO{in: os.Stdin, out: os.Stdout, err: os.Stderr}); code != phoneExitOK {
		return &exitError{code: code}
	}
	return nil
}

func phoneUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pinkyctl phone status [-device D] [-as NAME]
  pinkyctl phone tools [--json] [name]
  pinkyctl phone call [-as NAME] [-wait S] [-task T] [-device D] <tool> [json | k=v ... | -]
  pinkyctl phone run [-wait S] [-as NAME] [-task T] [-device D] (<file> | -e '<sh>' | -)
  pinkyctl phone pin (set | check | delete) [-device D]
  pinkyctl phone secret put -app BUNDLE_ID [-ttl D] NAME...
  pinkyctl phone login (set | check | delete) -name NAME
  pinkyctl phone fill (-account NAME | -login ID) [-ref N] [-submit] [-app BUNDLE_ID]

status prints one JSON line: free, device, holder, task, reserved_for,
reserved_left_s, waiting, link, health. tools lists the gateway's phone_*
tools as it serves them now. call makes one tool call (the tool name with or
without phone_): it takes the phone, calls, and releases. run holds one session
and one lease for the script's whole life; phone call inside it goes through
that session.

pin saves the passcode the daemon enters when a call finds the phone locked
(PINKY_AUTO_UNLOCK=1); run pinkyctl phone pin help.

fill types a saved login's password into a field through the credential broker;
run pinkyctl phone fill -h.

secret put stages a secret from the credential broker for one app, for
phone_type's {{secret:NAME}}; run pinkyctl phone secret help.

The agent is named -as, else $IPHONE_LEASE_HOLDER, else "Pinky CLI". -wait is
how many seconds to queue for the phone (default 60).

Exit status: 0 the tool answered with a status; 1 it answered an error; 2
usage; 3 no gateway; 4 the phone stayed busy for -wait; 5 the person has the
phone or must act (user_took_over, paused, stopped, device_locked,
device_needs_setup, device_paused). run returns the script's status.
`)
}

// phoneMain runs one phone command and returns its exit code.
func phoneMain(args []string, pio phoneIO) int {
	if len(args) == 0 {
		phoneUsage(pio.err)
		return phoneExitUsage
	}
	switch args[0] {
	case "status":
		return phoneStatusCmd(args[1:], pio)
	case "tools":
		return phoneToolsCmd(args[1:], pio)
	case "call":
		return phoneCallCmd(args[1:], pio)
	case "run":
		return phoneRunCmd(args[1:], pio)
	case "pin":
		return phonePinCmd(args[1:], pio)
	case "login":
		return phoneLoginCmd(args[1:], pio)
	case "fill":
		return phoneFillCmd(args[1:], pio)
	case "secret":
		return phoneSecretCmd(args[1:], pio)
	case "help", "-h", "--help", "-help":
		phoneUsage(pio.out)
		return phoneExitOK
	}
	return phoneFail(pio, phoneExitUsage, fmt.Sprintf("unknown command %q", args[0]), "run `pinkyctl phone help`")
}

// phoneFail prints the CLI's own error in plain English with its fix line.
func phoneFail(pio phoneIO, code int, msg, fix string) int {
	fmt.Fprintf(pio.err, "phone: %s\nfix: %s\n", msg, fix)
	return code
}

// phoneNoGateway is the exit 3 failure.
func phoneNoGateway(pio phoneIO, dirs api.Dirs) int {
	return phoneFail(pio, phoneExitNoGateway, "no gateway at "+dirs.Socket(),
		"pinky daemon --http 127.0.0.1:7465 (with the same PINKY_HOME)")
}

// phoneFlags is a flag set that reports into pio.
func phoneFlags(name string, pio phoneIO) *flag.FlagSet {
	fs := flag.NewFlagSet("pinkyctl phone "+name, flag.ContinueOnError)
	fs.SetOutput(pio.err)
	return fs
}

// phoneParse parses flags: ok is false when the command should end with code.
func phoneParse(fs *flag.FlagSet, args []string) (code int, ok bool) {
	switch err := fs.Parse(args); {
	case err == nil:
		return 0, true
	case errors.Is(err, flag.ErrHelp):
		return phoneExitOK, false
	}
	return phoneExitUsage, false
}

// phoneHolder is the agent name: -as, else the environment, else the default.
func phoneHolder(as string) string {
	if as != "" {
		return as
	}
	if h := os.Getenv(phoneHolderEnv); h != "" {
		return h
	}
	return phoneHolderDefault
}

// phoneStatus is the one JSON line `phone status` prints: the contract the
// agent skill reads. testdata/phone-status.golden.json pins its keys.
type phoneStatus struct {
	Free          bool    `json:"free"`            // online, no holder, no line, not kept for another name
	Device        string  `json:"device"`          // the phone's name
	Holder        *string `json:"holder"`          // agent name, null when nobody holds it
	Task          *string `json:"task"`            // the holder's task title
	ReservedFor   *string `json:"reserved_for"`    // kept for this name for a few seconds after its release
	ReservedLeftS int     `json:"reserved_left_s"` // seconds the reservation has left, 0 without one
	Waiting       int     `json:"waiting"`         // agents in line
	Link          string  `json:"link"`            // usb or wifi
	Health        string  `json:"health"`          // the gateway's status: idle, working, in_control, locked, needs_setup, offline, ...
}

// phoneStatusOf is a phone's status line for the agent named as: the phone kept
// for as counts as free for it.
func phoneStatusOf(d gateway.DeviceView, as string) phoneStatus {
	s := phoneStatus{Device: d.Info.Name, Waiting: len(d.Lease.Waiting), Link: d.Info.Connection, Health: string(d.Status)}
	if d.Lease.Holder != "" {
		s.Holder, s.Task = &d.Lease.Holder, &d.Lease.Task
	}
	mine := true
	if f := d.Lease.ReservedFor; f != "" {
		s.ReservedFor = &f
		s.ReservedLeftS = int((time.Until(d.Lease.ReservedUntil) + time.Second - 1) / time.Second)
		mine = f == as
	}
	s.Free = d.Status == gateway.DeviceIdle && d.Lease.Holder == "" && s.Waiting == 0 && mine
	return s
}

// pickDevice finds the phone named by want (id or name); without want, the
// only phone.
func pickDevice(devs []gateway.DeviceView, want string) (gateway.DeviceView, error) {
	var found []gateway.DeviceView
	for _, d := range devs {
		if want == "" || d.Info.ID == want || strings.EqualFold(d.Info.Name, want) {
			found = append(found, d)
		}
	}
	switch {
	case len(found) == 1:
		return found[0], nil
	case len(found) == 0 && want != "":
		return gateway.DeviceView{}, fmt.Errorf("no phone %q", want)
	case len(found) == 0:
		return gateway.DeviceView{}, errors.New("the gateway knows no phone")
	}
	return gateway.DeviceView{}, errors.New("the gateway knows several phones")
}

func phoneStatusCmd(args []string, pio phoneIO) int {
	fs := phoneFlags("status", pio)
	device := fs.String("device", "", "the phone, by id or name")
	as := fs.String("as", "", "the agent name the phone may be kept for")
	if code, ok := phoneParse(fs, args); !ok {
		return code
	}
	dirs, err := api.DefaultDirs()
	if err != nil {
		return phoneFail(pio, phoneExitToolError, err.Error(), "set PINKY_HOME or HOME")
	}
	ctl, err := api.DialControl(dirs)
	if err != nil {
		return phoneNoGateway(pio, dirs)
	}
	defer ctl.Close()
	var devs []gateway.DeviceView
	if err := ctl.Call("devices", nil, &devs); err != nil {
		return phoneFail(pio, phoneExitToolError, err.Error(), "run `pinkyctl devices`")
	}
	d, err := pickDevice(devs, *device)
	if err != nil {
		return phoneFail(pio, phoneExitUsage, err.Error(), "name one with -device (see `pinkyctl devices`)")
	}
	line, _ := json.Marshal(phoneStatusOf(d, phoneHolder(*as)))
	fmt.Fprintf(pio.out, "%s\n", line)
	return phoneExitOK
}

// dialPhone connects to the gateway's MCP role over its socket as the agent
// named holder, as dialTask does, but starts nothing: a missing gateway is
// errPhoneNoGateway.
func dialPhone(ctx context.Context, holder string) (*sdk.ClientSession, api.Dirs, error) {
	dirs, err := api.DefaultDirs()
	if err != nil {
		return nil, dirs, err
	}
	conn, err := api.Dial(dirs)
	if err != nil {
		return nil, dirs, errPhoneNoGateway
	}
	tok, err := api.EnsureToken(dirs.TokenFile())
	if err != nil {
		conn.Close()
		return nil, dirs, err
	}
	br, err := greetMCP(conn, tok)
	if err != nil {
		conn.Close()
		return nil, dirs, err
	}
	cl := sdk.NewClient(&sdk.Implementation{Name: "pinky-phone", Title: holder, Version: version}, nil)
	cs, err := cl.Connect(ctx, &sdk.IOTransport{Reader: connReader{br, conn}, Writer: conn}, nil)
	return cs, dirs, err
}

// phoneSession dials as the agent named as and says why not on failure.
func phoneSession(ctx context.Context, as string, pio phoneIO) (*sdk.ClientSession, int) {
	cs, dirs, err := dialPhone(ctx, phoneHolder(as))
	switch {
	case errors.Is(err, errPhoneNoGateway):
		return nil, phoneNoGateway(pio, dirs)
	case err != nil:
		return nil, phoneFail(pio, phoneExitToolError, err.Error(), "check that the daemon runs: `pinkyctl phone status`")
	}
	return cs, phoneExitOK
}

// phoneCatalog is the gateway's tools/list.
func phoneCatalog(ctx context.Context, cs *sdk.ClientSession) ([]*sdk.Tool, error) {
	var tools []*sdk.Tool
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		tools = append(tools, t)
	}
	return tools, nil
}

// phoneToolName finds name in the catalog, with or without the phone_ prefix.
func phoneToolName(tools []*sdk.Tool, name string) (*sdk.Tool, bool) {
	for _, t := range tools {
		if t.Name == name || t.Name == "phone_"+name {
			return t, true
		}
	}
	return nil, false
}

// firstSentence is a description up to its first full stop or line end.
func firstSentence(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}

func phoneToolsCmd(args []string, pio phoneIO) int {
	fs := phoneFlags("tools", pio)
	asJSON := fs.Bool("json", false, "print the full MCP definitions")
	as := fs.String("as", "", "the agent name")
	if code, ok := phoneParse(fs, args); !ok {
		return code
	}
	if fs.NArg() > 1 {
		return phoneFail(pio, phoneExitUsage, "tools takes at most one tool name", "run `pinkyctl phone tools [--json] [name]`")
	}
	ctx := context.Background()
	cs, code := phoneSession(ctx, *as, pio)
	if cs == nil {
		return code
	}
	defer cs.Close()
	tools, err := phoneCatalog(ctx, cs)
	if err != nil {
		return phoneFail(pio, phoneExitToolError, err.Error(), "check that the daemon runs: `pinkyctl phone status`")
	}
	if name := fs.Arg(0); name != "" {
		t, ok := phoneToolName(tools, name)
		if !ok {
			return phoneFail(pio, phoneExitUsage, fmt.Sprintf("no tool %q", name), "run `pinkyctl phone tools` for the list")
		}
		tools = []*sdk.Tool{t}
	}
	if *asJSON {
		var v any = tools
		if fs.NArg() == 1 {
			v = tools[0]
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintf(pio.out, "%s\n", b)
		return phoneExitOK
	}
	for _, t := range tools {
		title := t.Title
		if title == "" {
			title = t.Name
		}
		fmt.Fprintf(pio.out, "%s · %s · %s\n", t.Name, title, firstSentence(t.Description))
	}
	return phoneExitOK
}

// phoneReply is what one tool call gives its caller: the text for stdout,
// the CLI's own error for stderr, and the exit code. It is also the line the
// run's broker answers a `phone call` inside a script with.
type phoneReply struct {
	Out  string `json:"out"`
	Err  string `json:"err"`
	Code int    `json:"code"`
}

func phoneErrReply(code int, msg, fix string) phoneReply {
	return phoneReply{Err: fmt.Sprintf("phone: %s\nfix: %s\n", msg, fix), Code: code}
}

// phoneCode is the exit code of a tool's answer, read from its first line:
// the status, or "error: <code>".
func phoneCode(text string, isErr bool) int {
	head, _, _ := strings.Cut(text, "\n")
	head, _, _ = strings.Cut(head, " · ")
	if code, ok := strings.CutPrefix(head, "error: "); ok {
		switch gateway.ErrorCode(code) {
		case gateway.ErrLeaseHeld:
			return phoneExitBusy
		case gateway.ErrDevicePaused:
			return phoneExitPerson
		}
		return phoneExitToolError
	}
	switch gateway.Status(head) {
	case gateway.StatusTookOver, gateway.StatusPaused, gateway.StatusStopped, gateway.StatusLocked, gateway.StatusNeedsSetup:
		return phoneExitPerson
	}
	if isErr {
		return phoneExitToolError
	}
	return phoneExitOK
}

// phoneHead is the first token of a tool's answer: its status or "error: <code>".
func phoneHead(text string) string {
	head, _, _ := strings.Cut(text, "\n")
	head, _, _ = strings.Cut(head, " · ")
	return head
}

// phoneLeased says whether a phone_connect answer means the session now has
// the lease: any status but approval_pending does, even a paused one (the
// person has the phone, and the lease waits with it); an error does not.
func phoneLeased(text string) bool {
	head := phoneHead(text)
	return head != "" && !strings.HasPrefix(head, "error: ") && head != string(gateway.StatusPending)
}

// phoneNeedsLease says whether a tool works on a phone the session must
// hold. Connecting, releasing and Ask Pinky's tasks do not.
func phoneNeedsLease(name string) bool {
	return name != "phone_connect" && name != "phone_release" && !strings.HasPrefix(name, "phone_task_")
}

// phoneRun owns one MCP session for one command or one script: it takes the
// lease lazily, before the first call that needs one, forwards every call and
// releases at the end.
type phoneRun struct {
	cs       *sdk.ClientSession
	task     string
	device   string
	deadline time.Time // the end of -wait
	imageDir string

	mu        sync.Mutex // guards connected; held while connecting
	connected bool

	toolsMu sync.Mutex
	tools   []*sdk.Tool

	images atomic.Int64
	in     atomic.Int64 // bytes of tool output forwarded
}

func newPhoneRun(cs *sdk.ClientSession, task, device string, wait time.Duration) *phoneRun {
	id := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	return &phoneRun{cs: cs, task: task, device: device, deadline: time.Now().Add(wait),
		imageDir: filepath.Join(os.TempDir(), "pinky-phone", id)}
}

// resolve finds tool in the session's tools/list.
func (r *phoneRun) resolve(ctx context.Context, tool string) (string, *phoneReply) {
	r.toolsMu.Lock()
	defer r.toolsMu.Unlock()
	if r.tools == nil {
		tools, err := phoneCatalog(ctx, r.cs)
		if err != nil {
			rep := phoneErrReply(phoneExitNoGateway, err.Error(), "check that the daemon runs: `pinkyctl phone status`")
			return "", &rep
		}
		r.tools = tools
	}
	t, ok := phoneToolName(r.tools, tool)
	if !ok {
		rep := phoneErrReply(phoneExitUsage, fmt.Sprintf("no tool %q", tool), "run `pinkyctl phone tools` for the list")
		return "", &rep
	}
	return t.Name, nil
}

// do makes one tool call through the session.
func (r *phoneRun) do(ctx context.Context, tool string, args map[string]any) phoneReply {
	name, bad := r.resolve(ctx, tool)
	if bad != nil {
		return *bad
	}
	if phoneNeedsLease(name) {
		if rep, ok := r.ensureConnected(ctx); !ok {
			return rep
		}
	}
	rep, text := r.callTool(ctx, name, args)
	r.mu.Lock()
	switch name {
	case "phone_connect":
		r.connected = r.connected || phoneLeased(text)
	case "phone_release":
		r.connected = r.connected && rep.Code != phoneExitOK
	}
	r.mu.Unlock()
	return rep
}

// callTool calls one tool and turns its result into a reply, also returning
// the tool's text. Images go to files.
func (r *phoneRun) callTool(ctx context.Context, name string, args map[string]any) (phoneReply, string) {
	res, err := r.cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		code := phoneExitNoGateway
		if ctx.Err() != nil {
			code = phoneExitToolError
		}
		return phoneErrReply(code, name+": "+err.Error(), "check that the daemon runs: `pinkyctl phone status`"), ""
	}
	var text []string
	var images []string
	for _, c := range res.Content {
		switch c := c.(type) {
		case *sdk.TextContent:
			text = append(text, c.Text)
		case *sdk.ImageContent:
			line, err := r.saveImage(c.Data)
			if err != nil {
				return phoneErrReply(phoneExitToolError, "could not save the screenshot: "+err.Error(), "check that $TMPDIR is writable"), ""
			}
			images = append(images, line)
			r.in.Add(int64(len(c.Data)))
		}
	}
	joined := strings.Join(text, "\n")
	r.in.Add(int64(len(joined)))
	out := joined + "\n"
	for _, l := range images {
		out += l + "\n"
	}
	return phoneReply{Out: out, Code: phoneCode(joined, res.IsError)}, joined
}

// saveImage writes a screenshot under $TMPDIR/pinky-phone/<run>/ and says where.
func (r *phoneRun) saveImage(data []byte) (string, error) {
	if err := os.MkdirAll(r.imageDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(r.imageDir, strconv.FormatInt(r.images.Add(1), 10)+".jpg")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return fmt.Sprintf("image: %s · %d", path, len(data)), nil
}

// ensureConnected takes the lease with phone_connect, queueing for the time
// left of -wait and asking again while the gateway answers lease_held. ok is
// false when the call that needed the lease cannot go on; the reply says why.
func (r *phoneRun) ensureConnected(ctx context.Context) (phoneReply, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for !r.connected {
		args := map[string]any{"task": r.task}
		if r.device != "" {
			args["device"] = r.device
		}
		if ms := time.Until(r.deadline).Milliseconds(); ms > 0 {
			args["wait_ms"] = min(ms, 600000)
		}
		rep, text := r.callTool(ctx, "phone_connect", args)
		r.connected = phoneLeased(text)
		switch {
		case phoneHead(text) == string(gateway.StatusPending):
			rep.Code = phoneExitPerson
			return rep, false
		case rep.Code == phoneExitOK:
		case rep.Code != phoneExitBusy || time.Until(r.deadline) <= 0 || ctx.Err() != nil:
			return rep, false
		default:
			select {
			case <-time.After(300 * time.Millisecond):
			case <-ctx.Done():
			}
		}
	}
	return phoneReply{}, true
}

// release ends the lease, if the session holds one.
func (r *phoneRun) release(outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.connected {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r.callTool(ctx, "phone_release", map[string]any{"outcome": outcome})
	r.connected = false
}

// phoneOutcome is how phone_release is told a run ended.
func phoneOutcome(code int) string {
	if code == 0 {
		return "done"
	}
	return "failed"
}

// parseCallArgs reads a call's arguments: none, one JSON object, "-" for a
// JSON object on stdin, or k=v pairs where a value that is JSON is JSON and
// anything else is a string.
func parseCallArgs(rest []string, stdin io.Reader) (map[string]any, error) {
	args := map[string]any{}
	object := func(b []byte) error {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&args); err != nil {
			return fmt.Errorf("arguments are not a JSON object: %w", err)
		}
		return nil
	}
	switch {
	case len(rest) == 0:
	case len(rest) == 1 && rest[0] == "-":
		b, err := io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
		return args, object(b)
	case len(rest) == 1 && strings.HasPrefix(rest[0], "{"):
		return args, object([]byte(rest[0]))
	default:
		for _, kv := range rest {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return nil, fmt.Errorf("%q is not key=value", kv)
			}
			var val any = v
			if json.Valid([]byte(v)) {
				dec := json.NewDecoder(strings.NewReader(v))
				dec.UseNumber()
				if err := dec.Decode(&val); err != nil {
					val = v
				}
			}
			args[k] = val
		}
	}
	return args, nil
}

func phoneCallCmd(args []string, pio phoneIO) int {
	fs := phoneFlags("call", pio)
	as := fs.String("as", "", "the agent name")
	wait := fs.Float64("wait", 60, "seconds to queue for the phone")
	task := fs.String("task", "", "the task title shown in Pinky")
	device := fs.String("device", "", "the phone, by id or name")
	if code, ok := phoneParse(fs, args); !ok {
		return code
	}
	if fs.NArg() == 0 {
		return phoneFail(pio, phoneExitUsage, "call needs a tool name", "run `pinkyctl phone call <tool> [json | k=v ... | -]`")
	}
	tool := fs.Arg(0)
	callArgs, err := parseCallArgs(fs.Args()[1:], pio.in)
	if err != nil {
		return phoneFail(pio, phoneExitUsage, err.Error(), "pass one JSON object, or key=value pairs")
	}
	if sock := os.Getenv(phoneRunEnv); sock != "" {
		return phoneViaRun(sock, tool, callArgs, pio)
	}
	ctx := context.Background()
	cs, code := phoneSession(ctx, *as, pio)
	if cs == nil {
		return code
	}
	defer cs.Close()
	if *task == "" {
		*task = fmt.Sprintf("phone %s · %d", strings.TrimPrefix(tool, "phone_"), os.Getpid())
	}
	r := newPhoneRun(cs, *task, *device, time.Duration(*wait*float64(time.Second)))
	rep := r.do(ctx, tool, callArgs)
	rep.write(pio)
	r.release(phoneOutcome(rep.Code))
	return rep.Code
}

// write prints a reply on pio.
func (rep phoneReply) write(pio phoneIO) {
	io.WriteString(pio.out, rep.Out)
	io.WriteString(pio.err, rep.Err)
}

// phoneViaRun makes one call through the session of the run this script
// belongs to: one JSON line to its socket, one phoneReply line back.
func phoneViaRun(sock, tool string, args map[string]any, pio phoneIO) int {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return phoneFail(pio, phoneExitNoGateway, "the phone run this script belongs to is gone",
			"call phone only from a script started by `pinkyctl phone run`")
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(map[string]any{"tool": tool, "args": args}); err != nil {
		return phoneFail(pio, phoneExitToolError, err.Error(), "run the script again")
	}
	var rep phoneReply
	if err := json.NewDecoder(c).Decode(&rep); err != nil {
		return phoneFail(pio, phoneExitToolError, "the phone run ended before it answered", "run the script again")
	}
	rep.write(pio)
	return rep.Code
}

// serve answers the script's phone calls on ln: one JSON line each way.
func (r *phoneRun) serve(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			var req struct {
				Tool string         `json:"tool"`
				Args map[string]any `json:"args"`
			}
			if json.NewDecoder(c).Decode(&req) != nil {
				return
			}
			if req.Args == nil {
				req.Args = map[string]any{}
			}
			json.NewEncoder(c).Encode(r.do(ctx, req.Tool, req.Args))
		}()
	}
}

// countWriter counts the bytes a script prints.
type countWriter struct {
	w io.Writer
	n atomic.Int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	c.n.Add(int64(len(b)))
	return c.w.Write(b)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// phoneExitStatus is a finished script's exit status: 128 plus the signal
// that ended it.
func phoneExitStatus(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return phoneExitToolError
}

// phoneScript is the command a run executes and the name its task takes.
func phoneScript(fs *flag.FlagSet, eText string, pio phoneIO) (cmd *exec.Cmd, name string, err error) {
	switch {
	case eText != "":
		return exec.Command("sh", append([]string{"-c", eText, "sh"}, fs.Args()...)...), "script", nil
	case fs.NArg() == 1 && fs.Arg(0) == "-":
		b, err := io.ReadAll(pio.in)
		if err != nil {
			return nil, "", err
		}
		return exec.Command("sh", "-c", string(b)), "stdin", nil
	case fs.NArg() >= 1:
		file := fs.Arg(0)
		st, err := os.Stat(file)
		if err != nil {
			return nil, "", err
		}
		if st.Mode()&0o111 != 0 {
			if !strings.ContainsRune(file, filepath.Separator) {
				file = "." + string(filepath.Separator) + file
			}
			return exec.Command(file, fs.Args()[1:]...), filepath.Base(file), nil
		}
		return exec.Command("sh", fs.Args()...), filepath.Base(file), nil
	}
	return nil, "", errors.New("run needs a script")
}

func kb(n int64) string { return fmt.Sprintf("%.1fKB", float64(n)/1024) }

func phoneRunCmd(args []string, pio phoneIO) int {
	fs := phoneFlags("run", pio)
	as := fs.String("as", "", "the agent name")
	wait := fs.Float64("wait", 60, "seconds to queue for the phone")
	task := fs.String("task", "", "the task title shown in Pinky")
	device := fs.String("device", "", "the phone, by id or name")
	eText := fs.String("e", "", "shell text to run")
	if code, ok := phoneParse(fs, args); !ok {
		return code
	}
	cmd, name, err := phoneScript(fs, *eText, pio)
	if err != nil {
		return phoneFail(pio, phoneExitUsage, err.Error(), "run `pinkyctl phone run (<file> | -e '<sh>' | -)`")
	}
	cs, code := phoneSession(context.Background(), *as, pio)
	if cs == nil {
		return code
	}
	defer cs.Close()
	if *task == "" {
		*task = fmt.Sprintf("%s · %d", name, os.Getpid())
	}
	r := newPhoneRun(cs, *task, *device, time.Duration(*wait*float64(time.Second)))

	dir, err := os.MkdirTemp("", "pp-")
	if err != nil {
		return phoneFail(pio, phoneExitToolError, err.Error(), "check that $TMPDIR is writable")
	}
	defer os.RemoveAll(dir)
	exe, err := os.Executable()
	if err != nil {
		return phoneFail(pio, phoneExitToolError, err.Error(), "run pinkyctl by its path")
	}
	shim := "#!/bin/sh\nexec " + shellQuote(exe) + ` phone "$@"` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "phone"), []byte(shim), 0o700); err != nil {
		return phoneFail(pio, phoneExitToolError, err.Error(), "check that $TMPDIR is writable")
	}
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return phoneFail(pio, phoneExitToolError, err.Error(), "check that $TMPDIR is writable and its path is short")
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.serve(ctx, ln)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)

	printed := &countWriter{w: pio.out}
	cmd.Env = append(os.Environ(), phoneRunEnv+"="+sock, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pio.in, printed, pio.err
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		return phoneFail(pio, phoneExitToolError, err.Error(), "check the script path and its permissions")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var status int
	select {
	case err := <-done:
		status = phoneExitStatus(err)
	case sig := <-sigs:
		cancel()
		cmd.Process.Signal(sig)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
		status = 128
		if s, ok := sig.(syscall.Signal); ok {
			status += int(s)
		}
	}
	r.release(phoneOutcome(status))
	line := fmt.Sprintf("[code-mode: %s tool output -> %s printed", kb(r.in.Load()), kb(printed.n.Load()))
	if in := r.in.Load(); in > 0 {
		line += fmt.Sprintf(" (%.1f%%)", max(0, 100*(1-float64(printed.n.Load())/float64(in))))
	}
	fmt.Fprintln(pio.err, line+"]")
	return status
}
