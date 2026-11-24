package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

var (
	secretBank  = screen.App{ID: "com.example.mybank", Name: "Example Bank"}
	secretVault = screen.App{ID: "sg.ndi.sp", Name: "Singpass"}
	secretShop  = screen.App{ID: "com.example.shop", Name: "Shop"}
	secretPW    = &screen.Element{Role: screen.RoleInput, States: []string{screen.StatePassword}}
	secretOTP   = &screen.Element{Role: screen.RoleInput, Label: "Verification code"}
	secretChat  = &screen.Element{Role: screen.RoleInput, Label: "Message"}
)

func secretEngine(t *testing.T, listed ...string) *Engine {
	t.Helper()
	f := DefaultFile()
	f.CredentialApps = listed
	return New(f, nil)
}

func secretIn(app screen.App, target *screen.Element) Input {
	return Input{Op: OpType, App: app, Target: target, Text: "{{secret:pw}}", Secret: true}
}

// TestSecretVerdict: a reference types only into a password or code field of
// an app the owner listed by exact id; every other case is refused with a
// reason that says what to do, and a literal text into the same field is
// refused as ever.
func TestSecretVerdict(t *testing.T) {
	e := secretEngine(t, secretBank.ID, secretShop.ID)
	for _, c := range []struct {
		name   string
		in     Input
		want   Decision
		reason string
	}{
		{"password, listed", secretIn(secretShop, secretPW), Allow, ""},
		{"code, listed", secretIn(secretShop, secretOTP), Allow, ""},
		{"chat field, listed", secretIn(secretShop, secretChat), Deny, "password or one-time-code field"},
		{"no target, listed", secretIn(secretShop, nil), Deny, "password or one-time-code field"},
		{"password, not listed", secretIn(screen.App{ID: "com.example.other", Name: "Other"}, secretPW), Deny, "credential_apps"},
		{"listed name, other id", secretIn(screen.App{ID: "com.evil.app", Name: "Shop"}, secretPW), Deny, "credential_apps"},
		{"listed id, other name", secretIn(screen.App{ID: secretShop.ID, Name: "Whatever"}, secretPW), Allow, ""},
		{"no id", secretIn(screen.App{Name: "Shop"}, secretPW), Deny, "credential_apps"},
		{"literal text, listed", Input{Op: OpType, App: secretShop, Target: secretPW, Text: "hunter2"}, Deny, "never types into password"},
		{"literal code, listed", Input{Op: OpType, App: secretShop, Target: secretOTP, Text: "123456"}, Deny, "never types into password"},
	} {
		v := e.Evaluate(c.in)
		if v.Decision != c.want {
			t.Errorf("%s: decision %v, want %v (%s)", c.name, v.Decision, c.want, v.Reason)
		}
		if c.want == Deny && (v.Rule != RuleCredentials || !strings.Contains(v.Reason, c.reason)) {
			t.Errorf("%s: rule %q reason %q", c.name, v.Rule, v.Reason)
		}
	}
	if v := e.Evaluate(secretIn(secretShop, secretChat)); !strings.Contains(v.Reason, "fix:") {
		t.Errorf("a refusal has no fix line: %q", v.Reason)
	}
	if v := e.Evaluate(secretIn(screen.App{ID: "com.example.other"}, secretPW)); !strings.Contains(v.Reason, "com.example.other") {
		t.Errorf("an unlisted app's refusal does not name its id: %q", v.Reason)
	}
}

// TestSecretLift: a listed finance app or password manager is not blocked
// and its payments still ask; an unlisted one stays blocked, and the
// owner's own block, read-only mode and the agent's scope still win.
func TestSecretLift(t *testing.T) {
	e := secretEngine(t, secretBank.ID, secretVault.ID)
	if _, ok := Default().Blocked(secretBank); !ok {
		t.Fatal("the bank is not blocked by default, the test proves nothing")
	}
	for _, a := range []screen.App{secretBank, secretVault} {
		if rule, ok := e.Blocked(a); ok {
			t.Errorf("%s is blocked by %q though listed", a.ID, rule)
		}
		if v := e.Evaluate(secretIn(a, secretPW)); v.Decision != Allow {
			t.Errorf("%s: %v %q", a.ID, v.Decision, v.Reason)
		}
	}
	if rule, ok := e.Blocked(screen.App{ID: "com.example.otherbank", Name: "Other Bank"}); !ok || rule != RuleFinance {
		t.Errorf("an unlisted bank: blocked %v by %q", ok, rule)
	}

	pay := Input{Op: OpTap, App: secretBank, Target: &screen.Element{Role: screen.RoleButton, Label: "Pay now"}}
	if v := e.Evaluate(pay); v.Decision != Ask || !v.Has(Payment) {
		t.Errorf("a payment in a listed bank: %v %q %v", v.Decision, v.Rule, v.Kinds)
	}

	mine := secretEngine(t, secretBank.ID)
	mine.AddApps(CatBlocked, CategoryEntries{IDs: []string{secretBank.ID}})
	if rule, ok := mine.Blocked(secretBank); !ok || rule != RuleBlocked {
		t.Errorf("an app the owner blocked is lifted: blocked %v by %q", ok, rule)
	}
	if v := mine.Evaluate(secretIn(secretBank, secretPW)); v.Decision != Deny {
		t.Errorf("an app the owner blocked accepts a secret: %v", v.Decision)
	}

	scoped := secretIn(secretBank, secretPW)
	scoped.Scoped, scoped.ScopeApps = true, []string{"com.example.other"}
	if v := e.Evaluate(scoped); v.Decision != Deny || !v.Scope {
		t.Errorf("the agent's scope was lifted: %+v", v)
	}
	ro := secretIn(secretBank, secretPW)
	ro.ReadOnly = true
	if v := e.Evaluate(ro); v.Decision != Deny || v.Rule != RuleReadOnly {
		t.Errorf("read-only was lifted: %+v", v)
	}
}

// TestCredentialAppsFile: the list is read from rules.yaml, and Settings
// saving the file keeps it.
func TestCredentialAppsFile(t *testing.T) {
	dir := t.TempDir()
	rules, apps := filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml")
	edited := strings.Replace(string(DefaultRulesYAML()), "credential_apps: []", "credential_apps:\n  - com.example.shop", 1)
	if edited == string(DefaultRulesYAML()) {
		t.Fatal("the bundled rules file has no credential_apps line to edit")
	}
	if err := os.WriteFile(rules, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	if !e.SecretApp("com.example.shop") || e.SecretApp("com.example.other") || e.SecretApp("") {
		t.Fatal("the list was not read")
	}
	if err := e.SetVirtualCredentials(true); err != nil {
		t.Fatal(err)
	}
	e2, err := Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	if !e2.SecretApp("com.example.shop") {
		t.Fatal("saving the file dropped credential_apps")
	}
}
