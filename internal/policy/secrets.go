package policy

import (
	"fmt"

	"github.com/DIYgod/pinky/internal/screen"
)

// SecretApp reports whether the app's exact id is on the owner's
// credential_apps list. A name never counts: an app can call itself
// anything, but it cannot take another app's id.
func (e *Engine) SecretApp(appID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.secretAppLocked(appID)
}

func (e *Engine) secretAppLocked(appID string) bool {
	return appID != "" && contains(e.file.CredentialApps, appID)
}

// secretVerdictLocked decides the credentials check of an action whose text
// is a secret reference. It reports done when the verdict is a refusal;
// otherwise the rest of Evaluate goes on with the check satisfied.
func (e *Engine) secretVerdictLocked(in Input, kinds []Kind) (Verdict, bool) {
	if in.Op != OpType || !IsCredentialField(in.Target) {
		return Verdict{Decision: Deny, Rule: RuleCredentials, Kinds: kinds,
			Reason: "A secret goes only into a password or one-time-code field; this is not one. Nothing was done. fix: pass the ref of the password or code field."}, true
	}
	if !e.secretAppLocked(in.App.ID) {
		return Verdict{Decision: Deny, Rule: RuleCredentials, Kinds: kinds,
			Reason: fmt.Sprintf("%s is not on the owner's credential_apps list, so Pinky types no secret there. Nothing was done. fix: ask the owner to add %s to credential_apps in rules.yaml, or call phone_handoff.",
				appName(in.App), idOrName(in.App))}, true
	}
	return Verdict{}, false
}

// secretLiftLocked reports whether the bundled Finance or Password-manager
// block does not apply to app because it is on credential_apps. No other
// rule is lifted: the person's own blocks, the system apps and Pinky's own
// apps stay, and payments in a listed app still ask.
func (e *Engine) secretLiftLocked(app screen.App, r Rule) bool {
	return (r.Name == RuleFinance || r.Name == RulePasswords) && e.secretAppLocked(app.ID)
}

func idOrName(app screen.App) string {
	if app.ID != "" {
		return app.ID
	}
	return appName(app)
}
