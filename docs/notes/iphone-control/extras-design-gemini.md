# Design: Autonomous Unlock & Credential Typing (`iphone-control` fork of Pinky)

**Author:** Gemini 4 Neon (design sub-agent)  
**Target repo:** `/Users/nqh/Desktop/iphone-control` (Go fork of Pinky, vendored later into `/Users/nqh/Desktop/ipad-local/tools/iphone-control/`)  
**Status:** Design sketch only (`architect` + `codebase-design` gate; no code edited)

---

## 1. Phase A Grounding (Verified Code Facts)

Every claim below is grounded directly in the repository (unverified runtime facts are marked **UNPROVEN**):

- **Baseline fork diff (`docs/notes/iphone-control/DESIGN.md:1-33`)**: Dedicated iPhone SE (iOS 26.2) via WebDriverAgent (`WDADevice`). Lease queue lives in `internal/gateway/queue.go:1-266` (`Options.LeaseReserve`, `wait_ms`), device filtering (`PINKY_ALLOW_DEVICES`) and agent trust (`PINKY_TRUST_AGENTS=1`) live in `cmd/pinky/daemon.go:208-219,276-286`, and the generic CLI `pinkyctl phone status|tools|call|run` lives in `cmd/pinky/phone.go:1-859`.
- **Unlock & Wake (`internal/gateway/wake.go:30-140`, `internal/driver/ios/wda_power.go:20-242`, `internal/driver/ios/power.go:204-350`)**:
  - `Gateway.Unlock(deviceID)` (`wake.go:69-140`) calls `g.opt.PIN(pctx, deviceID)`, acquires `d.act.Lock()`, calls `g.inputLocked(d)` and `g.pauseVideoLocked(d)`, checks `g.lockedNow(ctx, dev)` so an unlocked phone never receives a PIN, calls `u.Unlock(ctx, pin)`, zeroes `pin`, logs without the PIN, records a PIN-free step (`recordUnlock`, `wake.go:306-325`), wraps `ErrWrongPIN` on `driver.ErrLocked`, and answers unlock-only handoffs (`answerUnlockHandoffs`).
  - `WDADevice` implements `driver.Unlocker` (`wda_power.go:242`) and `driver.LockReporter` (`wda_device.go:1204`), but **does NOT implement `driver.Waker`** (only CoreDevice's `Device` implements `driver.Waker`, `power.go:510`). Thus `g.wake(ctx, d)` (`wake.go:30-52`) is a no-op on `WDADevice`; waking on WDA happens inside `WDADevice.Unlock` -> `reachPad` -> `wdaPower.pressHome` -> `swipeUp` -> `keypadShown` (with `pressPadButton` handling Touch ID's "Use Passcode" prompt, `power.go:320-332`, `wda_power.go:145-155`), tapping digit keys directly, checking `p.locked` before every key, and never typing twice (`power.go:216-246`).
  - **Lock ordering hazard**: `Gateway.Unlock` acquires `d.act.Lock()` (`wake.go:95`). `Gateway.act` calls `g.gate(l, c.ApprovalID, true)` *outside* `d.act` (`pipeline.go:474`), then `g.readyScreen(ctx, l)` *outside* `d.act` (`pipeline.go:480`), then `lockAct(ctx, d)` (`pipeline.go:482`), then `g.gate(l, "", true)` *inside* `d.act` (`pipeline.go:492`), then `startScreen` and `cur.Locked` (`pipeline.go:510-523`). `firstScreen` (`agent.go:1033-1075`) and `Observe` (`agent.go:1373-1485`) do not hold `d.act`.
  - **Call budget (`internal/gateway/budget.go:10-113`)**: One tool call has `Options.CallBudget = 45s` (`gateway.go:116`). `waitTurn` leaves `connectReserve = 15s` (`queue.go:134`). `unlockPINTimeout` is 2 min (for Touch ID) and `unlockTimeout` is 30s (`wake.go:57-58`).
- **Keychain & Secrets (`internal/secret/secret.go:54-124`, `internal/secret/keychain_darwin.go:16-408`, `cmd/pinky/app.go:438-470`, `cmd/pinky/daemon.go:131-133`)**:
  - `cmd/pinky/daemon.go:131-133`: `gatewayConfig.PIN` is `nil` in `runDaemon` (no one to confirm Touch ID).
  - `cmd/pinky/app.go:441-470`: desktop app's `pinSource` calls `store.Reveal(ctx, secret.PINAccount(deviceID), "unlock "+name)`, which invokes LocalAuthentication (`presence_darwin.go:63-128`).
  - `internal/secret/secret.go:69,85,116-124`: `Service = "app.pinky.desktop"`, `PINAccount(id) = "pin:"+id`. `secret.Get` explicitly rejects `pin:*` with `ErrNotModelKey` (enforced by `internal/secret/get_test.go:9-27`).
  - `internal/secret/keychain_darwin.go:285-300,375-394`: `Has()` sets `kSecUseAuthenticationUI = kSecUseAuthenticationUIFail` to avoid modal prompts, whereas `read()` does **not** — on a locked keychain or rebuilt ad-hoc binary (`keychain_darwin.go:24-27`), `read()` can pop a macOS modal dialog and hang a headless daemon.
  - `go.mod:21`: `golang.org/x/sys` is already a direct dependency (usable for no-echo TTY reads without adding dependencies).
  - iPad precedent (`/Users/nqh/Desktop/ipad-local/tools/passcode/DESIGN.md:3-11`, `tools/unlock/DESIGN.md:10-49`, `tools/unlock/unlock:46-57`): one attempt per run, no verb that prints the secret, secret never in argv/files/logs.
