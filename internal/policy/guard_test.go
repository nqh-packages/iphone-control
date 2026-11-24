package policy

import (
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

var (
	settingsPhone = screen.App{Name: "Settings", ID: "com.apple.Preferences"}
	chatApp       = screen.App{Name: "Chatter", ID: "com.example.chatter"}
)

var (
	storeApp   = screen.App{Name: "App Store", ID: "com.apple.AppStore"}
	gateLabels = []struct{ key, label, rule string }{
		{GuardErase, "Erase All Content and Settings", RuleEraseDevice},
		{GuardErase, "Erase iPhone", RuleEraseDevice},
		{GuardSignOut, "Sign Out", RuleSignOut},
		{GuardSignOut, "sign out", RuleSignOut},
		{GuardDeleteAccount, "Delete Account", RuleDeleteAccount},
		{GuardPasscodeOff, "Turn Passcode Off", RulePasscode},
	}
)

func guardedEngine(t testing.TB, preset Preset, guards map[string]string) *Engine {
	t.Helper()
	f := DefaultFile()
	p := int(preset)
	f.Preset = &p
	f.Guards = guards
	return New(f, nil)
}

func priceScreen(label string, price string) Input {
	in := tapOn(storeApp, *button(label))
	in.Screen.Elements = append(in.Screen.Elements, screen.Element{Role: screen.RoleText, Label: price})
	return in
}

// TestSettingsGuardsRefuseByDefault: with no guards set, each Settings gate
// is an instant denial that names the gate, in every preset but none.
func TestSettingsGuardsRefuseByDefault(t *testing.T) {
	for _, c := range gateLabels {
		for _, e := range []*Engine{Default(), guardedEngine(t, PresetCommits, nil)} {
			v := e.Evaluate(tapOn(settingsPhone, *button(c.label)))
			if v.Decision != Deny || v.Rule != c.rule || len(v.Asks) != 0 {
				t.Errorf("%q, preset %d: %v %q asks %v, want deny %q", c.label, e.Preset(), v.Decision, v.Rule, v.Asks, c.rule)
			}
		}
	}
}

// TestGuardRefusalHasFixLine: the message names the gate and says how the
// owner allows it.
func TestGuardRefusalHasFixLine(t *testing.T) {
	for _, c := range gateLabels {
		v := Default().Evaluate(tapOn(settingsPhone, *button(c.label)))
		want := "the owner can allow it with guards: " + c.key + ": allow in rules.yaml"
		if !strings.Contains(strings.ToLower(v.Reason), want) || !strings.Contains(v.Reason, c.rule) {
			t.Errorf("%q: %q", c.label, v.Reason)
		}
	}
	v := Default().Evaluate(tapOn(dex, *button("Buy")))
	if v.Decision != Deny || !strings.Contains(v.Reason, "guards: payments: allow in rules.yaml") {
		t.Errorf("payment: %+v", v)
	}
}

// TestGuardOverridePerKey: the owner's value for one key changes that gate
// alone.
func TestGuardOverridePerKey(t *testing.T) {
	for _, c := range gateLabels {
		for value, want := range map[string]Decision{GuardAllow: Allow, GuardAsk: Ask, GuardRefuse: Deny} {
			e := guardedEngine(t, PresetPayments, map[string]string{c.key: value})
			v := e.Evaluate(tapOn(settingsPhone, *button(c.label)))
			if v.Decision != want {
				t.Errorf("%s: %s: %v, want %v", c.key, value, v.Decision, want)
			}
			for _, o := range gateLabels {
				if o.key == c.key {
					continue
				}
				if v := e.Evaluate(tapOn(settingsPhone, *button(o.label))); v.Decision != Deny {
					t.Errorf("%s: %s moved %q to %v", c.key, value, o.label, v.Decision)
				}
			}
		}
	}
	for value, want := range map[string]Decision{GuardAllow: Allow, GuardAsk: Ask, GuardRefuse: Deny} {
		e := guardedEngine(t, PresetPayments, map[string]string{GuardPayments: value})
		if v := e.Evaluate(tapOn(dex, *button("Buy"))); v.Decision != want {
			t.Errorf("payments: %s: %v, want %v", value, v.Decision, want)
		}
		if v := e.Evaluate(priceScreen("Get", "$0.99")); v.Decision != want {
			t.Errorf("priced Get, payments: %s: %v, want %v", value, v.Decision, want)
		}
	}
}

// TestPaymentsFreeAllowedPricedRefused: a free Get passes by default, a
// nonzero price is refused, and an unclear screen stays refused.
func TestPaymentsFreeAllowedPricedRefused(t *testing.T) {
	e := Default()
	for _, c := range []struct {
		name string
		in   Input
		want Decision
	}{
		{"free Get", tapOn(storeApp, *button("Get")), Allow},
		{"Get with a zero price", priceScreen("Get", "$0.00"), Allow},
		{"zero button", tapOn(storeApp, *button("$0.00")), Allow},
		{"Get with a price", priceScreen("Get", "$4.99"), Deny},
		{"price button", tapOn(storeApp, *button("$4.99")), Deny},
		{"price button, free elsewhere", priceScreen("$4.99", "$0.00"), Deny},
		{"Buy", tapOn(dex, *button("Buy")), Deny},
		{"unclear button", priceScreen("Continue", "$0.00"), Deny},
	} {
		v := e.Evaluate(c.in)
		if v.Decision != c.want || len(v.Asks) != 0 {
			t.Errorf("%s: %v %q asks %v, want %v", c.name, v.Decision, v.Rule, v.Asks, c.want)
		}
	}
	refused := guardedEngine(t, PresetPayments, map[string]string{GuardPayments: GuardRefuse})
	if v := refused.Evaluate(priceScreen("Get", "$0.00")); v.Decision != Deny {
		t.Errorf("explicit refuse, free Get: %v", v.Decision)
	}
}

// TestSettingsGuardsStayInSettings: the same words in another app, and other
// actions, allow under the default preset.
func TestSettingsGuardsStayInSettings(t *testing.T) {
	e := Default()
	for _, l := range []string{"Sign Out", "Delete Account", "Erase iPhone", "Turn Passcode Off"} {
		if v := e.Evaluate(tapOn(chatApp, *button(l))); v.Decision != Allow {
			t.Errorf("%q in a chat app: %+v", l, v)
		}
	}
	for _, l := range []string{"Delete", "Send", "Wi-Fi", "Turn Passcode On", "Change Passcode", "Transfer or Reset iPhone"} {
		if v := e.Evaluate(tapOn(settingsPhone, *button(l))); v.Decision != Allow {
			t.Errorf("%q in Settings: %+v", l, v)
		}
	}
	if v := e.Evaluate(tapOn(chatApp, *button("Send"))); v.Decision != Allow {
		t.Errorf("sending a message: %+v", v)
	}
	if v := e.Evaluate(tapOn(chatApp, *button("Delete"))); v.Decision != Allow {
		t.Errorf("deleting a note: %+v", v)
	}
}

// TestSettingsGuardsKeepOtherRules: payments still ask, and the guards are
// absent with no preset.
func TestSettingsGuardsKeepOtherRules(t *testing.T) {
	if v := Default().Evaluate(tapOn(dex, *button("Buy"))); v.Decision != Deny || v.Rule != "Payments" {
		t.Errorf("payment: %+v", v)
	}
	none := Default()
	if err := none.SetPreset(PresetNone); err != nil {
		t.Fatal(err)
	}
	if v := none.Evaluate(tapOn(settingsPhone, *button("Erase iPhone"))); v.Decision != Allow {
		t.Errorf("no preset: %+v", v)
	}
}

// TestSettingsGuardsReachExistingFiles: a rules file an earlier version wrote,
// untouched or edited by hand, refuses the guarded Settings actions too,
// and the hand edit stays on disk as it was.
func TestSettingsGuardsReachExistingFiles(t *testing.T) {
	old, _ := oldDefaults.ReadFile("olddefaults/2026.1107.2.yaml")
	edited := []byte(string(lf(old)) + "\n# mine\n")
	for name, data := range map[string][]byte{"untouched": old, "hand edited": edited, "new install": nil} {
		e, after := loadFile(t, data)
		if name == "hand edited" && string(after) != string(edited) {
			t.Errorf("%s: file rewritten", name)
		}
		if v := e.Evaluate(tapOn(settingsPhone, *button("Erase iPhone"))); v.Decision != Deny || v.Rule != RuleEraseDevice {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

func askAll() map[string]string {
	g := map[string]string{}
	for _, k := range guardKeys {
		g[k] = GuardAsk
	}
	return g
}

func askingDefault() *Engine {
	f := DefaultFile()
	f.Guards = askAll()
	return New(f, nil)
}

func TestGuardsFileKeysAreChecked(t *testing.T) {
	f, err := ParseFile([]byte("guards:\n  erase: allow\n  payments: ask\nrules: []\n"))
	if err != nil || f.Guards[GuardErase] != GuardAllow {
		t.Fatalf("valid guards: %+v, %v", f.Guards, err)
	}
	for _, bad := range []string{"guards: {wipe: allow}\n", "guards: {erase: maybe}\n"} {
		if _, err := ParseFile([]byte(bad + "rules: []\n")); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
