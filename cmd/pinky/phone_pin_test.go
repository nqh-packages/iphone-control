package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/secret"
)

const pinSentinel = "S3NT1NEL-7f2a"

type pinEnv struct {
	store *secret.MemoryPINStore
	devs  []phonePinDevice
}

func newPinEnv(t *testing.T, devs ...phonePinDevice) *pinEnv {
	t.Helper()
	if len(devs) == 0 {
		devs = []phonePinDevice{{ID: "se-id", Name: "iPhone SE"}}
	}
	e := &pinEnv{store: &secret.MemoryPINStore{}, devs: devs}
	pins := secret.NewAgentPINsOn(e.store)
	oldPINs, oldDevs := phonePINs, phonePinDevices
	phonePINs = func() *secret.AgentPINs { return pins }
	phonePinDevices = func() ([]phonePinDevice, error) { return e.devs, nil }
	t.Cleanup(func() { phonePINs, phonePinDevices = oldPINs, oldDevs })
	return e
}

func (e *pinEnv) run(stdin string, args ...string) (code int, out, errOut string) {
	var o, er bytes.Buffer
	code = phoneMain(append([]string{"pin"}, args...), phoneIO{in: strings.NewReader(stdin), out: &o, err: &er})
	return code, o.String(), er.String()
}

func (e *pinEnv) saved(t *testing.T, id string) string {
	t.Helper()
	pin, err := secret.NewAgentPINsOn(e.store).PIN(context.Background(), id)
	if err != nil {
		return ""
	}
	return pin
}

func TestPhonePinSetCheckDelete(t *testing.T) {
	e := newPinEnv(t)
	if code, out, _ := e.run("", "check"); code != 0 || strings.TrimSpace(out) != `{"device":"se-id","saved":false,"refused":false}` {
		t.Fatalf("check before set: %d %q", code, out)
	}
	if code, out, errOut := e.run(pinSentinel+"\n", "set"); code != 0 || strings.TrimSpace(out) != `{"device":"se-id","saved":true}` {
		t.Fatalf("set: %d %q %q", code, out, errOut)
	}
	if got := e.saved(t, "se-id"); got != pinSentinel {
		t.Fatalf("the store holds %q", got)
	}
	if code, out, _ := e.run("", "check"); code != 0 || strings.TrimSpace(out) != `{"device":"se-id","saved":true,"refused":false}` {
		t.Fatalf("check after set: %d %q", code, out)
	}
	if code, out, _ := e.run("", "delete"); code != 0 || strings.TrimSpace(out) != `{"device":"se-id","saved":false}` {
		t.Fatalf("delete: %d %q", code, out)
	}
	if code, out, _ := e.run("", "check", "-device", "iPhone SE"); code != 0 || !strings.Contains(out, `"saved":false`) {
		t.Fatalf("check after delete, by name: %d %q", code, out)
	}
}

func TestPhonePinCheckSaysWhenThePhoneRefusedThePIN(t *testing.T) {
	e := newPinEnv(t)
	e.run("4711\n", "set")
	pins := secret.NewAgentPINsOn(e.store)
	pins.Refused("se-id")
	if _, out, _ := e.run("", "check"); strings.TrimSpace(out) != `{"device":"se-id","saved":true,"refused":true}` {
		t.Fatalf("check: %q", out)
	}
	e.run("4711\n", "set")
	if _, out, _ := e.run("", "check"); !strings.Contains(out, `"refused":false`) {
		t.Fatalf("set did not clear the refusal: %q", out)
	}
}

func TestPhonePinSavesNothingFromBadInput(t *testing.T) {
	for name, in := range map[string]string{
		"empty": "", "blank line": "\n", "two lines": "4711\n4711\n", "space": "47 11\n", "short": "123\n",
		"non ascii": "47é1\n", "long": strings.Repeat("1", 65) + "\n",
	} {
		e := newPinEnv(t)
		code, out, errOut := e.run(in, "set")
		if code != phoneExitUsage || !strings.Contains(errOut, "fix:") || out != "" {
			t.Errorf("%s: %d %q %q", name, code, out, errOut)
		}
		if st, _ := secret.NewAgentPINsOn(e.store).State("se-id"); st.Saved {
			t.Errorf("%s: a passcode was saved", name)
		}
	}
}