- **Credentials & Policy (`internal/policy/defaults.yaml:27-68`, `internal/policy/file.go:14-63`, `internal/policy/engine.go:178-276`, `internal/policy/presets.go:163-192,341-407`, `internal/policy/apps.go:97-147`, `internal/policy/detect.go:613-649`, `internal/policy/keypad.go:28-62`, `internal/gateway/pipeline.go:866-942`)**:
  - `RuleCredentials` (`policy.go:84`) is enforced in **three** places: (1) `Engine.Evaluate` (`engine.go:197-203`), (2) `HiddenKeypad` tap check (`pipeline.go:912-918`), and (3) unfocused `phone_type` when `credentialOnScreen(cur)` (`pipeline.go:937-941`).
  - `apps.yaml` `exclude:` (`apps.go:101-126`, `presets.go:341-407`) already removes explicitly named bundle IDs/names from `finance` and `passwords` categories (lifting `Finance apps` and `Password managers` deny rules in `defaults.yaml:61-68`) while keeping `Payment` commit detection active (`detect.go:835-839`).
  - `phone_type` (`internal/mcp/tools.go:475-485`) sends `Action.Text` into `Action.digest()` (`pipeline.go:182`), `Action.args(c)` (`pipeline.go:221-223`, saved to SQLite `record.go:84-87` and exported in `export.go:46,183`), `sayAction`/`describe` (`pipeline.go:2934-2942`, also used for recording marks `video.go:788`), `approvalTitle` (`pipeline.go:1451-1452`), and `dev.Type` (`pipeline.go:2147`).
  - `WDADevice.Type` (`wda_device.go:776-798`, `wda.go:548-550`) posts to `/wda/keys` directly (never touches pasteboard, never logs `text`). CoreDevice `Device.Type` (`type.go:175-180`) uses the pasteboard and `judgeTyped` (`type.go:266-289`) reports character counts on mismatch.
  - One-time code masking already exists in `policy.MaskScreen` (`policy/codes.go:47-54`), `gateway.coverCodes` (`gateway/codes.go:56-77`), and `shots.go:329-336`, but does **not** mask arbitrary passwords or skip/cover credential fields after typing.
  - Owner's credential broker (`~/.agents/skills/credentials/SKILL.md:27-140`): `cred find <query>` returns `{ login, title, sites, account }` (no approval, no secrets); `cred run (--site <host> | --login <id>) --reason <text> [--stdin <template>] [--env NAME=<template>] -- <cmd>...` passes `{username}`, `{password}`, `{otp}` (freshly generated TOTP at start), asks the owner to approve, redacts stdout/stderr, and returns JSON with `status` (`done`, `waiting` + `request_id` for `cred resume`, `denied`, `expired`, `unavailable`, `invalid`). **UNPROVEN:** exact default wait timeout of `cred run` before returning `waiting`, and whether `cred` enforces a command allowlist on `cred run`.

---

## 2. Addition 1: Autonomous Unlock

### 2.1 Usage (Caller's View)

**Provisioning (owner, once per phone — secret never touches argv, file, or transcript):**
```sh
# 1. Interactive hidden prompt (no echo on TTY)
pinkyctl phone pin set -device 00008030-001234567890ABCD
# Passcode (hidden):
# stderr: saved PIN for iPhone SE (00008030-001234567890ABCD)

# 2. From a T3 request_secret delivered file (unconditionally zeroed & deleted)
pinkyctl phone pin set -device 00008030-001234567890ABCD --from-file /tmp/t3-secret-xxxx

# 3. Verify presence without ever revealing the value
pinkyctl phone pin check -device 00008030-001234567890ABCD
# stdout: saved

# 4. Remove
pinkyctl phone pin delete -device 00008030-001234567890ABCD
```

**Daemon startup (opt-in, fork-scoped):**
```sh
PINKY_ALLOW_DEVICES=00008030-001234567890ABCD \
PINKY_TRUST_AGENTS=1 \
PINKY_AUTO_UNLOCK=1 \
pinkyctl daemon --http 127.0.0.1:7465
```

**Agent experience (MCP or `pinkyctl phone`, unchanged tools):**
```sh
# Phone is asleep and locked; agent runs a normal call:
pinkyctl phone call observe
# Gateway sees locked -> wakes + unlocks once -> returns settled + full unlocked screen (exit 0)

# If no PIN is saved:
pinkyctl phone call observe
# stdout: device_locked · iPhone SE
#         The phone is locked and no autonomous-unlock PIN is saved for iPhone SE.
#         fix: run `pinkyctl phone pin set -device 00008030-...` (no-echo prompt or --from-file)
# (exit 5: phoneExitPerson; returns immediately, never hangs)

# If saved PIN is wrong (after 1 attempt):
pinkyctl phone call observe
# stdout: device_locked · iPhone SE
#         Autonomous unlock failed: the phone did not accept the saved PIN. Pinky will not try this PIN again.
#         fix: save the right PIN with `pinkyctl phone pin set -device 00008030-...` or unlock on the phone
# (exit 5; every subsequent call with the same saved PIN makes 0 unlock attempts)
```

### 2.2 Chosen Shape (Candidate U1)

#### Module Map & Signatures

1. **Non-interactive Keychain store (`internal/secret/autounlock.go`, `autounlock_darwin.go`, `autounlock_other.go`)**
   ```go
   package secret

   // Separate keychain service so personal-phone PINs saved in Pinky.app
   // (Service = "app.pinky.desktop") can NEVER be read without Touch ID.
   const AutoUnlockService = "app.pinky.iphone-control.pin"

   // ErrInteractionNotAllowed means macOS Keychain refused a silent read
   // (keychain locked or ad-hoc binary signature changed since Save).
   var ErrInteractionNotAllowed = errors.New("secret: keychain refused non-interactive read")

   // SilentGetter reads a secret without ever showing Touch ID or a
   // macOS Keychain modal prompt (kSecUseAuthenticationUIFail).
   type SilentGetter interface {
       GetSilent(ctx context.Context, account string) (string, error)
   }

   // NewService returns a Store scoped to a custom keychain service name.
   // Upstream secret.System() and secret.Get() remain untouched.
   func NewService(service string) Store

   // GetSilent reads account without prompting. Never accepts Service
   // ("app.pinky.desktop") PIN accounts; intended for fork-scoped stores.
   func GetSilent(ctx context.Context, s Store, account string) (string, error)
   ```
   - On Darwin (`autounlock_darwin.go`), `(keychain).GetSilent` calls `SecItemCopyMatching` with `kSecReturnData=true`, `kSecMatchLimit=kSecMatchLimitOne`, and `kSecUseAuthenticationUI=kSecUseAuthenticationUIFail` (already loaded on `security` at `keychain_darwin.go:157-162`), mapping `errSecInteractionNotAllowed` (`-25308`) to `ErrInteractionNotAllowed`.

