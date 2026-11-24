package policy

import (
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

var (
	settingsPhone = screen.App{Name: "Settings", ID: "com.apple.Preferences"}
	chatApp       = screen.App{Name: "Chatter", ID: "com.example.chatter"}
)

// TestSettingsGuardsAskByDefault: erasing the phone, leaving or deleting the
// Apple Account and turning the passcode off ask under the default preset,
// each by its own name.
func TestSettingsGuardsAskByDefault(t *testing.T) {
	cases := []struct{ label, rule string }{
		{"Erase All Content and Settings", RuleEraseDevice},
		{"Erase iPhone", RuleEraseDevice},
		{"Sign Out", RuleAppleAccount},
		{"sign out", RuleAppleAccount},
		{"Delete Account", RuleAppleAccount},
		{"Turn Passcode Off", RulePasscode},
	}
	for _, c := range cases {
		for _, e := range []*Engine{Default(), commitsEngine(t)} {
			v := e.Evaluate(tapOn(settingsPhone, *button(c.label)))
			if v.Decision != Ask || v.Rule != c.rule {
				t.Errorf("%q, preset %d: %v %q, want ask %q", c.label, e.Preset(), v.Decision, v.Rule, c.rule)
			}
		}
	}
}

// TestSettingsGuardsStayInSettings: the same words in another app, and other
// actions, ask nothing under the default preset.
func TestSettingsGuardsStayInSettings(t *testing.T) {
	e := Default()
	for _, l := range []string{"Sign Out", "Delete Account", "Erase iPhone", "Turn Passcode Off"} {
		if v := e.Evaluate(tapOn(chatApp, *button(l))); v.Decision != Allow {
			t.Errorf("%q in a chat app: %+v", l, v)
		}
	}
	for _, l := range []string{"Delete", "Send", "Wi-Fi", "Turn Passcode On", "Change Passcode"} {
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
	if v := Default().Evaluate(tapOn(dex, *button("Buy"))); v.Decision != Ask || v.Rule != "Payments" {
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
// untouched or edited by hand, asks about the guarded Settings actions too,
// and the hand edit stays on disk as it was.
func TestSettingsGuardsReachExistingFiles(t *testing.T) {
	old, _ := oldDefaults.ReadFile("olddefaults/2026.1107.2.yaml")
	edited := []byte(string(lf(old)) + "\n# mine\n")
	for name, data := range map[string][]byte{"untouched": old, "hand edited": edited, "new install": nil} {
		e, after := loadFile(t, data)
		if name == "hand edited" && string(after) != string(edited) {
			t.Errorf("%s: file rewritten", name)
		}
		if v := e.Evaluate(tapOn(settingsPhone, *button("Erase iPhone"))); v.Decision != Ask || v.Rule != RuleEraseDevice {
			t.Errorf("%s: %+v", name, v)
		}
	}
}