func TestPhonePinNeverPrintsThePIN(t *testing.T) {
	e := newPinEnv(t)
	attempts := [][]string{
		{"set", pinSentinel}, {"set", "-" + pinSentinel}, {"set", "-device", "se-id", pinSentinel},
		{"check", pinSentinel}, {"delete", "-file", pinSentinel}, {"get"}, {"show"}, {"set", "-file", "/nonexistent/" + pinSentinel},
	}
	for _, args := range attempts {
		code, out, errOut := e.run(pinSentinel+"\n", args...)
		if code == 0 {
			t.Errorf("%v succeeded", args)
		}
		if strings.Contains(out+errOut, pinSentinel) {
			t.Errorf("%v printed the passcode: %q %q", args, out, errOut)
		}
	}
	for _, args := range [][]string{{"set"}, {"check"}, {"delete"}} {
		_, out, errOut := e.run(pinSentinel+"\n", args...)
		if strings.Contains(out+errOut, pinSentinel) {
			t.Errorf("%v printed the passcode: %q %q", args, out, errOut)
		}
	}
	e.run(pinSentinel+"\n", "set")
	e.store.ReadErr = secret.ErrKeychainDenied
	e.store.ExistsErr = secret.ErrKeychainLocked
	for _, args := range [][]string{{"set"}, {"check"}, {"delete"}} {
		_, out, errOut := e.run(pinSentinel+"\n", args...)
		if strings.Contains(out+errOut, pinSentinel) {
			t.Errorf("%v with a failing keychain printed the passcode: %q %q", args, out, errOut)
		}
	}
	if got := e.saved(t, "se-id"); got != "" {
		t.Errorf("a failing store still answers: %q", got)
	}
}

func TestPhonePinHasNoWayToShowIt(t *testing.T) {
	e := newPinEnv(t)
	e.run("4711\n", "set")
	for _, verb := range []string{"get", "show", "reveal", "print"} {
		if code, out, _ := e.run("", verb); code != phoneExitUsage || out != "" {
			t.Errorf("pin %s: %d %q", verb, code, out)
		}
	}
}

func TestPhonePinFileIsZeroedAndDeleted(t *testing.T) {
	for name, content := range map[string]string{"good": pinSentinel + "\n", "bad": "47 11\n4711\n"} {
		e := newPinEnv(t)
		dir := t.TempDir()
		path, link := filepath.Join(dir, "delivered"), filepath.Join(dir, "other-name-for-it")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(path, link); err != nil {
			t.Fatal(err)
		}
		code, _, _ := e.run("", "set", "-file", path)
		if (code == 0) != (name == "good") {
			t.Fatalf("%s: exit %d", name, code)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s: the file is still there: %v", name, err)
		}
		left, err := os.ReadFile(link)
		if err != nil || len(left) != len(content) || len(bytes.Trim(left, "\x00")) != 0 {
			t.Errorf("%s: the file's bytes were not zeroed: %q, %v", name, left, err)
		}
		if got := e.saved(t, "se-id"); (got == pinSentinel) != (name == "good") {
			t.Errorf("%s: saved %q", name, got)
		}
	}
}