2. **Gateway autonomous unlock (`internal/gateway/autounlock.go` + surgical hooks)**
   ```go
   package gateway

   // Addition to Options (internal/gateway/gateway.go):
   type Options struct {
       ...
       // AutoUnlock lets the gateway enter Options.PIN itself when an agent
       // call finds a leased phone locked (and the person is not in control).
       // Off by default (upstream desktop app uses Touch ID via client Unlock).
       AutoUnlock bool
   }

   // Addition to Gateway (internal/gateway/gateway.go):
   //   pinSalt [32]byte // random per-process key for wrong-PIN HMAC latch

   // Addition to device (internal/gateway/state.go, guarded by Gateway.mu):
   type unlockFailure struct {
       pinMAC [32]byte  // HMAC-SHA256(g.pinSalt, wrongPIN); never plaintext
       at     time.Time
   }
   // device.unlockFailed *unlockFailure

   // autoUnlockStatus classifies an autonomous unlock outcome for messages.
   type autoUnlockResult struct {
       unlocked bool
       reason   string // actionable message + "fix: ..." when !unlocked
   }

   // tryAutoUnlock attempts at most one autonomous unlock of l.dev.
   // Precondition: caller holds NEITHER g.mu NOR d.act.
   func (g *Gateway) tryAutoUnlock(ctx context.Context, l *lease) autoUnlockResult

   // unlockDevice is the shared implementation of Gateway.Unlock (autonomous=false)
   // and tryAutoUnlock (autonomous=true). Holds d.act only around the lock check
   // and driver.Unlocker.Unlock call; pauses video both before and after.
   func (g *Gateway) unlockDevice(ctx context.Context, d *device, pin string, autonomous bool) error
   ```

3. **CLI provisioning & daemon wiring (`cmd/pinky/pin.go`, `cmd/pinky/daemon.go`, `cmd/pinky/phone.go`)**
   ```go
   // cmd/pinky/pin.go
   func phonePinCmd(args []string, pio phoneIO) int
   func readSecretHidden(in io.Reader, errOut io.Writer, fromFile string) (string, error)
   func autoUnlockPIN(store secret.Store, log *slog.Logger) func(ctx context.Context, deviceID string) (string, error)
   ```
   - `readSecretHidden`: rejects positional secret args (exit 2 + `fix:`); if `fromFile != ""`, reads one trimmed line and **defers zero-overwrite + `os.Remove(fromFile)` unconditionally**; if stdin is a TTY (`unix.IoctlGetTermios`), clears `unix.ECHO` while reading one line; else reads one line from piped stdin. Validates against `pinStrokes`-compatible printable ASCII (non-empty, no whitespace/control chars).
   - `runDaemon` (`cmd/pinky/daemon.go`): when `os.Getenv("PINKY_AUTO_UNLOCK") == "1"` and `!c.Fake`, sets `c.AutoUnlock = true` and `c.PIN = autoUnlockPIN(secret.NewService(secret.AutoUnlockService), c.Log)`. Logs a warning if `PINKY_ALLOW_DEVICES` is empty.

#### Trigger Points, Locking & Invariants

- **Where triggered (lazy, on agent calls only — never on idle phones):**
  1. `readyScreen(ctx, l)` (`background.go:144-146`): right after `g.wake(ctx, d)`, when `!onBG && g.opt.AutoUnlock`, checks cached `d.locked` or `LockReporter.Locked(ctx)`; if locked and `!d.person`, calls `g.tryAutoUnlock(ctx, l)`. This covers `firstScreen` (`phone_connect`), `Observe`, `WaitFor`, `runApproved`, and `handedBackResult`.
  2. `act(ctx, l, c, a)` (`pipeline.go:474`): if the pre-`d.act` `g.gate(l, c.ApprovalID, true)` returns `StatusLocked` and `c.ApprovalID == ""` and `g.opt.AutoUnlock`, calls `g.tryAutoUnlock(ctx, l)` (outside `d.act`!) and, if unlocked, re-runs `g.gate` once.
  3. Post-`observe` fallback in `firstScreen` (`agent.go:1070`), `Observe` (`agent.go:1426`), and `act` (`pipeline.go:519`, *after* `d.act.Unlock()`): handles drivers without `LockReporter` or lock-state lag where `cur.Locked` is only discovered on read — calls `g.tryAutoUnlock(ctx, l)` at most once per call, then re-observes `cur`.
- **Zero-deadlock invariant:** `tryAutoUnlock` is NEVER called while holding `d.act` or `g.mu`. Inside `tryAutoUnlock`, `lockAct(ctx, d)` bounds waiting for `d.act` to the call's `CallBudget`.
- **Never-retry / lockout-proof invariant:**
  - Before calling `u.Unlock`, compute `mac := hmacSHA256(g.pinSalt, pin)`. If `d.unlockFailed != nil && hmac.Equal(d.unlockFailed.pinMAC[:], mac[:])`, immediately wipe `pin` and return `unlocked: false` with the wrong-PIN `fix:` message — **0 driver calls**.
  - On `driver.ErrLocked` from `u.Unlock`, store `d.unlockFailed = &unlockFailure{pinMAC: mac, at: time.Now()}`. Even if the person manually unlocks the phone later (`setLocked(d, false)`) and the phone re-locks, the same wrong PIN is **never** retried. Once the owner runs `pinkyctl phone pin set` with a corrected PIN, its MAC differs, allowing one attempt without restarting the daemon.
  - On `driver.ErrPINNotEntered` (swipe did not reach pad; zero attempts spent, `power.go:347`), do NOT set `d.unlockFailed`, return `StatusLocked` for this call (at most one attempt per call).
- **Person-takeover invariant:** `tryAutoUnlock` checks `d.person`, `g.paused[d.id]`, `l.ended`, and `l.bg != nil` both before fetching the PIN and again after acquiring `d.act`. If `d.person == true`, it does nothing; `StatusTookOver` / `StatusPaused` stands.
- **Zero-leak invariant:**
  - `g.opt.PIN` fetch context is bounded to `5s` (`autoUnlockPINTimeout`), never 2 min.
  - `g.inputLocked(d)` drops any pending `bgShot`; `g.pauseVideoLocked(d)` runs **both before AND right after** `u.Unlock` so zero passcode-pad frames can enter the MP4.
  - `pin` is wiped (`pin = ""`) immediately after `u.Unlock`; `slog` logs only `device`, `ok`, and error kind; `recordUnlock` records `"Unlocked automatically"` / `"wrong_pin"` / `"pin_not_entered"` with zero secret data.
- **Call budget invariant:** `unlockDevice` uses `min(unlockTimeout, budgetLeft)` before the first digit; once the first digit is tapped, `power.go:220` already finishes the single attempt under `context.WithoutCancel(ctx)` so a phone is never left half-typed.

#### `codebase-design` Depth & Seam Assessment
- **Module:** *autonomous device unlock* (`internal/gateway/autounlock.go` + `internal/secret/autounlock*.go`).
- **Seam:** `Options.AutoUnlock` + `Options.PIN` at `Gateway`, backed by `secret.SilentGetter` at `internal/secret`.
- **Adapters:** `autoUnlockPIN` (Darwin `SecItemCopyMatching` with `kSecUseAuthenticationUIFail`), `pinSource` (existing desktop Touch ID `Reveal`), and `secret.Memory` (tests).
- **Depth & leverage:** Callers (`phone_connect`, `phone_observe`, `phone_tap`, MCP clients, CLI) learn **0** new tools or arguments. All complexity (wake, Touch ID prompt dismissal, lock-before-key check, single-attempt HMAC latch, video pause, takeover respect, non-interactive keychain error mapping) stays localized in one module.

