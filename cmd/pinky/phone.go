package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
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

status prints one JSON line: free, device, holder, task, reserved_for,
reserved_left_s, waiting, link, health. tools lists the gateway's phone_*
tools as it serves them now. call makes one tool call (the tool name with or
without phone_): it takes the phone, calls, and releases. run holds one session
and one lease for the script's whole life; phone call inside it goes through
that session.

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