func TestPhonePinFileThatIsNotRegularIsLeftAlone(t *testing.T) {
	e := newPinEnv(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("keep me\n"), 0o600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.run("", "set", "-file", link); code != phoneExitUsage {
		t.Fatalf("exit %d", code)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep me\n" {
		t.Fatalf("the symlink's target changed: %q, %v", b, err)
	}
}

func TestPhonePinDeviceMustBeOneTheGatewayLists(t *testing.T) {
	e := newPinEnv(t, phonePinDevice{ID: "se-id", Name: "iPhone SE"}, phonePinDevice{ID: "pixel-id", Name: "Pixel"})
	if code, _, errOut := e.run("4711\n", "set"); code != phoneExitUsage || !strings.Contains(errOut, "se-id, pixel-id") || !strings.Contains(errOut, "fix:") {
		t.Fatalf("two phones, none named: %d %q", code, errOut)
	}
	if code, _, errOut := e.run("4711\n", "set", "-device", "typo-id"); code != phoneExitUsage || !strings.Contains(errOut, "fix:") {
		t.Fatalf("an id the gateway does not list: %d %q", code, errOut)
	}
	if st, _ := secret.NewAgentPINsOn(e.store).State("typo-id"); st.Saved {
		t.Fatal("a passcode was saved under an id the gateway does not list")
	}
	if code, out, _ := e.run("4711\n", "set", "-device", "pixel-id"); code != 0 || !strings.Contains(out, `"pixel-id"`) {
		t.Fatalf("a listed id: %d %q", code, out)
	}
}

func TestPhonePinWithNoPhoneKnownSaysHowToFixIt(t *testing.T) {
	e := newPinEnv(t)
	e.devs = nil
	code, _, errOut := e.run("4711\n", "set")
	if code != phoneExitUsage || !strings.Contains(errOut, "PINKY_ALLOW_DEVICES") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestPhonePinKeychainFailuresEachHaveTheirOwnFix(t *testing.T) {
	fixes := map[string]bool{}
	for _, err := range []error{secret.ErrKeychainLocked, secret.ErrKeychainDenied} {
		e := newPinEnv(t)
		e.store.ExistsErr = err
		code, _, errOut := e.run("", "check")
		_, fix, ok := strings.Cut(errOut, "fix: ")
		if code != phoneExitToolError || !ok {
			t.Fatalf("%v: %d %q", err, code, errOut)
		}
		fixes[fix] = true
	}
	if len(fixes) != 2 {
		t.Fatalf("the two failures share a fix: %v", fixes)
	}
}

func TestPhonePinFileTooLongForAPassCodeIsRefusedAndKept(t *testing.T) {
	e := newPinEnv(t)
	path := filepath.Join(t.TempDir(), "big")
	content := bytes.Repeat([]byte("1"), 1<<20)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := e.run("", "set", "-file", path)
	if code != phoneExitUsage || out != "" || !strings.Contains(errOut, "too long") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, content) {
		t.Fatalf("an oversized file was changed or deleted: %d bytes, %v", len(b), err)
	}
}

func TestPhonePinFileAtTheLimitIsRead(t *testing.T) {
	e := newPinEnv(t)
	path := filepath.Join(t.TempDir(), "edge")
	pin := strings.Repeat("a", 64)
	os.WriteFile(path, []byte(pin+"\r\n"), 0o600)
	if code, _, errOut := e.run("", "set", "-file", path); code != 0 {
		t.Fatalf("%d %q", code, errOut)
	}
	if got := e.saved(t, "se-id"); got != pin {
		t.Fatalf("saved %q", got)
	}
}

func TestPhonePinSymlinkIsNotFollowedAndStays(t *testing.T) {
	e := newPinEnv(t)
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target"), filepath.Join(dir, "link")
	os.WriteFile(target, []byte(pinSentinel+"\n"), 0o600)
	os.Symlink(target, link)
	if code, _, _ := e.run("", "set", "-file", link); code != phoneExitUsage {
		t.Fatalf("exit %d", code)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != pinSentinel+"\n" {
		t.Fatalf("the target changed: %q, %v", b, err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("the symlink was deleted: %v", err)
	}
	if got := e.saved(t, "se-id"); got != "" {
		t.Fatalf("a passcode was read through a symlink: %q", got)
	}
}

func TestScrubFileLeavesAPathSwappedForAnotherFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "delivered")
	os.WriteFile(path, []byte(pinSentinel+"\n"), 0o600)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	moved := filepath.Join(dir, "moved")
	os.Rename(path, moved)
	os.WriteFile(path, []byte("someone else's file\n"), 0o600)
	if err := scrubFile(f, fi, path); err == nil {
		t.Fatal("scrubFile deleted a path that no longer names the opened file")
	}
	if b, _ := os.ReadFile(path); string(b) != "someone else's file\n" {
		t.Fatalf("the swapped-in file changed: %q", b)
	}
	if b, _ := os.ReadFile(moved); len(bytes.Trim(b, "\x00")) != 0 {
		t.Fatalf("the opened file was not zeroed: %q", b)
	}
}
