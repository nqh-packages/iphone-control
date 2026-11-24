package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, name, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPhoneInterruptsMatch(t *testing.T) {
	book := writeFile(t, "book.yaml", "entries:\n  - name: tour\n    kind: dismiss\n    when:\n      texts: [\"Take a tour\"]\n    do:\n      - tap: \"Skip\"\n")
	screen := writeFile(t, "tour.txt", "screen 400x800 · app: Notes (com.apple.mobilenotes) · portrait · keyboard hidden\n[1] text \"Take a tour\" @200,300\n[2] button \"Skip\" @200,400\n")
	code, out, errOut := phone("interrupts", "match", book, screen)
	if code != phoneExitOK || !strings.Contains(out, "fires: tour") || !strings.Contains(out, `step: tap "Skip"`) {
		t.Fatalf("match: exit %d\n%s\n%s", code, out, errOut)
	}
	plain := writeFile(t, "plain.txt", "screen 400x800 · portrait · keyboard hidden\n[1] text \"Hello\" @200,300\n")
	if code, out, _ = phone("interrupts", "match", book, plain); code != phoneExitOK || !strings.Contains(out, "fires: none") {
		t.Fatalf("plain screen: exit %d %s", code, out)
	}
}

func TestPhoneInterruptsMatchConsentNeedsExpectAndTyped(t *testing.T) {
	book := writeFile(t, "empty.yaml", "entries: []\n")
	request := writeFile(t, "request.txt", "screen 400x800 · app: SpringBoard (com.apple.springboard) · portrait · keyboard hidden\n[1] text \"Sign In Requested\" @200,300\n[2] button \"Allow\" @200,400\n")
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "fires: none"},
		{[]string{"-expect", "sign-in"}, "fires: none"},
		{[]string{"-typed"}, "fires: none"},
		{[]string{"-expect", "sign-in", "-typed"}, "fires: sign-in-request"},
	} {
		args := append([]string{"interrupts", "match"}, c.args...)
		args = append(args, book, request)
		if code, out, errOut := phone(args...); code != phoneExitOK || !strings.Contains(out, c.want) {
			t.Errorf("%v: exit %d\n%s\n%s", c.args, code, out, errOut)
		}
	}
}

func TestPhoneInterruptsCheckNamesTheRule(t *testing.T) {
	bad := writeFile(t, "bad.yaml", "entries:\n  - name: sneaky\n    kind: dismiss\n    when:\n      texts: [\"x\"]\n    do:\n      - type_code: true\n")
	code, _, errOut := phone("interrupts", "check", bad)
	if code != phoneExitUsage || !strings.Contains(errOut, "bad.yaml:2") || !strings.Contains(errOut, "fix:") {
		t.Fatalf("check: exit %d\n%s", code, errOut)
	}
	good := writeFile(t, "good.yaml", "entries: []\n")
	if code, out, _ := phone("interrupts", "check", good); code != phoneExitOK || !strings.HasPrefix(out, "ok:") {
		t.Fatalf("check of a sound file: exit %d %s", code, out)
	}
}

func TestRunExpectFlag(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	expect := expectFlag(fs)
	if err := fs.Parse([]string{"-expect", "sign-in", "-expect", "store, sign-in"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*expect, "|"); got != "sign-in|store|sign-in" {
		t.Fatalf("expect = %s", got)
	}
	newPhoneEnv(t)
	code, out, errOut := phone("run", "-as", "expecting", "-expect", "sign-in", "-e", "phone call observe | head -1")
	if code != phoneExitOK || !strings.HasPrefix(out, "settled") {
		t.Fatalf("run with -expect: exit %d\n%s\n%s", code, out, errOut)
	}
}
