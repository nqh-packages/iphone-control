package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/interrupt"
)

type expectList []string

func (e *expectList) String() string { return strings.Join(*e, ",") }

func (e *expectList) Set(v string) error {
	for _, n := range strings.Split(v, ",") {
		if n = strings.TrimSpace(n); n != "" {
			*e = append(*e, n)
		}
	}
	return nil
}

func expectFlag(fs *flag.FlagSet) *expectList {
	var e expectList
	fs.Var(&e, "expect", "what the script starts, such as sign-in or store (repeat or comma-separate)")
	return &e
}

const interruptsHelp = `Usage:
  pinkyctl phone interrupts check <file.yaml>
  pinkyctl phone interrupts match <file.yaml> <screen.txt> [-expect NAME] [-typed]

check loads one book file and prints each rule it breaks as file:line: rule,
with a fix line; exit 0 when the file is sound, 2 when not.

match runs the same Match the gateway runs, offline, on a saved screen read
(the text of phone call observe, one line per element; add size=WxH to a line
for code rows and boxes). It also loads the built-in entries and
$PINKY_HOME/interrupts.d. It prints "fires: NAME", the entry's kind and the
steps it would run, or "fires: none". -expect declares what the task started;
-typed says a credential was typed in the last minute. A saved screen holds
no earlier read, so entries that need a kept code assume it was kept.
`

func phoneInterruptsCmd(args []string, pio phoneIO) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(pio.out, interruptsHelp)
		return phoneExitOK
	}
	switch args[0] {
	case "check":
		return phoneInterruptsCheck(args[1:], pio)
	case "match":
		return phoneInterruptsMatch(args[1:], pio)
	}
	return phoneFail(pio, phoneExitUsage, fmt.Sprintf("unknown interrupts command %q", args[0]), "run `pinkyctl phone interrupts help`")
}

func readBookFile(path string) (interrupt.File, error) {
	data, err := os.ReadFile(path)
	return interrupt.File{Name: path, Data: data}, err
}

func phoneInterruptsCheck(args []string, pio phoneIO) int {
	if len(args) != 1 {
		return phoneFail(pio, phoneExitUsage, "check takes one book file", "run `pinkyctl phone interrupts check <file.yaml>`")
	}
	f, err := readBookFile(args[0])
	if err != nil {
		return phoneFail(pio, phoneExitUsage, err.Error(), "give the path of a book file")
	}
	_, errs := interrupt.Load([]interrupt.File{f})
	for _, e := range errs {
		fmt.Fprintln(pio.err, e.Error())
	}
	if len(errs) > 0 {
		return phoneExitUsage
	}
	fmt.Fprintf(pio.out, "ok: %s\n", args[0])
	return phoneExitOK
}

func phoneInterruptsMatch(args []string, pio phoneIO) int {
	fs := phoneFlags("interrupts match", pio)
	expect := expectFlag(fs)
	typed := fs.Bool("typed", false, "a credential was typed in the last minute")
	if code, ok := phoneParse(fs, args); !ok {
		return code
	}
	if fs.NArg() != 2 {
		return phoneFail(pio, phoneExitUsage, "match takes a book file and a screen file", "run `pinkyctl phone interrupts match <file.yaml> <screen.txt>`")
	}
	f, err := readBookFile(fs.Arg(0))
	if err != nil {
		return phoneFail(pio, phoneExitUsage, err.Error(), "give the path of a book file")
	}
	text, err := os.ReadFile(fs.Arg(1))
	if err != nil {
		return phoneFail(pio, phoneExitUsage, err.Error(), "save a read with `phone call observe > screen.txt`")
	}
	s, err := interrupt.ParseScreen(string(text))
	if err != nil {
		return phoneFail(pio, phoneExitUsage, err.Error(), "use the lines `phone call observe` prints")
	}
	book, errs := interrupt.Load(append([]interrupt.File{f}, interrupt.Builtin()...))
	for _, e := range errs {
		fmt.Fprintln(pio.err, e.Error())
	}
	if len(errs) > 0 {
		return phoneExitUsage
	}
	now := time.Now()
	lease := interrupt.Lease{Expects: *expect, Fired: map[string]int{}, Now: now, CodeKept: true}
	if *typed {
		lease.Triggers = map[interrupt.Trigger]time.Time{interrupt.TriggerCredentialTyped: now.Add(-time.Minute)}
	}
	e, ok := book.Match(s, lease)
	if !ok {
		fmt.Fprintln(pio.out, "fires: none")
		return phoneExitOK
	}
	fmt.Fprintf(pio.out, "fires: %s\nkind: %s\nsource: %s\n", e.Name, e.Kind, e.Source)
	for _, st := range e.Steps {
		fmt.Fprintf(pio.out, "step: %s\n", describeStep(st))
	}
	if e.Report != "" {
		fmt.Fprintf(pio.out, "report: %s\n", e.Report)
	}
	return phoneExitOK
}

func describeStep(st interrupt.Step) string {
	switch st.Op {
	case interrupt.OpWait:
		return fmt.Sprintf("wait %s", st.Wait)
	case interrupt.OpTypeCode:
		return "type_code"
	}
	return fmt.Sprintf("%s %q", st.Op, st.Label)
}