### 2.3 Rejected Candidates (Addition 1)

| Candidate | Structural Shape | Why It Lost (Red Flags & Depth) |
|---|---|---|
| **U2: CLI-side unlock wrapper** | `pinkyctl phone` intercepts `device_locked`, calls control RPC `unlock`, retries tool call | **Split ownership & shallow pass-through**: (1) Direct MCP clients (`pinkyctl mcp`, `--http`) still hit `device_locked`. (2) Each `phone call` is a fresh process, so no shared wrong-PIN latch exists — repeated calls lock out the iPhone. (3) Blurs privilege seams by having the agent-facing CLI invoke human control RPCs. |
| **U3: Proactive background unlocker** | `discoverLoop` -> `pollLocks` unlocks idle locked phones automatically | **Violates Pinky invariant** (`wake.go:345`: never wake/unlock unasked). Races with a person locking the phone, drains battery, and has no active lease/task to audit the unlock step in history (`recordUnlock(nil, ...)` drops it). |

---

## 3. Addition 2: Credential Typing

### 3.1 Usage (Caller's View)

**Owner configures allowed apps in `rules.yaml` (hot-reloaded by `Engine.Refresh`, never writable by agents):**
```yaml
# $PINKY_HOME/rules.yaml
preset: 2
mask_codes: true
credential_apps:
  - com.github.stormcrow
  - com.tinyspeck.chatlyio
  - com.example.mybank          # if finance/passwords category, also list in apps.yaml exclude:
```
And for a specific banking or password-manager app the owner wants unblocked whole-app (existing upstream mechanism, `internal/policy/apps.go:101-126`):
```yaml
# $PINKY_HOME/apps.yaml
exclude:
  - com.example.mybank
```

**Optional unattended Keychain secret provisioning (for dedicated-phone test accounts / TOTP seeds):**
```sh
# Password (no-echo prompt or --from-file, never positional arg)
pinkyctl phone cred set staging-shop --kind password
pinkyctl phone cred set staging-shop --kind totp --from-file /tmp/t3-secret-totp
pinkyctl phone cred check staging-shop
# stdout: password,totp
pinkyctl phone cred delete staging-shop
```

**Agent flow (MCP or `pinkyctl phone` — zero new MCP tools):**
```sh
# 1. Agent finds the login id via the owner's credential broker (no approval, no secret):
cred find github
# -> {"logins":[{"login":"lg_3f9a1c07b2d4","title":"GitHub","sites":["github.com"],"account":"dev@example.com"}]}

# 2. Agent types username normally, then password & OTP via placeholders in phone_type:
pinkyctl phone call type ref=14 text='dev@example.com'
pinkyctl phone call type ref=15 text='{{secret:lg_3f9a1c07b2d4:password}}' submit=true
# -> settled · iPhone SE (secret resolved at last moment, typed, wiped; history says "Typed secret (password) into “Password”")

# 3. On the 2FA screen, agent requests a fresh TOTP code from the broker:
pinkyctl phone call type ref=22 text='{{secret:lg_3f9a1c07b2d4:otp}}' submit=true
# -> settled · iPhone SE (OTP digits masked as •••••• in returned screen & screenshots)

# 4. In an app NOT in credential_apps:
pinkyctl phone call type ref=15 text='{{secret:lg_3f9a1c07b2d4:password}}'
# -> blocked_by_policy · iPhone SE · rule: credentials
#    Pinky never types into password or one-time-code fields; call phone_handoff so the person can fill it.
#    fix: to allow brokered credentials in this app, add its bundle id to credential_apps in rules.yaml
```

### 3.2 Chosen Shape (Candidate C1)

#### 1. Policy Seam (`internal/policy/`)
```go
// internal/policy/file.go — addition to File:
type File struct {
    ...
    // CredentialApps lists apps (bundle ids, package names, display names, or
    // "com.example.*" prefixes) where an agent may type broker-resolved
    // credentials ({{secret:...}}) into password and one-time-code fields on
    // a real phone. Empty by default; owned by the person in rules.yaml.
    CredentialApps []string `yaml:"credential_apps,omitempty"`
}

// internal/policy/policy.go — addition to Input:
type Input struct {
    ...
    // CredentialRef is true when OpType's text is a validated {{secret:...}}
    // placeholder that the gateway will resolve at execution time.
    CredentialRef bool
}

// internal/policy/presets.go — additions to Engine:
func (e *Engine) CredentialApps() []string
func (e *Engine) SetCredentialApps(apps []string) error
func (e *Engine) CredentialAppAllowed(app screen.App) bool
```
**Exact rule behavior in `Engine.Evaluate` (`internal/policy/engine.go:197-203`):**
- `in.Op == OpType && IsCredentialField(in.Target)`:
  - Allowed when `(in.Virtual && e.file.VirtualCredentials)` **OR** `(in.CredentialRef && e.credentialAppAllowedLocked(app))`.
  - Denied (`RuleCredentials`) otherwise:
    - App NOT in `credential_apps` and `!in.CredentialRef`: **exact upstream reason unchanged** (all upstream tests pass verbatim).
    - App NOT in `credential_apps` and `in.CredentialRef`: upstream reason + `fix: add "<app.ID>" to credential_apps in rules.yaml to allow brokered credentials in this app`.
    - App IN `credential_apps` and `!in.CredentialRef` (real phone): denied with `"Typing raw text into credential fields is denied even for allowed apps; use a {{secret:<ref>:password}} or {{secret:<ref>:otp}} placeholder.\nfix: find the login with cred find and pass {{secret:<id>:password}} or {{secret:<id>:otp}}"`.
- **Anti-exfiltration guard (non-credential field with placeholder):**
  - If `in.Op == OpType && in.CredentialRef && !IsCredentialField(in.Target)` (e.g., agent passes `{{secret:...}}` to Notes, Messages, Search, or URL bar): **DENIED** (`RuleCredentials`) with `"Secret placeholders may only be typed into password or one-time-code fields, not %q.\nfix: pass the ref of the password or code field"`.
  - If `in.Op == OpType && in.CredentialRef && in.Target == nil` (no `ref` and no focused field reported): **DENIED** (`ErrInvalidArgument`) in `prepare` with `"Pinky cannot verify that the focused field is a password or code field.\nfix: pass ref=<field ref> from phone_observe"`.
