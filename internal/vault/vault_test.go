package vault

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

const sentinel = "S3NT1NEL-7f2a"

var bank = screen.App{ID: "com.example.bank", Name: "Bank"}

func newTestVault(listed ...string) (*Vault, *time.Time) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	v := New(func(id string) bool {
		for _, l := range listed {
			if l == id {
				return true
			}
		}
		return false
	})
	v.now = func() time.Time { return now }
	return v, &now
}

func TestRef(t *testing.T) {
	for _, c := range []struct {
		text   string
		name   string
		isRef  bool
		failed bool
	}{
		{"{{secret:bank-pw}}", "bank-pw", true, false},
		{"{{secret:a.b_c-1}}", "a.b_c-1", true, false},
		{"hunter2", "", false, false},
		{"{{other:x}}", "", false, false},
		{"user {{secret:pw}}", "", false, true},
		{"{{secret:pw}} ", "", false, true},
		{"{{secret:pw}}{{secret:pw}}", "", false, true},
		{"{{secret:}}", "", false, true},
		{"{{secret:UPPER}}", "", false, true},
		{"{{secret:a b}}", "", false, true},
		{"{{secret:" + strings.Repeat("a", 41) + "}}", "", false, true},
	} {
		name, isRef, err := ParseRef(c.text)
		if name != c.name || isRef != c.isRef || (err != nil) != c.failed {
			t.Errorf("%q: got %q %v %v", c.text, name, isRef, err)
		}
	}
	if n, ok, err := ParseRef(Ref("x1")); n != "x1" || !ok || err != nil {
		t.Errorf("Ref does not round trip: %q %v %v", n, ok, err)
	}
}

func TestVaultTakeOnce(t *testing.T) {
	v, _ := newTestVault(bank.ID)
	if err := v.Put(bank.ID, 0, map[string][]byte{"pw": []byte(sentinel)}); err != nil {
		t.Fatal(err)
	}
	if err := v.Staged("pw", bank); err != nil {
		t.Fatalf("Staged: %v", err)
	}
	if err := v.Staged("pw", bank); err != nil {
		t.Fatalf("Staged used it up: %v", err)
	}
	got, err := v.TakeSecret("pw", bank)
	if err != nil || got != sentinel {
		t.Fatalf("take: %q %v", got, err)
	}
	if _, err := v.TakeSecret("pw", bank); !errors.Is(err, ErrNotStaged) {
		t.Fatalf("second take: %v", err)
	}
	if err := v.Staged("pw", bank); !errors.Is(err, ErrNotStaged) {
		t.Fatalf("Staged after take: %v", err)
	}
}

