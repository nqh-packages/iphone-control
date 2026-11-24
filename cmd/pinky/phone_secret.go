package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/vault"
)

// pinkyctl phone secret put is what the owner's credential broker runs as
// its child: the broker pipes a secret in on stdin, and put hands it to the
// daemon's in-memory vault over a socket only this user can reach. The
// value is never an argument, an environment variable, a file or output.

func phoneSecretUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pinkyctl phone secret put -app BUNDLE_ID [-ttl DURATION] NAME...

put reads one line from stdin for each NAME (no echo on a terminal) and stages
them for the app with that bundle id, which must be on credential_apps in
rules.yaml. Each is used once, by phone_type with {{secret:NAME}}, within the
ttl (default 120s, at most 300s). It prints {"staged":[...],"app":...,
"expires_in_s":...} and never the values. Run it through the credential
broker: cred run --login ID --reason '...' --stdin '{password}' --
pinkyctl phone secret put -app BUNDLE_ID NAME
`)
}

// phoneVaultSocket is where the daemon's vault listens; tests replace it.
var phoneVaultSocket = func() (string, error) {
	dirs, err := api.DefaultDirs()
	if err != nil {
		return "", err
	}
	return vaultSocketIn(dirs), nil
}

// vaultSocketIn is the vault socket of a gateway's folders: next to its
// own socket, in the same private folder.
func vaultSocketIn(dirs api.Dirs) string { return filepath.Join(dirs.Run, "vault.sock") }

func phoneSecretCmd(args []string, pio phoneIO) int {
	if len(args) == 0 {
		phoneSecretUsage(pio.err)
		return phoneExitUsage
	}
	switch args[0] {
	case "put":
		return phoneSecretPut(args[1:], pio)
	case "help", "-h", "--help", "-help":
		phoneSecretUsage(pio.out)
		return phoneExitOK
	}
	return phoneFail(pio, phoneExitUsage, "unknown secret command", "run `pinkyctl phone secret help`")
}

func phoneSecretPut(args []string, pio phoneIO) int {
	fs := phoneFlags("secret put", pio)
	fs.SetOutput(io.Discard)
	app := fs.String("app", "", "")
	ttl := fs.Duration("ttl", 0, "")
	if err := fs.Parse(args); err != nil || *app == "" || fs.NArg() == 0 {
		return phoneFail(pio, phoneExitUsage, "put needs -app and at least one NAME", "run `pinkyctl phone secret help`")
	}
	if *ttl < 0 || *ttl > vault.MaxTTL {
		return phoneFail(pio, phoneExitUsage, fmt.Sprintf("-ttl must be between 0 and %v", vault.MaxTTL), "leave -ttl out for the default")
	}
	names := fs.Args()
	for _, n := range names {
		if !vault.ValidName(n) {
			return phoneFail(pio, phoneExitUsage, fmt.Sprintf("%q is not a valid secret name", n), "use 1-40 characters from a-z, 0-9, dot, dash and underscore")
		}
	}
	sock, err := phoneVaultSocket()
	if err != nil {
		return phoneFail(pio, phoneExitToolError, err.Error(), "run `pinkyctl phone status`")
	}
	in := pio.in
	if f, ok := in.(*os.File); ok {
		if restore, err := echoOff(int(f.Fd())); err == nil {
			defer restore()
			fmt.Fprint(pio.err, "Secret (not shown): ")
			defer fmt.Fprintln(pio.err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := vault.StageSecret(ctx, sock, *app, *ttl, names, in)
	var se *vault.StageError
	switch {
	case errors.As(err, &se):
		return phoneFail(pio, phoneExitToolError, se.Msg, se.Fix)
	case errors.Is(err, vault.ErrInput):
		return phoneFail(pio, phoneExitUsage, err.Error(), "pipe one line per NAME into stdin")
	case err != nil:
		return phoneFail(pio, phoneExitNoGateway, err.Error(), "check that the daemon runs with the vault: `pinkyctl phone status`")
	}
	line, _ := json.Marshal(got)
	fmt.Fprintf(pio.out, "%s\n", line)
	return phoneExitOK
}