- **Unchanged upstream protections:**
  - `ReadOnly` (`engine.go:258`), `ScopeApps` (`engine.go:212`), `TaskApps` (`engine.go:217`), `PresetEvery` (`engine.go:230`), `always_allow` (`exempt.go:76`, `RuleCredentials` remains non-exemptable), and `HiddenKeypad` (`pipeline.go:912`, tapping hidden digit pads stays denied on real phones because tapping requires the agent to know the digits) all remain intact.
  - Person-initiated approvals (`PresetPayments`, `phone_request_approval`, future Q5 gates) continue working identically.

#### 2. Placeholder Grammar & Gateway Resolver Seam (`internal/gateway/creds.go`)
```go
package gateway

type CredentialKind string

const (
    CredPassword CredentialKind = "password"
    CredOTP      CredentialKind = "otp"
)

// SecretRef is a parsed {{secret:<target>[:password|otp]}} placeholder.
// The entire Action.Text must be one token; mixed text or multiple tokens are rejected.
type SecretRef struct {
    Raw    string         // original placeholder, safe for history/logs
    Target string         // "lg_..." broker login id, site host, or keychain name
    Kind   CredentialKind // CredPassword (default) or CredOTP
}

type CredentialRequest struct {
    Ref    string         // SecretRef.Target
    Kind   CredentialKind // CredPassword or CredOTP
    App    screen.App     // foreground app receiving the typed value
    Device string         // device display name
    Reason string         // Call.Intent or task title (shown on broker approval)
}

type CredentialResolver interface {
    Resolve(ctx context.Context, req CredentialRequest) (string, error)
}

// Typed errors returned by adapters (all wrapped with actionable fix: lines):
var (
    ErrSecretNotFound    = errors.New("secret not found")
    ErrBrokerWaiting     = errors.New("credential broker waiting for approval")
    ErrBrokerDenied      = errors.New("credential broker request denied")
    ErrBrokerExpired     = errors.New("credential broker request expired")
    ErrBrokerUnavailable = errors.New("credential broker unavailable")
)

// parseSecretRef checks whether text is a secret placeholder.
//   - Returns (nil, nil) if text contains no "{{secret:" marker (ordinary typing).
//   - Returns (ref, nil) if text is a valid single "{{secret:<target>[:password|otp]}}".
//   - Returns (nil, err) if malformed, mixed with surrounding text, or unknown kind.
func parseSecretRef(text string) (*SecretRef, error)

// Addition to Options (internal/gateway/gateway.go):
//   Credentials CredentialResolver // nil disables placeholder resolution
```

