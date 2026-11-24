package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// pinkyctl phone fill types a saved login's password into a field of the
// phone: it asks the owner's credential broker (cred) to run `phone secret
// put` with the password on stdin, then types {{secret:NAME}} through
// phone_type, so the password is in no argument, file, output or log of
// the agent's. The broker asks the owner each time.

func phoneFillUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pinkyctl phone fill -login ID [-ref N] [-submit] [-app BUNDLE_ID] [-as NAME] [-wait S] [-device D]

fill types the password of the broker login ID (cred find) into the field ref
(the focused field without -ref) and presses Enter with -submit. The app that
shows the field is read from the screen unless -app names it; it must be on
credential_apps in rules.yaml. The owner approves each use in the broker.
`)
}

var phoneAppOnScreen = regexp.MustCompile(`app: [^(\n]*\(([A-Za-z0-9._-]+)\)`)

func phoneFillCmd(args []string, pio phoneIO) int {
	fs := phoneFlags("fill", pio)
	fs.SetOutput(io.Discard)
	login := fs.String("login", "", "")
	ref := fs.Int("ref", 0, "")
	submit := fs.Bool("submit", false, "")
	app := fs.String("app", "", "")
	as := fs.String("as", "", "")
	wait := fs.Float64("wait", 60, "")
	device := fs.String("device", "", "")
	if err := fs.Parse(args); err != nil || *login == "" {
		phoneFillUsage(pio.err)
		return phoneExitUsage
	}
	exe, err := os.Executable()
	if err != nil {
		return phoneFail(pio, phoneExitToolError, err.Error(), "run pinkyctl from its own path")
	}
	ctx := context.Background()
	cs, code := phoneSession(ctx, *as, pio)
	if cs == nil {
		return code
	}
	defer cs.Close()
	r := newPhoneRun(cs, "fill a password", *device, time.Duration(*wait*float64(time.Second)))
	outcome := phoneExitToolError
	defer func() { r.release(phoneOutcome(outcome)) }()

	bundle := *app
	if bundle == "" {
		rep := r.do(ctx, "phone_observe", map[string]any{})
		if rep.Code != phoneExitOK {
			rep.write(pio)
			outcome = rep.Code
			return rep.Code
		}
		m := phoneAppOnScreen.FindStringSubmatch(rep.Out)
		if m == nil {
			outcome = phoneExitToolError
			return phoneFail(pio, phoneExitToolError, "could not read which app shows the field", "pass -app BUNDLE_ID")
		}
		bundle = m[1]
	}
	b := make([]byte, 4)
	rand.Read(b)
	name := "fill-" + hex.EncodeToString(b)
	if code := phoneStage(ctx, exe, *login, bundle, name, pio); code != phoneExitOK {
		outcome = code
		return code
	}
	call := map[string]any{"text": "{{secret:" + name + "}}", "submit": *submit}
	if *ref > 0 {
		call["ref"] = *ref
	}
	rep := r.do(ctx, "phone_type", call)
	rep.write(pio)
	outcome = rep.Code
	return rep.Code
}

// phoneStage has the broker run `phone secret put` with the login's
// password, waiting for the owner's answer.
func phoneStage(ctx context.Context, exe, login, bundle, name string, pio phoneIO) int {
	reason := fmt.Sprintf("Type the password into a field of %s on the agent's iPhone", bundle)
	cred := []string{"run", "--login", login, "--reason", reason, "--stdin", "{password}"}
	if h := os.Getenv("PINKY_HOME"); h != "" {
		cred = append(cred, "--env", "PINKY_HOME="+h)
	}
	cred = append(cred, "--", exe, "phone", "secret", "put", "-app", bundle, name)
	for try := 0; try < 12; try++ {
		var out, errb bytes.Buffer
		c := exec.CommandContext(ctx, "cred", cred...)
		c.Stdout, c.Stderr = &out, &errb
		err := c.Run()
		var res struct {
			Status    string `json:"status"`
			RequestID string `json:"request_id"`
			Message   string `json:"message"`
			ExitCode  int    `json:"exit_code"`
		}
		json.Unmarshal(out.Bytes(), &res)
		switch {
		case err != nil && res.Status == "" && !isExit(err):
			return phoneFail(pio, phoneExitToolError, "the credential broker (cred) did not run: "+err.Error(), "install the broker CLI or ask the owner")
		case res.Status == "waiting":
			cred = []string{"run", "--request-id", res.RequestID}
			continue
		case res.Status == "done" && res.ExitCode == 0:
			return phoneExitOK
		case res.Status == "done":
			return phoneFail(pio, phoneExitToolError, "staging the password failed: "+strings.TrimSpace(phoneFirstLine(errb.String()+res.Message)), "check `pinkyctl phone secret help` and credential_apps in rules.yaml")
		default:
			return phoneFail(pio, phoneExitPerson, "the owner did not approve the password ("+res.Status+"): "+res.Message, "ask the owner, then call fill again")
		}
	}
	return phoneFail(pio, phoneExitPerson, "the owner did not answer the broker in time", "ask the owner, then call fill again")
}

func isExit(err error) bool { _, ok := err.(*exec.ExitError); return ok }

func phoneFirstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
