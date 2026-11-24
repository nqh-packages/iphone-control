package policy

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	GuardPayments      = "payments"
	GuardErase         = "erase"
	GuardDeleteAccount = "delete_account"
	GuardSignOut       = "sign_out"
	GuardPasscodeOff   = "passcode_off"
)

const (
	GuardAllow  = "allow"
	GuardRefuse = "refuse"
	GuardAsk    = "ask"
)

const (
	RuleEraseDevice    = "Erase this iPhone"
	RuleDeleteAccount  = "Delete Account"
	RuleSignOut        = "Sign Out"
	RulePasscode       = "Passcode"
	RulePayments       = "Payments"
	settingsApp        = "com.apple.Preferences"
	guardRefusalFormat = "Refused by the %q gate. The owner can allow it with guards: %s: allow in rules.yaml."
)

type gate struct {
	key    string
	name   string
	labels []string
}

var settingsGates = []gate{
	{GuardErase, RuleEraseDevice, []string{"Erase All Content and Settings", "Erase iPhone"}},
	{GuardDeleteAccount, RuleDeleteAccount, []string{"Delete Account"}},
	{GuardSignOut, RuleSignOut, []string{"Sign Out"}},
	{GuardPasscodeOff, RulePasscode, []string{"Turn Passcode Off"}},
}

var guardKeys = []string{GuardPayments, GuardErase, GuardDeleteAccount, GuardSignOut, GuardPasscodeOff}

func checkGuards(g map[string]string) error {
	for k, v := range g {
		if !contains(guardKeys, k) {
			return fmt.Errorf("guards: unknown key %q (want one of %s)", k, strings.Join(guardKeys, ", "))
		}
		switch v {
		case GuardAllow, GuardRefuse, GuardAsk:
		default:
			return fmt.Errorf("guards: %s: unknown value %q (want allow, refuse or ask)", k, v)
		}
	}
	return nil
}

func guardRule(r Rule, key string, g map[string]string, freeOK bool) (Rule, bool) {
	switch g[key] {
	case GuardAllow:
		return r, false
	case GuardAsk:
		r.Decision = Ask
	case GuardRefuse:
		r.Decision = Deny
		r.Guard = key
	default:
		r.Decision = Deny
		r.Guard = key
		r.FreeOK = freeOK
	}
	return r, true
}

func settingsGateRules(g map[string]string) []Rule {
	var out []Rule
	for _, gt := range settingsGates {
		base := Rule{Name: gt.name, Match: Match{App: StringList{settingsApp}, Label: StringList(gt.labels)}}
		if r, ok := guardRule(base, gt.key, g, false); ok {
			out = append(out, r)
		}
	}
	return out
}

func paymentsRule(g map[string]string) (Rule, bool) {
	base := Rule{Name: RulePayments, Match: Match{Action: StringList{string(Payment)}}}
	return guardRule(base, GuardPayments, g, true)
}

func guardRefusal(key string) string {
	return fmt.Sprintf(guardRefusalFormat, gateName(key), key)
}

func gateName(key string) string {
	if key == GuardPayments {
		return RulePayments
	}
	for _, gt := range settingsGates {
		if gt.key == key {
			return gt.name
		}
	}
	return key
}

var freeLabels = words("get", "free", "install", "download")

var amountRe = regexp.MustCompile(`\d[\d.,]*`)

func nonzeroAmount(s string) bool {
	if !hasCurrency(s) {
		return false
	}
	for _, n := range amountRe.FindAllString(s, -1) {
		if strings.Trim(n, "0.,") != "" {
			return true
		}
	}
	return false
}

func zeroAmount(s string) bool {
	return hasCurrency(s) && !nonzeroAmount(s)
}

func freeItem(in Input) bool {
	t := in.Target
	if t == nil {
		return false
	}
	if nonzeroAmount(t.Label) {
		return false
	}
	if in.Screen != nil {
		for _, e := range in.Screen.Elements {
			if nonzeroAmount(e.Label) {
				return false
			}
		}
	}
	ws := labelWords(t.Label)
	return zeroAmount(t.Label) || (len(ws) == 1 && freeLabels[ws[0]])
}