func TestVaultExpires(t *testing.T) {
	v, now := newTestVault(bank.ID)
	if err := v.Put(bank.ID, 0, map[string][]byte{"pw": []byte(sentinel), "otp": []byte("482913")}); err != nil {
		t.Fatal(err)
	}
	if err := v.Put(bank.ID, time.Hour, map[string][]byte{"long": []byte(sentinel)}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(DefaultTTL - time.Second)
	if err := v.Staged("pw", bank); err != nil {
		t.Fatalf("expired early: %v", err)
	}
	*now = now.Add(2 * time.Second)
	if err := v.Staged("pw", bank); !errors.Is(err, ErrNotStaged) {
		t.Fatalf("still staged after 120 s: %v", err)
	}
	*now = now.Add(MaxTTL)
	if err := v.Staged("long", bank); !errors.Is(err, ErrNotStaged) {
		t.Fatalf("a request for an hour was kept past %v: %v", MaxTTL, err)
	}
}

func TestVaultBoundToOneApp(t *testing.T) {
	other := screen.App{ID: "com.example.shop", Name: "Shop"}
	v, _ := newTestVault(bank.ID, other.ID)
	if err := v.Put(bank.ID, 0, map[string][]byte{"pw": []byte(sentinel)}); err != nil {
		t.Fatal(err)
	}
	for _, app := range []screen.App{other, {Name: "Bank"}, {ID: "com.example.bank.evil", Name: "Bank"}} {
		_, err := v.TakeSecret("pw", app)
		if err == nil || strings.Contains(err.Error(), sentinel) {
			t.Fatalf("%+v: %v", app, err)
		}
	}
	if _, err := v.TakeSecret("pw", other); !errors.Is(err, ErrWrongApp) {
		t.Fatalf("another listed app: %v", err)
	}
	if got, err := v.TakeSecret("pw", bank); err != nil || got != sentinel {
		t.Fatalf("a refused take used the secret up: %q %v", got, err)
	}
}

func TestVaultAllowedIsAskedAtPutAndTake(t *testing.T) {
	list := map[string]bool{bank.ID: true}
	v := New(func(id string) bool { return list[id] })
	if err := v.Put("com.example.unlisted", 0, map[string][]byte{"pw": []byte(sentinel)}); !errors.Is(err, ErrAppNotAllowed) {
		t.Fatalf("put to an unlisted app: %v", err)
	}
	if err := v.Put(bank.ID, 0, map[string][]byte{"pw": []byte(sentinel)}); err != nil {
		t.Fatal(err)
	}
	delete(list, bank.ID)
	if _, err := v.TakeSecret("pw", bank); !errors.Is(err, ErrAppNotAllowed) {
		t.Fatalf("take after the owner removed the app: %v", err)
	}
}

func TestVaultRefusesWhatItCannotMask(t *testing.T) {
	v, _ := newTestVault(bank.ID)
	for _, c := range []struct {
		name string
		val  string
	}{{"pw", ""}, {"pw", "abc"}, {"Bad Name", sentinel}, {"", sentinel}} {
		if err := v.Put(bank.ID, 0, map[string][]byte{c.name: []byte(c.val)}); !errors.Is(err, ErrBadSecret) {
			t.Errorf("%q=%q: %v", c.name, c.val, err)
		}
	}
	for i := 0; i < maxSlots; i++ {
		if err := v.Put(bank.ID, 0, map[string][]byte{"n" + string(rune('a'+i)): []byte(sentinel)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.Put(bank.ID, 0, map[string][]byte{"extra": []byte(sentinel)}); !errors.Is(err, ErrBadSecret) {
		t.Errorf("slot %d: %v", maxSlots+1, err)
	}
	if err := v.Put(bank.ID, 0, map[string][]byte{"na": []byte(sentinel + "2")}); err != nil {
		t.Errorf("replacing a staged name: %v", err)
	}
}

func TestVaultPutCopiesAndTakeWipes(t *testing.T) {
	v, now := newTestVault(bank.ID)
	in := []byte(sentinel)
	if err := v.Put(bank.ID, 0, map[string][]byte{"pw": in}); err != nil {
		t.Fatal(err)
	}
	in[0] = 'X'
	if got, _ := v.TakeSecret("pw", bank); got != sentinel {
		t.Fatalf("the vault shares the caller's bytes: %q", got)
	}
	held := v.typed[0].value
	*now = now.Add(typedFor + time.Second)
	v.Mask(&screen.Screen{})
	if len(v.typed) != 0 {
		t.Fatal("typed value kept past its window")
	}
	for _, b := range held {
		if b != 0 {
			t.Fatal("typed value not wiped on expiry")
		}
	}
}

func TestMask(t *testing.T) {
	v, now := newTestVault(bank.ID)
	if err := v.Put(bank.ID, 0, map[string][]byte{"pw": []byte(sentinel)}); err != nil {
		t.Fatal(err)
	}
	read := func() *screen.Screen {
		return &screen.Screen{
			Elements: []screen.Element{
				{Ref: 1, Role: screen.RoleInput, Label: "Password, " + sentinel, Content: sentinel, ID: "pw-" + sentinel},
				{Role: screen.RoleOther, Label: "Signed in as " + sentinel, Source: screen.SourceOCR},
				{Ref: 2, Role: screen.RoleButton, Label: "Sign in"},
			},
			Events: []screen.Event{{Kind: screen.EventToast, Text: "Bad password " + sentinel}, {Kind: screen.EventToast, Text: "Saved"}},
		}
	}
	if n := v.Mask(read()); n != 0 {
		t.Fatalf("masked %d before anything was typed", n)
	}
	if _, err := v.TakeSecret("pw", bank); err != nil {
		t.Fatal(err)
	}
	orig := read()
	s := *orig
	n := v.Mask(&s)
	if n != 3 {
		t.Fatalf("masked %d, want the field, the OCR line and the toast", n)
	}
	for _, e := range s.Elements {
		if strings.Contains(e.Label, sentinel) || strings.Contains(e.Content, sentinel) {
			t.Errorf("element still shows the value: %+v", e)
		}
	}
	if !s.Elements[0].Masked || !s.Elements[1].Masked || s.Elements[2].Masked {
		t.Errorf("Masked flags: %+v", s.Elements)
	}
	if s.Elements[0].ID != orig.Elements[0].ID {
		t.Error("an identifier was rewritten")
	}
	if strings.Contains(s.Events[0].Text, sentinel) || s.Events[1].Text != "Saved" {
		t.Errorf("events: %+v", s.Events)
	}
	if !strings.Contains(orig.Elements[0].Label, sentinel) {
		t.Error("Mask rewrote the caller's slice in place")
	}
	*now = now.Add(typedFor + time.Second)
	if n := v.Mask(read()); n != 0 {
		t.Errorf("masked %d after the window", n)
	}
}

func socketPair(t *testing.T, v *Vault) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "vault.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- v.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return sock
}

func TestSocketStagesAndNeverEchoes(t *testing.T) {
	v, _ := newTestVault(bank.ID)
	sock := socketPair(t, v)
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", fi, err)
	}
	got, err := StageSecret(context.Background(), sock, bank.ID, 30*time.Second, []string{"pw", "otp"},
		strings.NewReader(sentinel+"\n482913\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got.App != bank.ID || len(got.Names) != 2 || got.ExpiresInS != 30 {
		t.Fatalf("reply: %+v", got)
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), sentinel) || strings.Contains(string(b), "482913") {
		t.Fatalf("the reply holds a value: %s", b)
	}
	if val, err := v.TakeSecret("pw", bank); err != nil || val != sentinel {
		t.Fatalf("staged value: %q %v", val, err)
	}
	if val, err := v.TakeSecret("otp", bank); err != nil || val != "482913" {
		t.Fatalf("staged value: %q %v", val, err)
	}
}

func TestSocketRefusals(t *testing.T) {
	v, _ := newTestVault(bank.ID)
	sock := socketPair(t, v)
	ctx := context.Background()
	_, err := StageSecret(ctx, sock, "com.example.unlisted", 0, []string{"pw"}, strings.NewReader(sentinel+"\n"))
	var se *StageError
	if !errors.As(err, &se) || !strings.Contains(se.Fix, "credential_apps") || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("unlisted app: %v", err)
	}
	for _, c := range []struct {
		name  string
		names []string
		input string
	}{
		{"missing line", []string{"pw", "otp"}, sentinel + "\n"},
		{"empty line", []string{"pw"}, "\n"},
		{"no input", []string{"pw"}, ""},
		{"long line", []string{"pw"}, strings.Repeat("a", valueMax+1) + "\n"},
	} {
		if _, err := StageSecret(ctx, sock, bank.ID, 0, c.names, strings.NewReader(c.input)); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	if _, err := StageSecret(ctx, sock, bank.ID, 0, []string{"Bad Name"}, strings.NewReader(sentinel+"\n")); err == nil {
		t.Error("a bad name was accepted")
	}
	if err := v.Staged("pw", bank); !errors.Is(err, ErrNotStaged) {
		t.Fatalf("a refused request left a secret staged: %v", err)
	}
	if _, err := StageSecret(ctx, filepath.Join(filepath.Dir(sock), "none.sock"), bank.ID, 0, []string{"pw"}, strings.NewReader(sentinel+"\n")); err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("no daemon: %v", err)
	}
}

func TestSocketNeedsAPrivateFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(filepath.Join(dir, "v.sock")); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("an open folder was accepted: %v", err)
	}
}
