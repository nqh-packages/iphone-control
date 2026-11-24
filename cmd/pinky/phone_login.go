package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/DIYgod/pinky/internal/secret"
)

// pinkyctl phone login keeps the password of an account that belongs to the
// agents (the phone's own Apple Account) in the Mac's login keychain, where
// `phone fill -account` reads it without asking anyone and types it through
// the vault. The owner saves it once; no one is asked after that.

var agentLogins = secret.NewAgentLogins

func phoneLoginUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pinkyctl phone login set    -name NAME [-file PATH]
  pinkyctl phone login check  -name NAME
  pinkyctl phone login delete -name NAME

set reads the password with no echo on a terminal, or one line from stdin, or
from the file at -file, which it zeroes and deletes after reading. check prints
{"name":...,"saved":...} and never the password. fill uses it:
pinkyctl phone fill -account NAME [-ref N] [-submit].
`)
}

func phoneLoginCmd(args []string, pio phoneIO) int {
	if len(args) == 0 {
		phoneLoginUsage(pio.err)
		return phoneExitUsage
	}
	verb := args[0]
	if verb == "help" || verb == "-h" || verb == "--help" {
		phoneLoginUsage(pio.out)
		return phoneExitOK
	}
	fs := phoneFlags("login "+verb, pio)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "")
	file := fs.String("file", "", "")
	if err := fs.Parse(args[1:]); err != nil || *name == "" {
		phoneLoginUsage(pio.err)
		return phoneExitUsage
	}
	store := agentLogins()
	reply := func(saved bool) int {
		line, _ := json.Marshal(struct {
			Name  string `json:"name"`
			Saved bool   `json:"saved"`
		}{*name, saved})
		fmt.Fprintf(pio.out, "%s\n", line)
		return phoneExitOK
	}
	switch verb {
	case "set":
		pw, err := readPinInput(*file, pio)
		defer zero(pw)
		if err != nil {
			return phoneFail(pio, phoneExitUsage, err.Error(), "give one password: type it at the prompt, or pipe one line in, or use -file")
		}
		if err := store.Save(*name, string(pw)); err != nil {
			return phoneFail(pio, phoneExitToolError, err.Error(), "unlock the Mac's login keychain and run it again")
		}
		return reply(true)
	case "check":
		ok, err := store.Exists(*name)
		if err != nil {
			return phoneFail(pio, phoneExitToolError, err.Error(), "unlock the Mac's login keychain and run it again")
		}
		return reply(ok)
	case "delete":
		if err := store.Delete(*name); err != nil {
			return phoneFail(pio, phoneExitToolError, err.Error(), "unlock the Mac's login keychain and run it again")
		}
		return reply(false)
	}
	return phoneFail(pio, phoneExitUsage, fmt.Sprintf("unknown login command %q", verb), "run `pinkyctl phone login help`")
}
