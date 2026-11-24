package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/vault"
)

const secretSentinel = "S3NT1NEL-7f2a"

type secretEnv struct {
	v    *vault.Vault
	sock string
}

func newSecretEnv(t *testing.T, listed ...string) *secretEnv {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ps")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	v := vault.New(func(id string) bool {
		for _, l := range listed {
			if l == id {
				return true
			}
		}
		return false
	})
	sock := filepath.Join(dir, "vault.sock")
	ln, err := vault.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- v.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); <-done })
	old := phoneVaultSocket
	phoneVaultSocket = func() (string, error) { return sock, nil }
	t.Cleanup(func() { phoneVaultSocket = old })
	return &secretEnv{v: v, sock: sock}
}

func (e *secretEnv) run(stdin string, args ...string) (code int, out, errOut string) {
	var o, er bytes.Buffer
	code = phoneMain(append([]string{"secret"}, args...), phoneIO{in: strings.NewReader(stdin), out: &o, err: &er})
	return code, o.String(), er.String()
}

func TestPhoneSecretPutStagesWithoutPrintingIt(t *testing.T) {
	e := newSecretEnv(t, "com.example.bank")
	code, out, errOut := e.run(secretSentinel+"\n", "put", "-app", "com.example.bank", "bank-pw")
	if code != 0 || strings.TrimSpace(out) != `{"staged":["bank-pw"],"app":"com.example.bank","expires_in_s":120}` {
		t.Fatalf("put: %d %q %q", code, out, errOut)
	}
	if strings.Contains(out+errOut, secretSentinel) {
		t.Fatalf("the value was printed: %q %q", out, errOut)
	}
	got, err := e.v.TakeSecret("bank-pw", screen.App{ID: "com.example.bank"})
	if err != nil || got != secretSentinel {
		t.Fatalf("staged: %q %v", got, err)
	}
	code, out, _ = e.run("482913\n", "put", "-app", "com.example.bank", "-ttl", "25s", "bank-otp")
	if code != 0 || !strings.Contains(out, `"expires_in_s":25`) {
		t.Fatalf("ttl: %d %q", code, out)
	}
}

func TestPhoneSecretPutRefusals(t *testing.T) {
	e := newSecretEnv(t, "com.example.bank")
	for _, c := range []struct {
		name  string
		stdin string
		args  []string
		code  int
		fix   string
	}{
		{"app not listed", secretSentinel + "\n", []string{"put", "-app", "com.example.other", "pw"}, 1, "credential_apps"},
		{"no -app", secretSentinel + "\n", []string{"put", "pw"}, 2, "help"},
		{"no name", secretSentinel + "\n", []string{"put", "-app", "com.example.bank"}, 2, "help"},
		{"bad name", secretSentinel + "\n", []string{"put", "-app", "com.example.bank", "Bad Name"}, 2, "a-z"},
		{"ttl too long", secretSentinel + "\n", []string{"put", "-app", "com.example.bank", "-ttl", "1h", "pw"}, 2, "-ttl"},
		{"one line for two names", secretSentinel + "\n", []string{"put", "-app", "com.example.bank", "a", "b"}, 2, "one line per NAME"},
		{"empty stdin", "", []string{"put", "-app", "com.example.bank", "pw"}, 2, "one line per NAME"},
		{"value as a flag", "", []string{"put", "-app", "com.example.bank", "-value", secretSentinel, "pw"}, 2, "help"},
		{"unknown verb", "", []string{"show"}, 2, "help"},
	} {
		code, out, errOut := e.run(c.stdin, c.args...)
		if code != c.code || out != "" || !strings.Contains(errOut, "fix: ") || !strings.Contains(errOut, c.fix) || strings.Contains(out+errOut, secretSentinel) {
			t.Errorf("%s: %d out=%q err=%q", c.name, code, out, errOut)
		}
	}
	if err := e.v.Staged("pw", screen.App{ID: "com.example.bank"}); err == nil {
		t.Error("a refused put left a secret staged")
	}
}

func TestPhoneSecretPutWithoutADaemon(t *testing.T) {
	old := phoneVaultSocket
	phoneVaultSocket = func() (string, error) { return filepath.Join(t.TempDir(), "none.sock"), nil }
	t.Cleanup(func() { phoneVaultSocket = old })
	var o, er bytes.Buffer
	code := phoneMain([]string{"secret", "put", "-app", "com.example.bank", "pw"}, phoneIO{in: strings.NewReader(secretSentinel + "\n"), out: &o, err: &er})
	if code != phoneExitNoGateway || !strings.Contains(er.String(), "fix: ") || strings.Contains(o.String()+er.String(), secretSentinel) {
		t.Fatalf("%d %q %q", code, o.String(), er.String())
	}
}

func TestPhoneSecretDaemonVault(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dirs := api.Dirs{Run: dir}
	f := policy.DefaultFile()
	f.CredentialApps = []string{"com.example.bank"}
	v, stop := newSecretVault(policy.New(f, nil), dirs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if v == nil {
		t.Fatal("no vault")
	}
	old := phoneVaultSocket
	phoneVaultSocket = func() (string, error) { return vaultSocketIn(dirs), nil }
	t.Cleanup(func() { phoneVaultSocket = old })

	var o, er bytes.Buffer
	code := phoneMain([]string{"secret", "put", "-app", "com.example.bank", "pw"}, phoneIO{in: strings.NewReader(secretSentinel + "\n"), out: &o, err: &er})
	if code != 0 {
		t.Fatalf("%d %q %q", code, o.String(), er.String())
	}
	if got, err := v.TakeSecret("pw", screen.App{ID: "com.example.bank"}); err != nil || got != secretSentinel {
		t.Fatalf("staged: %q %v", got, err)
	}
	code = phoneMain([]string{"secret", "put", "-app", "com.example.other", "pw"}, phoneIO{in: strings.NewReader(secretSentinel + "\n"), out: &o, err: &er})
	if code != phoneExitToolError || !strings.Contains(er.String(), "credential_apps") {
		t.Fatalf("an app that is not listed: %d %q", code, er.String())
	}
	stop()
	code = phoneMain([]string{"secret", "put", "-app", "com.example.bank", "pw"}, phoneIO{in: strings.NewReader(secretSentinel + "\n"), out: &o, err: &er})
	if code != phoneExitNoGateway {
		t.Fatalf("after stop: %d", code)
	}

	short := filepath.Join(dir, strings.Repeat("x", 120))
	if v, stop := newSecretVault(policy.New(f, nil), api.Dirs{Run: short}, slog.New(slog.NewTextHandler(io.Discard, nil))); v != nil {
		stop()
		t.Fatal("a vault without a socket")
	}
}
