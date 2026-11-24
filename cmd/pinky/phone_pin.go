package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/secret"
)

// pinkyctl phone pin saves the passcode the daemon enters when an agent's call
// finds the phone locked (PINKY_AUTO_UNLOCK=1). set reads it from a terminal
// with no echo, from stdin, or from a file it zeroes and deletes; it never
// takes it as an argument. There is no verb that shows it, and nothing printed
// here is derived from it.

func phonePinUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pinkyctl phone pin set    [-device D] [-file PATH]
  pinkyctl phone pin check  [-device D]
  pinkyctl phone pin delete [-device D]

set reads the passcode with no echo on a terminal, or one line from stdin, or
from the file at -file, which it zeroes and deletes after reading. check
prints {"device":...,"saved":...,"refused":...} and never reads the passcode.
-device is the id the gateway lists; it may be left out when there is one phone.
`)
}

// phonePINs is the passcode store the pin verbs use; tests replace it.
var phonePINs = secret.NewAgentPINs

// phonePinDevice is a phone the gateway knows.
type phonePinDevice struct{ ID, Name string }

// phonePinDevices lists the phones the gateway knows: from the running
// daemon, else from PINKY_ALLOW_DEVICES, the only ids a gateway accepts.
var phonePinDevices = func() ([]phonePinDevice, error) {
	if dirs, err := api.DefaultDirs(); err == nil {
		if ctl, err := api.DialControl(dirs); err == nil {
			defer ctl.Close()
			var devs []gateway.DeviceView
			if err := ctl.Call("devices", nil, &devs); err != nil {
				return nil, err
			}
			out := make([]phonePinDevice, 0, len(devs))
			for _, d := range devs {
				out = append(out, phonePinDevice{ID: d.Info.ID, Name: d.Info.Name})
			}
			return out, nil
		}
	}
	var out []phonePinDevice
	for _, id := range allowDevices() {
		out = append(out, phonePinDevice{ID: id})
	}
	return out, nil
}

func phonePinCmd(args []string, pio phoneIO) int {
	if len(args) == 0 {
		phonePinUsage(pio.err)
		return phoneExitUsage
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "set", "check", "delete":
		return phonePinVerb(verb, rest, pio)
	case "help", "-h", "--help", "-help":
		phonePinUsage(pio.out)
		return phoneExitOK
	}
	return phoneFail(pio, phoneExitUsage, "unknown pin command", "run `pinkyctl phone pin help`")
}

func phonePinVerb(verb string, args []string, pio phoneIO) int {
	fs := phoneFlags("pin "+verb, pio)
	fs.SetOutput(io.Discard)
	device := fs.String("device", "", "")
	file := fs.String("file", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || (*file != "" && verb != "set") {
		return phoneFail(pio, phoneExitUsage, "pin "+verb+" takes no passcode and no argument", "run `pinkyctl phone pin help`")
	}
	id, code := phonePinDeviceID(*device, pio)
	if id == "" {
		return code
	}
	pins := phonePINs()
	switch verb {
	case "check":
		st, err := pins.State(id)
		if err != nil {
			return phonePinFail(pio, err, id)
		}
		line, _ := json.Marshal(struct {
			Device  string `json:"device"`
			Saved   bool   `json:"saved"`
			Refused bool   `json:"refused"`
		}{id, st.Saved, st.Refused})
		fmt.Fprintf(pio.out, "%s\n", line)
	case "delete":
		if err := pins.Delete(id); err != nil {
			return phonePinFail(pio, err, id)
		}
		line, _ := json.Marshal(struct {
			Device string `json:"device"`
			Saved  bool   `json:"saved"`
		}{id, false})
		fmt.Fprintf(pio.out, "%s\n", line)
	default:
		return phonePinSet(pins, id, *file, pio)
	}
	return phoneExitOK
}

func phonePinSet(pins *secret.AgentPINs, id, file string, pio phoneIO) int {
	pin, err := readPinInput(file, pio)
	defer zero(pin)
	if err != nil {
		return phoneFail(pio, phoneExitUsage, err.Error(), "give one passcode: type it at the prompt, or pipe one line in, or use -file")
	}
	if err := pins.Save(id, pin); err != nil {
		if errors.Is(err, secret.ErrPINInvalid) {
			return phoneFail(pio, phoneExitUsage, err.Error(), "enter the phone's passcode as it is typed on its pad")
		}
		return phonePinFail(pio, err, id)
	}
	line, _ := json.Marshal(struct {
		Device string `json:"device"`
		Saved  bool   `json:"saved"`
	}{id, true})
	fmt.Fprintf(pio.out, "%s\n", line)
	return phoneExitOK
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// phonePinDeviceID resolves -device to the id the gateway lists. An empty id
// means the command ends with the returned code.
func phonePinDeviceID(want string, pio phoneIO) (string, int) {
	devs, err := phonePinDevices()
	if err != nil {
		return "", phoneFail(pio, phoneExitToolError, err.Error(), "run `pinkyctl phone status`")
	}
	var found []phonePinDevice
	for _, d := range devs {
		if want == "" || d.ID == want || (d.Name != "" && strings.EqualFold(d.Name, want)) {
			found = append(found, d)
		}
	}
	switch {
	case len(found) == 1:
		return found[0].ID, phoneExitOK
	case len(devs) == 0:
		return "", phoneFail(pio, phoneExitUsage, "the gateway knows no phone",
			"start the daemon (`pinky daemon`), or set PINKY_ALLOW_DEVICES to the phone's id")
	case len(found) == 0:
		return "", phoneFail(pio, phoneExitUsage, fmt.Sprintf("no phone %q among: %s", want, phonePinChoices(devs)),
			"name one with -device; a passcode saved under another id is never used")
	}
	return "", phoneFail(pio, phoneExitUsage, "several phones: "+phonePinChoices(devs), "name one with -device")
}

func phonePinChoices(devs []phonePinDevice) string {
	var ids []string
	for _, d := range devs {
		ids = append(ids, d.ID)
	}
	return strings.Join(ids, ", ")
}

// phonePinFail words a keychain failure with its fix line.
func phonePinFail(pio phoneIO, err error, id string) int {
	fix := secret.PINFix(err, id)
	if fix == "" {
		fix = "run `pinkyctl phone pin check -device " + id + "`"
	}
	return phoneFail(pio, phoneExitToolError, strings.TrimPrefix(err.Error(), "secret: "), fix)
}

// pinInputMax bounds what set reads, so a wrong file cannot fill memory.
const pinInputMax = 256

// readPinInput reads one passcode: from file, which is then zeroed and
// deleted, else from a terminal with no echo, else one line of stdin.
func readPinInput(file string, pio phoneIO) ([]byte, error) {
	if file != "" {
		return readPinFile(file)
	}
	if f, ok := pio.in.(*os.File); ok {
		if restore, err := echoOff(int(f.Fd())); err == nil {
			defer restore()
			fmt.Fprint(pio.err, "Passcode (not shown): ")
			defer fmt.Fprintln(pio.err)
		}
	}
	return readPinLine(pio.in)
}

// readPinLine reads exactly one line: empty input and a second line are
// refused.
func readPinLine(r io.Reader) ([]byte, error) {
	br := bufio.NewReaderSize(io.LimitReader(r, pinInputMax), pinInputMax)
	line, err := br.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return nil, errors.New("could not read the passcode")
	}
	line = bytes.TrimRight(line, "\r\n")
	if rest, _ := io.ReadAll(br); len(bytes.TrimSpace(rest)) > 0 {
		zero(rest)
		zero(line)
		return nil, errors.New("more than one line was given")
	}
	if len(line) == 0 {
		return nil, errors.New("no passcode was given")
	}
	return line, nil
}

// readPinFile reads the file's one line and scrubs it, whatever the line
// held: the delivered file is the passcode's only copy besides the keychain.
func readPinFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("-file is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("could not open -file")
	}
	pin, rerr := readPinLine(f)
	f.Close()
	if err := scrubFile(path, fi.Size()); err != nil {
		zero(pin)
		return nil, errors.New("could not zero and delete -file; it was not used")
	}
	return pin, rerr
}

// scrubFile overwrites a file with zeros and deletes it.
func scrubFile(path string, size int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.Write(make([]byte, size))
	serr := f.Sync()
	f.Close()
	if err := os.Remove(path); err != nil {
		return err
	}
	return errors.Join(werr, serr)
}