#### 3. Concrete Resolver Adapters (`cmd/pinky/creds_resolver.go`, outside `internal/gateway`)
1. **`CredBrokerResolver` (Owner's `cred` CLI)**:
   - Invokes `cred run (--login <id> | --site <host>) --reason <plain sentence> --stdin '{password}' -- <self-exe> _cred-deliver <sock-path>` (using `--stdin '{otp}'` when `Kind == CredOTP`, so the broker generates a fresh TOTP right as the command starts, matching `~/.agents/skills/credentials/SKILL.md:66`).
   - **Socket delivery (`_cred-deliver`)**: Gateway creates a `0700` temp directory and a `0600` Unix domain socket, verifies connecting peer UID == `os.Getuid()` (`LOCAL_PEERCRED` on Darwin), accepts exactly one connection, reads up to 4 KiB, closes & removes the socket. `_cred-deliver` reads stdin, writes to the socket, prints nothing to stdout/stderr, exits 0. The secret never touches argv, env of other processes, disk, or stdout/stderr.
   - **Waiting / Resume handling**: If `cred run` returns JSON `{"status":"waiting","request_id":"..."}`, `CredBrokerResolver` retains the pending `(requestID, socket, secretCh)` in memory keyed by `(target, kind, app.ID)` for 10 min (matching `cred`'s 10m expiry, `SKILL.md:134`) and returns `ErrBrokerWaiting` naming `request_id`. When the agent repeats the same `phone_type` call, `CredBrokerResolver` invokes `cred resume <request_id>` so the owner is never asked twice. Cleaned up on timeout or `Close()`.
   - Maps `cred` JSON statuses (`denied`, `expired`, `unavailable`, `invalid`) to typed errors with `fix:` lines (e.g., `"fix: import the login in Credential Broker, or store a dedicated secret with pinkyctl phone cred set"`).
2. **`KeychainCredResolver` (Non-Interactive macOS Keychain + In-Process TOTP)**:
   - Uses `secret.NewService("app.pinky.iphone-control.cred")` + `secret.GetSilent` (`kSecUseAuthenticationUIFail`).
   - Accounts: `"cred:<name>:password"` and `"cred:<name>:totp"` (Base32 TOTP secret or `otpauth://` URI).
   - For `CredOTP`: computes the current 6-digit RFC 6238 SHA-1/30s TOTP code in-process using Go stdlib (`crypto/hmac`, `crypto/sha1`, `encoding/base32`, `time.Now()`), wipes the seed buffer immediately. Zero new dependencies.
3. **`ChainResolver` (Routing)**:
   - `Target` starts with `lg_` -> `CredBrokerResolver` (`--login`).
   - Otherwise -> `KeychainCredResolver` first; if `ErrSecretNotFound` and `CredBrokerResolver != nil` and `Target` contains `.` (hostname) -> `CredBrokerResolver` (`--site`).
   - Wired in `cmd/pinky/daemon.go` when `PINKY_CREDENTIALS=1` (and `!c.Fake`).

#### 4. Last-Moment Resolution & Six-Surface Masking (`internal/gateway/pipeline.go` + `creds.go`)

- **Last-moment rule:** `a.Text` is **never** mutated to the resolved secret. Resolution runs strictly inside `executeOn` (`pipeline.go:1834`), *after* `gate`, `prepare`, `evaluate`, `ask`/`awaitApproval`, and `preflight` all succeed. A denied, blocked, read-only, stale, or pending call never touches the resolver.
- **Re-preflight:** Right after `Credentials.Resolve` returns `val`, `executeOn` runs `g.preflight(ctx, l)` again (in case takeover or lock occurred while the broker waited) before passing a stack-local copy `aRun := a; aRun.Text = val` to `g.perform`.
- **Six-surface masking contract:**
  1. **Agent result (`Result.Screen`, `Result.Diff`, `Result.Message`)**: `scrubSecretScreen(next, val, p.target)` replaces `val` (and spaced/hyphenated 6-digit OTP forms `"123 456"`, `"123-456"`) with `"••••••"` in every element's `Label` and `Content`, marks those elements `Masked = true`, and clears `Content` on `IsCredentialField` elements. `scrubString` strips `val` from `inputNote` and any driver error. `val` and `aRun.Text` are immediately zeroed (`""`) before `executeOn` returns.
  2. **History DB (`StepView.Text`, `StepView.Args`, `ScreenText`)**: `describe(a, p)` returns `"Typed secret (password) into “Password”"` (or `"one-time code"`). `a.args(c)` holds only the non-secret placeholder `{{secret:...}}`. `ScreenText` comes from the scrubbed `res.Screen`.
  3. **Exported HTML & JSON (`export.go`)**: Built from history DB — contains only placeholder + scrubbed screen text.
  4. **History screenshots (`shots.go`)**: `executeOn` skips `g.startShot(d, next)` (`shot = nil`) for any secret-typing step so no post-type screenshot is stored.
  5. **Agent `phone_observe` screenshots (`pipeline.go:2500-2538`)**: `lease.secretApps[fg.ID] = true` marks apps that received a typed secret during the lease; `screenshot()` covers every `IsCredentialField(e)` bounding box with `codeFill` (in addition to existing `coverCodes` + OCR `pictureCodes` for OTP digits!).
  6. **Screen recordings (`video.go`)**: `g.pauseVideoLocked(d)` is called both before `dev.Type` and right after `dev.Type` + settle, dropping every keystroke-popup or last-character-reveal frame from the MP4.
  7. **Logs (`slog`)**: Only `ref` target/kind and error category are logged; never `val`.

#### `codebase-design` Depth & Seam Assessment
- **Modules:** *credential policy gate* (`internal/policy`), *secret typing & scrubbing* (`internal/gateway/creds.go`), *broker/keychain adapters* (`cmd/pinky/creds_resolver.go`).
- **Seams:** (1) `policy.File.CredentialApps` + `policy.Input.CredentialRef` between gateway and policy; (2) `gateway.CredentialResolver` between gateway and host secret backends.
- **Adapters at seam 2:** `CredBrokerResolver` (`cred run`/`resume`), `KeychainCredResolver` (macOS Keychain + TOTP), and `fakeResolver` (unit/guard tests) — three real adapters justifying the seam.
- **Depth & leverage:** Zero new MCP tools, zero CLI `phone call`/`run` changes, automatic `phone_batch` support, and all 6 masking invariants enforced in one place (`executeOn` + `scrubSecretScreen`).

### 3.3 Rejected Candidates (Addition 2)

| Candidate | Structural Shape | Why It Lost (Red Flags & Depth) |
|---|---|---|
| **C2: Dedicated MCP tool `phone_type_secret`** | Add 21st MCP tool `phone_type_secret(ref, login, field, submit)` | **Two ways to do one task & high upstream diff**: Duplicates `phone_type`'s focus, clear, append, submit, settle, and `phone_batch` plumbing; widens `internal/mcp/tools.go` (`batchStep`, `stepTools`), diverging MCP catalog from upstream. |
| **C3: CLI-side broker wrapper** | Agent runs `cred run --stdin '{password}' -- pinkyctl phone call type ...`, sending plaintext over MCP | **Information leakage & broken zero-leak invariants**: Plaintext crosses MCP wire into `Action.Text`, leaking into SQLite history, `describe()`, exports, and approval titles; gateway cannot distinguish broker secrets from raw literals; breaks for direct MCP clients. |

---

## 4. Seam Guards & Contract Declarations (Question C)

Every new seam ships with a declaration, a mechanical test guard whose failures end with `fix:`, and a fixture-based self-test proving the guard catches violations.

| Seam | Contract Declared In | Mechanical Guard Tests | Fixture-Based Guard Self-Test |
|---|---|---|---|
| **1. Autonomous unlock & wrong-PIN latch** | `docs/mcp.md` ("Autonomous unlock"), `internal/gateway/autounlock.go` | `internal/gateway/autounlock_test.go`: (1) `AutoUnlock=false` leaves upstream `StatusLocked` & 0 `PIN`/`Unlock` calls; (2) `AutoUnlock=true` unlocks on `Connect`/`Observe`/`Act`; (3) wrong PIN trips `pinMAC` latch -> 0 retries across repeated calls AND after manual unlock + relock; (4) updated PIN recovers with 1 try; (5) `d.person==true` -> 0 `Unlock` calls; (6) missing PIN / `ErrInteractionNotAllowed` returns `StatusLocked` <100ms with `fix:`; (7) PIN canary absent from slog, history DB, exports, video frames. | `TestAutoUnlockGuardCatchesViolations`: runs the guard verifier over deliberately defective transcripts (retry after wrong PIN, unlock during takeover, PIN leaked into log/step text) and asserts the guard FAILS on each bad fixture and PASSES on the clean fixture. |
| **2. Non-interactive Keychain isolation** | `internal/secret/autounlock.go` (`AutoUnlockService`), `cmd/pinky/pin.go` | `internal/secret/autounlock_test.go`: `AutoUnlockService != Service`, `CredService != Service`, `Get(ctx, m, PINAccount("x"))` still returns `ErrNotModelKey`. `cmd/pinky/pin_test.go`: positional secret arg exits 2 + `fix:`; `--from-file` deletes file on both success and validation failure; no `get`/`show` subcommand. | `TestPinCLIGuardCatchesLeakyFixtures`: asserts the CLI guard fails if a temp secret file survives an error or if a secret appears in stdout/stderr. |
| **3. Credential policy (`credential_apps`)** | `internal/policy/defaults.yaml`, `internal/policy/file.go` | `internal/policy/credapps_test.go`: empty/unlisted app denies (`RuleCredentials`, exact upstream message); listed app allows ONLY `CredentialRef=true` on `IsCredentialField`; listed app denies raw literal (`CredentialRef=false`); listed app denies `CredentialRef=true` on non-credential field; read-only & scope still deny; YAML hot-reload & round-trip preserve `credential_apps`. | `TestCredentialPolicyGuardMatrix`: table-driven fixture matrix covering every `(appAllowed, isCredField, credRef, virtual, readOnly)` cell; verifies a mutated decision is caught with `fix:` naming `rules.yaml` / `policy/engine.go`. |
| **4. Last-moment resolution & 6-surface masking** | `docs/mcp.md` (`phone_type`), `internal/gateway/creds.go` | `internal/gateway/creds_test.go`: (1) resolver called 0 times on denied/stale/non-cred/pending calls; (2) called once on allowed `password` and `otp`; (3) canary secret & OTP digits checked absent across all 6 surfaces (MCP text, slog, SQLite history, HTML/JSON export, screenshots, video); (4) `HiddenKeypad` tap stays denied on real phones even in `credential_apps`; (5) `waiting`/`denied`/`unavailable`/`not_found` errors carry `fix:`. `cmd/pinky/pin_test.go`: RFC 6238 TOTP vectors, `_cred-deliver` UID check, mock `cred` `done` + `waiting`->`resume`. | `TestCredentialLeakGuardCatchesViolations`: injects a canary into each of the 6 surfaces one at a time (e.g., mutated `StepView.Args`, unmasked OTP in `Screen`, unredacted `slog`) and proves `assertNoSecretLeak` fails on every single one. |

---

## 5. Public Surface & Upstream Diff Budget (Question D)

### Gateway vs. Outside-Gateway Split
- **Inside `internal/gateway` & `internal/policy`**: Pure policy evaluation (`credential_apps`, `CredentialRef`), unlock orchestration + wrong-PIN HMAC latch, placeholder parsing, last-moment `CredentialResolver` call, and 6-surface scrubbing. Zero OS/CLI/subprocess coupling.
- **Outside `internal/gateway` (`internal/secret`, `cmd/pinky`)**: macOS `Security.framework` silent Keychain reads (`kSecUseAuthenticationUIFail`), `cred run`/`resume` subprocess + Unix socket `_cred-deliver` adapter, RFC 6238 TOTP computation, and interactive/file secret provisioning CLIs (`pinkyctl phone pin|cred`).

### File List & Estimated Diff

**Modified upstream files (~173 lines across 15 files):**

| File | Est. Diff | Purpose |
|---|---|---|
| `internal/gateway/gateway.go` | +8 | `Options.AutoUnlock`, `Options.Credentials`, `Gateway.pinSalt` |
| `internal/gateway/state.go` | +4 | `device.unlockFailed`, `lease.secretApps` |
| `internal/gateway/wake.go` | +8 | Extract `unlockDevice`, post-unlock `pauseVideoLocked`, autonomous step label |
| `internal/gateway/background.go` | +4 | Call `g.autoUnlockIfLocked(ctx, l)` in `readyScreen` |
| `internal/gateway/agent.go` | +12 | Post-observe `cur.Locked` auto-unlock once in `firstScreen` & `Observe` |
| `internal/gateway/pipeline.go` | +35 | `prep.secretRef`; `act` auto-unlock hooks; parse placeholder in `prepare`; mask `describe`; last-moment resolve + video pause + scrub + skip after-shot in `executeOn`; cover cred fields in `screenshot` |
| `internal/policy/file.go` | +5 | `File.CredentialApps` field + trim in `ParseFile` |
| `internal/policy/policy.go` | +3 | `Input.CredentialRef` field |
| `internal/policy/engine.go` | +12 | Lift `RuleCredentials` only for `CredentialRef && CredentialAppAllowed`; block placeholder on non-cred fields |
| `internal/policy/presets.go` | +18 | `CredentialApps`, `SetCredentialApps`, `CredentialAppAllowed`, `rulesHeader` |
| `internal/policy/defaults.yaml` | +5 | Document `credential_apps: []` |
| `cmd/pinky/daemon.go` | +14 | `PINKY_AUTO_UNLOCK=1` and `PINKY_CREDENTIALS=1` wiring |
| `cmd/pinky/phone.go` | +8 | Dispatch `pin` & `cred` subcommands, update `phoneUsage` |
| `cmd/pinky/main.go` | +2 | Hidden `_cred-deliver` internal subcommand |
| `docs/mcp.md` | +35 | Contract docs for autonomous unlock, `credential_apps`, `{{secret:...}}`, `phone pin|cred` |

**New fork-owned files (zero upstream merge conflict):**
- `internal/secret/autounlock.go` (~55), `autounlock_darwin.go` (~45), `autounlock_other.go` (~15), `autounlock_test.go` (~90)
- `internal/gateway/autounlock.go` (~150), `autounlock_test.go` (~240)
- `internal/gateway/creds.go` (~190), `creds_test.go` (~280)
- `internal/policy/credapps_test.go` (~120)
- `cmd/pinky/creds_resolver.go` (~260), `pin.go` (~220), `pin_test.go` (~180)

**Explicitly untouched upstream files (and why):**
- `internal/api/*`: No control RPC or socket protocol changes needed.
- `internal/mcp/tools.go` & `render.go`: Zero new MCP tools or schemas; `phone_type` and `phone_batch` work as-is.
- `internal/driver/ios/*`: `WDADevice.Unlock` already wakes, dismisses Touch ID prompt, taps pad, checks lock per digit, never retries, never logs PIN; `WDADevice.Type` already types via `/wda/keys` without pasteboard or logging.
- `internal/history/*`, `internal/approval/*`, `internal/recording/*`: Unchanged because secrets never enter `Action.Text`, steps, approvals, or unpaused video.
- `cmd/pinky/app.go`, `ui/*`: Desktop app keeps Touch ID `pinSource` (`app.go:441-470`) completely unchanged.
- `internal/secret/secret.go`, `get_test.go`: Upstream `secret.Get` PIN refusal contract remains 100% intact.

---

## 6. Tradeoffs, Risks & Open Questions

### Tradeoffs Accepted
- **We accept** a separate Keychain service (`app.pinky.iphone-control.pin`) **in exchange for** airtight isolation so personal-phone PINs saved in `Pinky.app` can never be read silently by the headless daemon.
- **We accept** strict single-token `{{secret:<ref>:<kind>}}` placeholders targeting verified `IsCredentialField` elements **in exchange for** eliminating exfiltration into Notes/Messages/URL bars and avoiding a 21st MCP tool.
- **We accept** omitting the `after` history screenshot on secret-typing steps and pausing video across `dev.Type` **in exchange for** guaranteeing no iOS last-character reveal or unmasked OTP pixels are persisted.
- **We accept** keeping `HiddenKeypad` (`not_in_screenshot` custom digit pads) blocked for `phone_tap` **in exchange for** never exposing app PIN digits to the agent.

### Risks
1. **Ad-hoc binary rebuilds invalidating macOS Keychain ACLs** (`keychain_darwin.go:24-27`): Rebuilding `pinky` with a fresh ad-hoc signature causes `GetSilent` to return `ErrInteractionNotAllowed (-25308)`. *Mitigation:* never hang; return actionable `fix:`; sign the installed fork binary with the Mac's existing Apple Development identity (or re-run `pinkyctl phone pin set`).
2. **Pre-WDA iOS automation passcode prompt (`driver.SetupPasscode`, `wda_device.go:183-187`)**: If WDA itself was killed and iOS asks for the passcode *before* WDA starts, `d.online` is false and `WDADevice.Unlock` cannot run — Pinky returns `device_needs_setup` (`setup: passcode`) for one-time human entry.
3. **Lockdown vs WDA lock-state lag (`wda_power.go:28-39`, `wda_device.go:601-609`, issue #7)**: If lockdown reports `PasswordProtected=false` while WDA's `/wda/locked` reports `true`, `lockedNow` in `wake.go:105` could skip unlocking. *Mitigation:* verify on the SE in Step 10; if reproduced, check `d.c.locked` when `lockdownLocked` says false inside `WDADevice`.
4. **CoreDevice pasteboard on future iOS 27+ (`internal/driver/ios/type.go:175-180`)**: Not used on the iOS 26.2 SE (WDA uses `/wda/keys`), but if the SE upgrades to iOS 27+, `Device.Type` pastes through the iPhone pasteboard (risking Universal Clipboard exposure). *Mitigation:* pass `TypeOptions.Sensitive` or force key-typing on CoreDevice before iOS 27 adoption.
5. **Custom OTP/password fields missed by `IsCredentialField`**: Non-standard Flutter/WebView password inputs lacking `StatePassword` or matching labels/IDs would be rejected by the anti-exfiltration guard. *Mitigation:* clear error naming the element's label/role; extend `policy/words.go` if a real app triggers a false negative rather than relaxing the guard.
6. **"Show password" toggle**: If an agent explicitly taps a password field's "Show" eye icon *after* typing, the password becomes visible on the next `phone_observe`. *Mitigation:* `lease.secretApps` masks `IsCredentialField` `Content` and covers its box in screenshots for the rest of the lease.

### Open Questions (for Owner / Synthesizer)
1. **`cred run` runtime behavior (UNPROVEN)**: What is `cred run`'s wait timeout before returning `waiting`, and does `cred` allow `pinky _cred-deliver <sock>` without policy friction — or should `cred` eventually expose a direct broker RPC / `phone-type` surface behind the same `CredentialResolver` interface?
2. **Unattended vs. approved credentials on the dedicated SE**: For apps in `credential_apps`, should `ChainResolver` check `KeychainCredResolver` first (unattended) and fall back to `CredBrokerResolver` (`cred`, owner-approved), or require an explicit prefix (`keychain:<name>` vs `lg_<id>`)?
3. **Banking/password-manager unblocking UX**: Is requiring the owner to list an explicitly allowed banking/password-manager app in `apps.yaml` `exclude:` (for whole-app visibility) AND `rules.yaml` `credential_apps:` (for credential typing) the right separation of concerns, or should `credential_apps` automatically lift `Finance apps`? *(Recommendation: keep them separate so `credential_apps` never silently exposes a bank app's screens.)*
4. **`PINKY_AUTO_UNLOCK=1` safety precondition**: Should `pinky daemon` refuse `PINKY_AUTO_UNLOCK=1` if `PINKY_ALLOW_DEVICES` is unset (hard fail) or merely log a warning (since `AutoUnlockService` already scopes PINs by `deviceID`)?

---

## 7. Numbered Build Order & Pass/Fail Tests

| Step | Deliverable | Pass / Fail Verification Command |
|---|---|---|
| **1** | **Silent Keychain service** (`internal/secret/autounlock*.go`, `autounlock_test.go`) | `go test -v ./internal/secret -run 'TestGetReadsOnlyModelKeys\|TestAutoUnlock'` — PASS: `AutoUnlockService != Service`, `Get` still refuses `pin:*`, `GetSilent` works on `Memory` & compiles with `kSecUseAuthenticationUIFail` on Darwin. |
| **2** | **Gateway autonomous unlock + HMAC latch** (`internal/gateway/autounlock.go`, `autounlock_test.go`, hooks in `gateway.go`, `state.go`, `wake.go`, `background.go`, `agent.go`, `pipeline.go`) | `go test -v -race ./internal/gateway -run 'TestAutoUnlock\|TestUnlock\|TestFreshLock'` — PASS: all 7 unlock guard assertions + `TestAutoUnlockGuardCatchesViolations` pass; upstream `TestUnlockKeepsAHandoffThatAsksForMore` & `TestFreshLock*` unchanged. |
| **3** | **Policy `credential_apps` & `CredentialRef`** (`internal/policy/file.go`, `policy.go`, `engine.go`, `presets.go`, `defaults.yaml`, `credapps_test.go`) | `go test -v ./internal/policy -run 'TestCredential\|TestVirtualCredentials'` — PASS: matrix guard passes, unlisted apps & `TestVirtualCredentials` unchanged, YAML round-trip & `Refresh` reload pass. |
| **4** | **Gateway secret placeholder, last-moment resolver & 6-surface masking** (`internal/gateway/creds.go`, `creds_test.go`, `pipeline.go`) | `go test -v -race ./internal/gateway -run 'TestCredential\|TestHiddenKeypad\|TestVirtualCredentials'` — PASS: 6-surface canary leak check + `TestCredentialLeakGuardCatchesViolations` + OTP masking + `HiddenKeypad` unchanged. |
| **5** | **CLI provisioning & resolver adapters** (`cmd/pinky/pin.go`, `creds_resolver.go`, `pin_test.go`, `phone.go`, `main.go`, `daemon.go`) | `go test -v ./cmd/pinky -run 'TestPin\|TestCred\|TestPhone'` — PASS: no-arg refusal, `--from-file` guaranteed deletion, RFC 6238 TOTP vectors, `_cred-deliver` socket + UID check, mock `cred` `done`/`waiting`->`resume`/`denied`, golden `TestPhoneStatusKeys` unchanged. |
| **6** | **Docs & full repo gate** (`docs/mcp.md`) | `make check` (`CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`) — PASS: zero build/vet/test failures across entire repo. |
| **7** | **Live iPhone SE verification (owner/operator run)** | (1) Save real SE passcode with `pinkyctl phone pin set --from-file ...`, lock SE, run `PINKY_AUTO_UNLOCK=1 pinkyctl daemon`, verify `pinkyctl phone call observe` wakes & unlocks SE and returns exit 0 with zero PIN in logs/history/video. (2) Verify wrong-PIN test on a disposable fake/test or latch unit check. (3) Add a test app to `credential_apps`, sign in using `{{secret:...}}` password + OTP, inspect history DB + screenshots + video to confirm masking. |
