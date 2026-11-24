# Autonomous edge-case handling for `iphone-control`

## Problem

An unattended agent driving a dedicated, non-jailbroken iPhone SE (iOS 26.2, WebDriverAgent helper) hits recurring modal interrupts, split-box one-time code inputs, transient link/tree failures, and human-blocking policy gates that currently force a person back into the loop. Grounding in `/Users/nqh/Desktop/iphone-control` surfaces six concrete constraints the design must honor:

1. **Why split-box code entry failed** (`internal/driver/ios/wda_device.go:776`, `wda.go:341,548`, `wda_observe.go:182`): `WDADevice.Type` runs `clearFocused` (`GET /element/active` + `/clear`) before sending all characters at `600 keys/s` via `/wda/keys`, which outruns Safari WebKit `<input maxlength="1">` focus jumps and can blur empty fields; meanwhile `wdaScreenElements` strips `XCUIElementTypeKeyboard` keys (`!inKeyboard`), leaving agents blind to the software keypad that WebKit reliably accepts.
2. **Split-digit code reading & disk-redaction gap** (`internal/policy/codes.go:47`, `internal/gateway/pipeline.go:1999`, `record.go:84`): Apple's 2FA modal renders each digit as a separate `StaticText` element at distinct `Bounds.X` (tree/OCR order is not guaranteed left-to-right), and when `mask_codes: false` (`HideCodes() == false`), `observeOCR` skips `MaskScreen`, so both single-element and split-digit codes are persisted into SQLite `history.Store` (`ScreenText`) and history screenshots.
3. **Secret/code masking coupling** (`internal/gateway/secrets.go:36`, `codes.go:57`): `secretReady` refuses `{{secret:NAME}}` whenever `!g.policy.HideCodes()`, solely because `coverCodes` early-returns on `!HideCodes()` instead of covering vault-masked (`e.Masked`) elements unconditionally.
4. **Policy `Ask` hangs unattended runs** (`internal/policy/presets.go:78-103`, `engine.go:244`): Every preset except `PresetNone (0)` emits `Decision: Ask` for Payments, Erase iPhone, Sign Out/Delete Account, and Turn Passcode Off (hanging 60 s until timeout), while `PresetNone` drops all four at once; strictest-wins evaluation means a preset `Deny` cannot be overridden by a file `Allow`.
5. **Blind `phone_wait_for` and brittle link/tree recovery** (`internal/gateway/agent.go:1518`, `gateway.go:812`, `internal/driver/ios/wda_tree.go:42`): `WaitFor` neither clears known modal interrupts nor short-circuits on error states (`No Internet Connection`, `Incorrect verification code`); `setOffline` only arms `dropWait` on USB-to-Wi-Fi transitions (not Wi-Fi drops or WDA runner restarts in the 2 s discovery gap); and `wdaTrees` locks an app into `treeNone` (OCR-only) for 2 minutes even after navigating to a small modal.
6. **Seam & neutrality invariants** (`internal/forkseams/seams.yaml`): Credentials never appear in agent output or logs; the gateway is the only place a secret meets the driver; any new store reader ships with a `seams.yaml` entry and fixture test; generic mechanisms carry neutral names with vendor specifics as YAML data.

---

## Usage (caller's view)

The agent-facing surface adds **one** MCP tool (`phone_enter_code`), **one** optional `profile` parameter on `phone_connect` (defaulting to the daemon's configured profile, e.g. `autonomous`), optional `fail_on` on `phone_wait_for`, and **one** CLI inspection verb (`pinkyctl phone profile`). Because `cmd/pinky/phone.go` discovers tools dynamically from `tools/list`, `pinkyctl phone call enter_code` works automatically with no CLI wrapper code.

### Quickstart (daemon configuration, set once by owner)

```yaml
# $PINKY_HOME/config/rules.yaml
preset: 4                        # PresetAutonomous: zero blocking human Ask gates
mask_codes: false                # agent may read codes on screen; disk/logs still redact them
credential_apps:
  - com.apple.mobilesafari
  - com.apple.AppStore
  - com.apple.Preferences
  - com.apple.springboard
```

```sh
# Inspect active screen-guard rules and simulate against a saved screen dump
pinkyctl phone profile list
pinkyctl phone profile check -screen /tmp/screen.txt
```

### Call site 1: Sign in to `account.apple.com` in Safari with same-device 2FA

The agent's script never mentions `"Sign In Requested"`, `"Allow"`, the 6-digit modal, `"OK"`, `"Save Password?"`, or passkey sheets. The gateway's `ScreenGuard` resolves them automatically during `wait_for` and actions, capturing the 6-digit code in-memory (sorted left-to-right) before dismissing `"OK"`.

```sh
pinkyctl phone run -task "Sign in to account.apple.com" -e '
  pinkyctl phone call launch app="https://account.apple.com"
  pinkyctl phone call wait_for text="Email or Phone Number" timeout_ms=20000
  pinkyctl phone call tap ref=12
  pinkyctl phone call type text="$AGENT_APPLE_ID" submit=true
  pinkyctl phone call wait_for text="Password" timeout_ms=15000
  pinkyctl phone fill -account apple-agent -submit

  # While waiting for the 6-box page, ScreenGuard automatically:
  #   1) taps "Allow" on "Sign In Requested"
  #   2) captures the 6 split-element digits left-to-right into the lease CodeBuffer
  #   3) taps "OK" on the verification code modal
  #   4) dismisses any Passkey or "Save Password?" sheet
  pinkyctl phone call wait_for any="[\"Two-Factor Authentication\",\"Enter the verification code\"]" \
    fail_on="[\"Account Locked\",\"Cannot Sign In\"]" timeout_ms=30000

  # One verb: detects the 6 split inputs, enters digits 1..5 via software keypad,
  # proves boxes 1..5 match the captured prefix before entering digit 6, then commits.
  pinkyctl phone call enter_code source=captured

  pinkyctl phone call wait_for text="Sign-In and Security" timeout_ms=25000
'
```

Typical output from `wait_for` and `enter_code`:

```text
settled · iPhone SE · matched: true · handled: auth.allow_prompt, auth.capture_code_modal
“Two-Factor Authentication” appeared after 4 s.
screen 750x1334 · app: Safari (com.apple.mobilesafari) · portrait · keyboard hidden
[41] input "Digit 1" @112,520
[42] input "Digit 2" @218,520
...
```

```text
settled · iPhone SE · verified: 6/6 · channel: keypad
Entered the 6-digit code from the captured buffer and verified before submit.
screen 750x1334 · app: Safari (com.apple.mobilesafari) · portrait · keyboard hidden
...
```

### Call site 2: First-run app onboarding + App Store password prompt

```sh
pinkyctl phone run -task "Install test app" -e '
  pinkyctl phone call launch app="com.apple.AppStore"
  # First-run "Personalised Ads" (disabled button until scrolled) and
  # "Allow Notifications" are cleared automatically during wait_for.
  pinkyctl phone call wait_for text="Search" timeout_ms=20000
  pinkyctl phone call tap ref=5
  pinkyctl phone call type text="TestFlight" submit=true
  pinkyctl phone call tap ref=18
  # App Store "Require Password" modal matches the profile fill_account rule
  # and stages the saved agent account password through the gateway vault.
  pinkyctl phone call wait_for text="Open" fail_on="[\"Cannot Connect to App Store\"]" timeout_ms=45000
'
```

### Call site 3: Explicit code entry (when an agent obtained a code from another channel)

```sh
pinkyctl phone call enter_code code="482913" submit=true
```

---

## Module map

```text
/Users/nqh/Desktop/iphone-control/
├── internal/
│   ├── driver/
│   │   ├── driver.go                 (edit: add optional Keypad capability interface)
│   │   └── ios/
│   │       ├── wda_keypad.go         (new: WDADevice.KeypadKeys via XCUIElementTypeKeyboard)
│   │       ├── wda_tree.go           (edit: fast shallow re-probe on screen/modal change)
│   │       └── provider.go           (edit: per-transport health + USB/Wi-Fi fallback)
│   ├── guard/                        (new: neutral declarative screen-interrupt engine)
│   │   ├── profile.go                (Profile, ScreenRule, ScreenMatch, RuleOp, YAML loader)
│   │   ├── match.go                  (pure predicate evaluator over *screen.Screen + OCR)
│   │   ├── engine.go                 (bounded Resolve loop, cycle/fire-count guard, Executor seam)
│   │   └── profiles/
│   │       └── default.yaml          (embedded neutral+iOS data profile; user overlay in config)
│   ├── codeentry/                    (new: code extraction, ephemeral CodeBuffer, N-1 entry)
│   │   ├── extract.go                (grouped + split-row X-sorted extractor, pure functions)
│   │   ├── buffer.go                 (per-lease in-memory CodeBuffer: TTL, generation, wipe)
│   │   └── enter.go                  (box detection, N-1 commit barrier, keypad/key fallback)
│   ├── policy/
│   │   ├── policy.go                 (edit: add OpEnterCode)
│   │   ├── detect.go                 (edit: split IsPasswordField vs IsCodeField; $0 payment check)
│   │   ├── codes.go                  (edit: add MaskSplitDigits for disk redaction)
│   │   ├── presets.go                (edit: split RuleDeleteAccount vs RuleSignOut; PresetAutonomous)
│   │   └── file.go                   (edit: GateOverrides on File; OpEnterCode in knownAction)
│   ├── gateway/
│   │   ├── gateway.go                (edit: Options.Guard, Options.AccountPassword; lease CodeBuffer)
│   │   ├── pipeline.go               (edit: pre/post guard hook; link-drop read recovery)
│   │   ├── agent.go                  (edit: WaitFor interrupt resolution + fail_on + offline retry)
│   │   ├── codeentry.go              (new: Session.EnterCode wiring policy + driver.Keypad + buffer)
│   │   ├── guard.go                  (new: gateway Executor impl — routes every op through policy!)
│   │   ├── secrets.go                (edit: decouple secret masking from HideCodes; account filler)
│   │   ├── codes.go                  (edit: cover e.Masked unconditionally; disk-only redaction)
│   │   ├── record.go                 (edit: redactForDisk on Step.Args, Text, ScreenText, shots)
│   │   └── wifi.go                   (edit: trigger discoverSoon + wait on Wi-Fi/WDA runner drop)
│   ├── mcp/
│   │   ├── tools.go                  (edit: register phone_enter_code; profile on connect; fail_on on wait)
│   │   └── render.go                 (edit: render handled: and recovered: status tokens)
│   └── forkseams/
│       ├── seams.yaml                (edit: guard AccountPassword wiring & CodeBuffer/disk seam)
│       └── forkseams_test.go         (edit: add fixture tests for new seams)
└── cmd/pinky/
    ├── daemon.go                     (edit: wire guard.Load + newAccountPasswords)
    ├── autologins.go                 (new: wire gateway.Options.AccountPassword from agentLogins)
    ├── phone.go                      (edit: add `profile` subcommand)
    └── phone_profile.go              (new: `pinkyctl phone profile list|check`)
```

**Ownership & dependency direction:**
- `internal/guard` depends ONLY on `internal/screen` and `internal/codeentry` (for `Extract`). It never imports `internal/driver`, `internal/gateway`, `internal/policy`, or `internal/secret`.
- `internal/codeentry` depends ONLY on `internal/screen`. It drives input through a narrow `Actor` interface implemented by `internal/gateway`.
- `internal/gateway` owns policy enforcement, lease state (`CodeBuffer`, `GuardState`), transport recovery, and disk redaction. Every `guard.RuleOp` executes through `gateway`'s policy-checked executor—a profile rule can never bypass `policy.Engine`.
- `cmd/pinky/autologins.go` is the sole new reader of `agentLogins`, wired into `gateway.Options.AccountPassword` mirroring `cmd/pinky/autounlock.go` -> `gateway.Options.PIN`.

---

## Type and signature sketch

All bodies return `not implemented`; behavioral invariants are summarized beside each contract rather than inline in code.

### 1. Driver capability (`internal/driver/driver.go`, `internal/driver/ios/wda_keypad.go`)

```go
package driver

import (
	"context"

	"github.com/DIYgod/pinky/internal/screen"
)

type Keypad interface {
	KeypadKeys(ctx context.Context) (map[rune]screen.Point, error)
}
```

- `WDADevice.KeypadKeys` reads WDA's tree (`d.source`), walks `XCUIElementTypeKeyboard` nodes (which `wdaScreenElements` hides from agents), and returns pixel-space centers for `'0'..'9'` and `'\b'` (Delete). Returns an empty map with `nil` error when no numeric keypad is visible.

### 2. Declarative screen-interrupt engine (`internal/guard`)

```go
package guard

import (
	"context"
	"errors"
	"time"

	"github.com/DIYgod/pinky/internal/codeentry"
	"github.com/DIYgod/pinky/internal/screen"
)

var ErrNotImplemented = errors.New("not implemented")

type OpKind string

const (
	OpTap                OpKind = "tap"
	OpScrollUntilEnabled OpKind = "scroll_until_enabled"
	OpCaptureCode        OpKind = "capture_code"
	OpFillAccount        OpKind = "fill_account"
	OpRetryWait          OpKind = "retry_wait"
	OpAbort              OpKind = "abort"
)

type Profile struct {
	Name        string       `yaml:"name"`
	MaxPerCall  int          `yaml:"max_per_call,omitempty"`
	Rules       []ScreenRule `yaml:"rules"`
}

type ScreenRule struct {
	ID       string        `yaml:"id"`
	Priority int           `yaml:"priority,omitempty"`
	MaxFires int           `yaml:"max_fires,omitempty"`
	Cooldown time.Duration `yaml:"cooldown,omitempty"`
	When     ScreenMatch   `yaml:"when"`
	Do       []RuleOp      `yaml:"do"`
}

type ScreenMatch struct {
	Apps        []string            `yaml:"apps,omitempty"`
	AllText     []string            `yaml:"all_text,omitempty"`
	AnyText     []string            `yaml:"any_text,omitempty"`
	NoneText    []string            `yaml:"none_text,omitempty"`
	HasElement  *ElementMatch       `yaml:"has_element,omitempty"`
	HasCode     *codeentry.CodeSpec `yaml:"has_code,omitempty"`
}

type ElementMatch struct {
	Role     string   `yaml:"role,omitempty"`
	LabelAny []string `yaml:"label_any,omitempty"`
	State    string   `yaml:"state,omitempty"`
	MinCount int      `yaml:"min_count,omitempty"`
}

type RuleOp struct {
	Kind       OpKind        `yaml:"op"`
	LabelAny   []string      `yaml:"label_any,omitempty"`
	Role       string        `yaml:"role,omitempty"`
	Direction  string        `yaml:"direction,omitempty"`
	MaxSwipes  int           `yaml:"max_swipes,omitempty"`
	Account    string        `yaml:"account,omitempty"`
	Submit     bool          `yaml:"submit,omitempty"`
	Code       *codeentry.CodeSpec `yaml:"code,omitempty"`
	Backoff    time.Duration `yaml:"backoff,omitempty"`
	Reason     string        `yaml:"reason,omitempty"`
}

type Executor interface {
	Observe(ctx context.Context) (*screen.Screen, error)
	TapElement(ctx context.Context, e screen.Element) error
	Swipe(ctx context.Context, direction string) error
	FillAccount(ctx context.Context, account string, field *screen.Element, submit bool) error
	StoreCode(code codeentry.CapturedCode)
}

type LeaseState struct {
	Fires     map[string]int
	LastFired map[string]time.Time
}

type Outcome struct {
	Screen      *screen.Screen
	Handled     []string
	AbortedBy   string
	AbortReason string
	LoopStopped bool
}

type Engine struct {
	profiles map[string]Profile
}

func Load(configPath string) (*Engine, error) {
	return nil, ErrNotImplemented
}

func (e *Engine) Match(profileName string, s *screen.Screen) (*ScreenRule, bool) {
	panic("not implemented")
}

func (e *Engine) Resolve(ctx context.Context, profileName string, st *LeaseState, cur *screen.Screen, ex Executor) (Outcome, error) {
	return Outcome{}, ErrNotImplemented
}
```

**`Resolve` control flow (bounded, cycle-proof, policy-checked):**
1. Sort rules in `profileName` by `Priority` descending (done once at `Load`).
2. Loop up to `profile.MaxPerCall` (default `4`), respecting `ctx` budget:
   - Compute a structural `fingerprint(cur)` (app + sorted labels + disabled states).
   - Pick the highest-priority `ScreenRule` where `Match(cur)` is true, `st.Fires[rule.ID] < rule.MaxFires`, and `time.Since(st.LastFired[rule.ID]) >= rule.Cooldown`.
   - If none matches, return `Outcome{Screen: cur, Handled: handled}`.
   - If the rule's first op is `OpAbort`, record `AbortedBy = rule.ID`, `AbortReason = op.Reason`, and return immediately with zero taps.
   - Execute `rule.Do` sequentially through `ex` (each `TapElement`, `Swipe`, `FillAccount` runs through `Gateway`'s normal `policy.Engine.Evaluate`—a `Deny` stops resolution and surfaces normally).
   - For `OpTap`: locate element by `LabelAny` + `Role` in tree first, falling back to `SourceOCR` bounds when `cur.TreeTooSlow`.
   - For `OpScrollUntilEnabled`: while target element exists with `StateDisabled` (or is not yet visible) up to `MaxSwipes`, call `ex.Swipe(ctx, op.Direction)` and `ex.Observe(ctx)`.
   - For `OpCaptureCode`: run `codeentry.Extract(cur, *op.Code)` and call `ex.StoreCode(captured)`.
   - Re-observe `next`. If `fingerprint(next) == fingerprint(cur)` (the rule fired but nothing changed), set `LoopStopped = true` and return immediately so it never loops.

### 3. One-time code extraction, buffer, and `N-1` commit-guard entry (`internal/codeentry`)

```go
package codeentry

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

var (
	ErrNotImplemented = errors.New("not implemented")
	ErrNoCode         = errors.New("codeentry: no code available")
	ErrCodeExpired    = errors.New("codeentry: captured code expired")
	ErrCodeConsumed   = errors.New("codeentry: captured code already submitted")
	ErrBoxesNotFound  = errors.New("codeentry: code input boxes not found")
	ErrPrefixMismatch = errors.New("codeentry: pre-submit verification failed before final digit")
	ErrAttemptLimit   = errors.New("codeentry: max code attempts reached for this screen")
)

type Layout string

const (
	LayoutAny      Layout = "any"
	LayoutInline   Layout = "inline"
	LayoutRowSplit Layout = "row_split"
)

type CodeSpec struct {
	Digits     int      `yaml:"digits,omitempty"`
	Layout     Layout   `yaml:"layout,omitempty"`
	AnchorText []string `yaml:"anchor_text,omitempty"`
}

type CapturedCode struct {
	Digits     string
	CapturedAt time.Time
	Generation uint64
	Source     string
	FromOCR    bool
}

type Buffer struct {
	mu          sync.Mutex
	current     []byte
	capturedAt  time.Time
	generation  uint64
	consumed    bool
	maxAge      time.Duration
	attempts    map[string]int
	maxAttempts int
}

func NewBuffer(maxAge time.Duration, maxAttempts int) *Buffer {
	panic("not implemented")
}

func (b *Buffer) Put(c CapturedCode) {
	panic("not implemented")
}

func (b *Buffer) Invalidate() {
	panic("not implemented")
}

func (b *Buffer) Peek(now time.Time) (string, uint64, error) {
	return "", 0, ErrNotImplemented
}

func (b *Buffer) MarkConsumed(gen uint64, screenKey string) {
	panic("not implemented")
}

func (b *Buffer) Wipe() {
	panic("not implemented")
}

func Extract(s *screen.Screen, spec CodeSpec) (CapturedCode, bool) {
	panic("not implemented")
}

type Boxes struct {
	Split    bool
	Elements []screen.Element
}

func DetectBoxes(s *screen.Screen, digits int, targetRef int) (Boxes, error) {
	return Boxes{}, ErrNotImplemented
}

type Actor interface {
	Observe(ctx context.Context) (*screen.Screen, error)
	TapPoint(ctx context.Context, p screen.Point) error
	TypeDigit(ctx context.Context, digit rune) error
	KeypadKeys(ctx context.Context) (map[rune]screen.Point, error)
}

type EnterRequest struct {
	Code        string
	TargetRef   int
	Submit      bool
	ScreenKey   string
	Buffer      *Buffer
	Generation  uint64
}

type Report struct {
	Digits        int
	VerifiedCount int
	Channel       string
	AutoSubmitted bool
	Submitted     bool
	FinalScreen   *screen.Screen
}

func Enter(ctx context.Context, req EnterRequest, cur *screen.Screen, a Actor) (Report, error) {
	return Report{}, ErrNotImplemented
}
```

**`Extract` + `Enter` invariants:**
1. **`Extract` (pure function):** Excludes status bar (`Y < H*6/100`) and `KeyboardFrame`. For `LayoutRowSplit`, finds `spec.Digits` elements (tree preferred over OCR) whose trimmed label is a single ASCII digit `'0'..'9'` (or two 3-digit groups), lying on one horizontal band (`|cy_i - medianY| <= 0.75 * medianH`) with similar height and monotonic X-spacing, then sorts strictly by `Bounds.X` ascending. When OCR is the only source, `Resolve` requires two consecutive identical reads before storing.
2. **Pre-submit proof barrier (`Enter`):**
   - Checks `Buffer.attempts[screenKey] < maxAttempts` (default `2`) and `Generation` still matches before touching any box.
   - Detects `Boxes` (`Split: true` = `N` `RoleInput`/`StateEditable` elements sorted by `Bounds.X`; or `Split: false` = 1 `RoleInput`).
   - Taps box `0` to focus and calls `a.KeypadKeys(ctx)`. If `'0'..'9'` are present, chooses `channel = "keypad"`; otherwise `channel = "keys"` (single-char `Type(Append: true, Submit: false)` so WDA never runs `clearFocused` or blasts 600 keys/s).
   - Enters digit `0` and observes once: if box `0` stays empty, switches channel once (keypad <-> keys); if still empty, aborts immediately without entering remaining digits.
   - Enters digits `1 .. N-2` (for a 6-digit code, digits 2 through 5), tapping box `i` only if focus did not auto-advance.
   - **Barrier before digit `N-1` (the 6th digit, which triggers auto-submit on Apple ID and most web forms):** Re-reads the screen and verifies every visible box `0..N-2` matches `code[i]` in order (or for masked boxes, `N-1` filled and box `N-1` empty), AND verifies `Buffer` generation was not invalidated/expired. If any box disagrees: **stops before digit `N`**, clears with Delete keys, and returns `ErrPrefixMismatch` (zero wrong-code submissions, never taps `"Resend"`).
   - Enters final digit `code[N-1]`, marks `Buffer.MarkConsumed`, wipes `Buffer`, waits for settle, and if a Submit button remains active with all `N` boxes verified, taps/submits when `req.Submit` is true.

### 4. Policy, Gateway & MCP additions

```go
package policy

const OpEnterCode Op = "enter_code"

const (
	RuleDeleteAccount = "Delete Account"
	RuleSignOut       = "Sign Out"
)

const PresetAutonomous Preset = 4

type GatePolicy map[string]Decision

func IsPasswordField(e *screen.Element) bool { panic("not implemented") }
func IsCodeField(e *screen.Element) bool     { panic("not implemented") }
func MaskSplitDigits(els []screen.Element) ([]screen.Element, int) {
	panic("not implemented")
}
```

```go
package gateway

import (
	"context"

	"github.com/DIYgod/pinky/internal/guard"
)

type AccountPasswordFunc func(ctx context.Context, account string) (string, error)

type EnterCodeRequest struct {
	Call
	Source string
	Code   string
	Ref    int
	Submit *bool
}
```

- `gateway.Options` adds:
  - `Guard *guard.Engine`
  - `DefaultProfile string` (default `"autonomous"` when `PresetAutonomous` is active)
  - `AccountPassword AccountPasswordFunc` (wired in `cmd/pinky/autologins.go`, guarded by `seams.yaml`)
- `gateway.ConnectRequest` adds `Profile string` (`""` = daemon default, `"none"` = disable guards for this lease).
- `gateway.WaitForRequest` adds `FailOn []string` (short-circuits immediately with `matched: false` if any string appears).
- `gateway.Result` adds `Handled []string` and `Recovered []string` rendered in `StatusLine` (`handled: ... · recovered: ...`).
- `gateway.Session.EnterCode(ctx, EnterCodeRequest) *Result`:
  - Checks `policy.Evaluate` with `OpEnterCode` (always denied on `IsPasswordField`; allowed on `IsCodeField` when `!HideCodes()`).
  - Resolves code from `lease.codeBuf` (`source: "captured"`) or `req.Code` (`source: "literal"`), runs `codeentry.Enter`, and passes `"••••••"` to `recordStep` so the code never touches SQLite or `slog`.
- **Disk redaction seam (`internal/gateway/record.go` + `codes.go` + `secrets.go`):**
  - `redactForDisk(s *screen.Screen) string` runs `policy.MaskElements` + `policy.MaskSplitDigits` + `Vault.Mask` unconditionally before writing `Step.ScreenText` to `history.Store`.
  - `coverCodes` in `codes.go` always covers `e.Masked` boxes (which includes vault-typed secrets) even when `HideCodes()` is false; only `pictureCodes` OCR scanning for unmasked OTPs is skipped when `!HideCodes()` for agent screenshots (and still runs for history screenshots written to disk).
  - `secrets.go:36` drops the `!g.policy.HideCodes()` block once `coverCodes` covers `e.Masked` unconditionally, so `phone fill -account` and `mask_codes: false` work together cleanly.

---

## Declarative profile data (`internal/guard/profiles/default.yaml`)

All vendor-specific prompts live here as data—zero Apple strings in `internal/guard/*.go`:

```yaml
name: autonomous
max_per_call: 4
rules:
  - id: net.offline
    priority: 100
    max_fires: 1
    when:
      any_text: ["No Internet Connection", "not connected to the Internet", "Cannot Connect to App Store"]
    do:
      - op: retry_wait
        label_any: ["Try Again", "Retry"]
        backoff: 2s

  - id: net.offline_abort
    priority: 95
    when:
      any_text: ["No Internet Connection", "not connected to the Internet", "Cannot Connect to App Store"]
    do:
      - op: abort
        reason: network_offline

  - id: auth.lockout_abort
    priority: 100
    when:
      any_text: ["Account Locked", "Apple Account Has Been Locked", "Too Many Verification Codes"]
    do:
      - op: abort
        reason: account_locked

  - id: auth.capture_code_modal
    priority: 90
    max_fires: 3
    cooldown: 3s
    when:
      any_text: ["Verification Code", "Apple Account Verification Code"]
      has_element: { role: button, label_any: ["OK"] }
      has_code: { digits: 6, layout: row_split }
    do:
      - op: capture_code
        code: { digits: 6, layout: row_split }
      - op: tap
        role: button
        label_any: ["OK"]

  - id: auth.allow_prompt
    priority: 85
    max_fires: 3
    cooldown: 3s
    when:
      any_text: ["Sign In Requested", "Apple ID Sign In Requested", "Apple Account Sign In Requested"]
      has_element: { role: button, label_any: ["Allow"] }
    do:
      - op: tap
        role: button
        label_any: ["Allow"]

  - id: auth.account_sheet_continue
    priority: 75
    max_fires: 2
    cooldown: 3s
    when:
      any_text: ["with Apple Account", "Sign in to"]
      has_element: { role: button, label_any: ["Continue"] }
    do:
      - op: tap
        role: button
        label_any: ["Continue"]

  - id: auth.store_password
    priority: 70
    max_fires: 2
    cooldown: 5s
    when:
      apps: ["com.apple.AppStore", "com.apple.springboard", "com.apple.Preferences"]
      any_text: ["Password", "Sign In to iTunes Store", "Require Password"]
      has_element: { role: input, state: password }
    do:
      - op: fill_account
        account: default
        submit: true

  - id: sheet.scroll_consent
    priority: 65
    max_fires: 3
    when:
      any_text: ["Personalised Ads", "Personalized Ads", "Data & Privacy"]
      has_element: { role: button, label_any: ["Turn Off Personalised Ads", "Turn Off Personalized Ads", "Continue"], state: disabled }
    do:
      - op: scroll_until_enabled
        direction: up
        label_any: ["Turn Off Personalised Ads", "Turn Off Personalized Ads", "Continue"]
        max_swipes: 6
      - op: tap
        role: button
        label_any: ["Turn Off Personalised Ads", "Turn Off Personalized Ads", "Continue"]

  - id: sheet.passkey_dismiss
    priority: 60
    max_fires: 3
    when:
      any_text: ["Passkey", "Use a Passkey", "Sign in with a passkey"]
      has_element: { role: button, label_any: ["Cancel", "Not Now"] }
    do:
      - op: tap
        role: button
        label_any: ["Cancel", "Not Now"]

  - id: sheet.save_password_dismiss
    priority: 55
    max_fires: 3
    when:
      any_text: ["Save Password", "Update Password", "Would you like to save this password"]
      has_element: { role: button, label_any: ["Not Now", "Never for This Website"] }
    do:
      - op: tap
        role: button
        label_any: ["Not Now"]
```

---

## Preset gates under "no loop" (owner decision table & default)

In `PresetAutonomous (4)`, **no gate uses `Decision: Ask`** (which hangs 60 s waiting for a human and times out as `approval_denied`). Each of the four existing preset gates resolves immediately to `Allow` or `Deny`, and `RuleAppleAccount` is split into two rules because `"Delete Account"` and `"Sign Out"` have completely different reversibility:

| Gate (`internal/policy/presets.go`) | Reversibility | Recommended default in `PresetAutonomous` | Tradeoff & owner option |
|---|---|---|---|
| **Erase All Content and Settings** (`RuleEraseDevice`) | Irreversible (wipes WDA, trust pairing, Wi-Fi; needs physical hands) | **Keep as hard `Deny`** | Zero cost to autonomous workflows; prevents a misclick from bricking the unattended phone. Owner can override in `rules.yaml` only if intentionally testing resets. |
| **Delete Account** (split from `RuleAppleAccount`) | Irreversible (destroys Apple Account & developer state) | **Keep as hard `Deny`** | Same: irreversible account destruction has no unattended use case. |
| **Turn Passcode Off** (`RulePasscode`) | Technically reversible, but breaks Keychain/2FA trust and `autoUnlock` | **Keep as hard `Deny`** | Removing passcode disables iCloud Keychain / passkeys / device-trusted 2FA and invalidates `phone pin`. Owner can switch to `Allow` via `gate_overrides` if intentionally running a passcode-free phone. |
| **Sign Out** (split from `RuleAppleAccount`) | **Reversible** (agent has `phone fill -account` + 2FA `enter_code`) | **Reversible safeguard: `Allow` with audit tag & never auto-tapped by `ScreenGuard`** | Lets unattended agents re-authenticate or switch test Apple Accounts; risk is temporary loss of App Store/iCloud state until re-signed-in. Owner can flip to `Deny` via `gate_overrides: { "Sign Out": deny }`. |
| **Payments** (`Payment`) | Irreversible financial spend (but today false-positives on free `"Get"` when an IAP price is on screen) | **Refine `$0`/free to `Allow`; keep nonzero `Payment` as hard `Deny`** | Unblocks free App Store installs (`"Get"` / `"$0.00"` / `"Free"`) without risking real charges. Owner who keeps **no payment method** on the dedicated agent Apple Account can set `gate_overrides: { "Payments": allow }`. |

---

## Failure and recovery: self-heal vs report

| Failure class | Tool recovers itself (reported in `recovered:` / `handled:`) | Tool stops and reports immediately |
|---|---|---|
| **Phone locked** | `autoUnlock` (1 attempt per saved PIN; existing behavior). | Wrong PIN (`pin_refused`), Keychain locked/denied (`device_locked` + `fix:`). |
| **Link drop (USB <-> Wi-Fi or WDA runner crash)** | On **reads** (`Observe`, `WaitFor`, pre-action `startScreen`): trigger `discoverSoon()`, wait up to `min(moveWait, budgetLeft)` for reconnect (now armed on Wi-Fi/WDA drops too, not only USB->Wi-Fi), fall back to the alternate `usbmuxd` transport (USB <-> Wi-Fi) if the current one fails 2 consecutive `Open`s, then retry the read once. | **Mid-mutation drop** (after `perform` sent): never blindly retry—wait briefly for reconnect if budget allows, do 1 read to show ground truth, return `StatusUncertain`. Offline past `ReconnectGrace` -> `ErrDeviceOffline`. |
| **Slow accessibility tree (`TreeTooSlow`)** | `ScreenGuard` matches & taps via `SourceOCR` bounds; `wdaTrees` downgrades `wdaSlowFor` reset on dramatic screen/OCR change and tries a fast shallow probe (`depth: 15`, 3 s timeout) so new modals regain refs quickly. | If both tree AND OCR picture fail after 1 retry within budget -> `ErrInternal` with `fix:`. |
| **Known modal / consent / 2FA interrupt** | `ScreenGuard.Resolve` runs in `act`, `WaitFor`, and `Observe` (when not read-only), bounded by `max_per_call` (4), `max_fires`, `cooldown`, and screen-fingerprint cycle detection. | Unmatched modal, `LoopStopped` (tap produced no screen change), or `max_fires` hit -> return current screen + `handled:` trace to calling agent. |
| **Split-box OTP entry** | Auto-detects `N` boxes, prefers software `KeypadKeys`, falls back once to single-char `Type(Append: true)` if digit 0 leaves box empty, verifies boxes `0..N-2` before entering digit `N`. | `ErrPrefixMismatch` at `N-1` barrier (clears boxes, never enters digit `N`, never taps Resend); `ErrCodeExpired`; `ErrCodeConsumed`; `ErrAttemptLimit`. |
| **Wi-Fi with no internet** | `net.offline` rule taps `"Try Again"` once after 2 s backoff. Never toggles device Wi-Fi settings (which would sever a Wi-Fi-controlled WDA link). | Second hit fires `net.offline_abort` -> short-circuits `WaitFor` with `matched: false · reason: network_offline`. |
| **`phone_wait_for` mismatch / screen changed** | Clears matching interrupts during polling so transient sheets don't starve the wait. | Any `fail_on` string or profile `OpAbort` rule terminates immediately with `matched: false` and the full screen. |
| **Unrecoverable host/device setup** | None (cannot self-heal without physical hands or Mac admin auth). | Immediate `StatusNeedsSetup` (`trust`, `developer_mode`, `passcode` for WDA automation, `device_service`, `install_helper`). |

---

## Seam and guard additions (`internal/forkseams/seams.yaml`)

1. **Extend `no-prompt login store variable` (`agentLogins`):**
   - Add `cmd/pinky/autologins.go` to `allowed` (alongside `phone_login.go`, `phone_fill.go`) so only daemon wiring reads saved agent account passwords into `gateway.Options.AccountPassword`.
2. **Add seam `account-password-handout`:**
   - Package: `github.com/DIYgod/pinky/internal/gateway`, ident/method: `AccountPassword` (or helper method `stageAccountSecret`) -> `allowed: [internal/gateway/secrets.go]` so saved account passwords only enter the driver through the vault + masking path, never in `guard` or `mcp`.
3. **Add seam `code-buffer-peek`:**
   - Package: `github.com/DIYgod/pinky/internal/codeentry`, method: `Peek` -> `allowed: [internal/gateway/codeentry.go]` so captured codes are never read by logging, history, or status endpoints.
4. **Add seam `guard-policy-bypass`:**
   - Verify via `forkseams` (or `internal/guard` package import rule) that `internal/guard` never imports `internal/driver` or `internal/secret`, making policy bypass by a profile rule a compile/check failure.
5. **Fixture tests in `internal/forkseams/testdata/`:**
   - `testdata/codepeek/` (non-allowed file calling `.Peek()` fails with `seam-code-buffer-peek` + `fix:` line).
   - `testdata/guarddriver/` (`internal/guard` importing `internal/driver` fails).

---

## Test plan

All tests run deterministically with zero live phone, zero credentials, and zero network:

```sh
# 1. Pure unit tests: declarative matcher, cycle/max_fires guard, scroll-until-enabled, abort short-circuit
go test -v ./internal/guard/...

# 2. Pure unit tests: split-row X-sorting extractor, CodeBuffer TTL/generation/invalidation,
#    N-1 commit barrier (proves digit 6 is NEVER sent on prefix mismatch, Resend NEVER tapped)
go test -v ./internal/codeentry/...

# 3. Policy tests: PresetAutonomous emits zero Ask rules; Delete Account vs Sign Out split;
#    $0/Free payment allow vs nonzero payment deny; OpEnterCode allowed on IsCodeField when !HideCodes
#    and ALWAYS denied on IsPasswordField; MaskSplitDigits redacts 6 separate digit elements
go test -v ./internal/policy/...

# 4. Gateway integration tests (fake driver + fake Keypad + simulated WDA drop & TreeTooSlow):
#    - end-to-end 2FA fixture: Allow prompt -> 6 split-digit modal -> OK -> 6 Safari boxes via Keypad
#    - canary test: with mask_codes=false, history SQLite DB + slog + history PNG contain ZERO digits of code
#    - vault secret works with mask_codes=false and screenshot still covers vault-typed bounds
#    - WaitFor resolves interrupt mid-wait and short-circuits on fail_on / network_offline
#    - link drop mid-Observe/WaitFor recovers; link drop mid-Tap returns uncertain (never retries tap)
go test -v -run 'TestGuard|TestEnterCode|TestAutonomous|TestDiskRedaction|TestLinkRecovery' ./internal/gateway/...

# 5. Seam guard & full repo check (must print PASS with zero violations)
go test -v ./internal/forkseams/...
make check

# 6. CLI profile smoke test (prints PASS/FAIL via exit status)
go run ./cmd/pinky phone profile list && go run ./cmd/pinky phone profile check -screen internal/guard/testdata/apple_2fa_modal.txt
```

---

## Rationale

### Interface depth & red-flag screening

- **Interface depth:** Callers gain autonomous handling of ~10 recurring edge cases behind **one** new tool (`phone_enter_code`) and **zero** extra calls for interrupts, retries, or link recovery. All complexity (WDA keyboard geometry, X-sorted split-digit extraction, `N-1` lockout barrier, scroll-until-enabled loops, cycle detection, disk redaction, USB/Wi-Fi fallback) is concentrated inside `internal/guard`, `internal/codeentry`, and `internal/gateway`.
- **Red-flag screen (`design-red-flags.md`):**
  - *Shallow module:* Avoided—no `check_interrupts`, `get_keypad`, or `read_code` micro-tools exposed to callers.
  - *Information leakage:* Avoided—`driver.Keypad` hides WDA's `XCUIElementTypeKeyboard`; vendor strings live only in YAML data; wire types stay inside `internal/mcp`.
  - *Temporal decomposition:* Avoided—`internal/guard` owns match + resolve + loop state; `internal/codeentry` owns extract + buffer + `N-1` entry.
  - *Pass-through method:* Avoided—`cmd/pinky/phone.go` already discovers MCP tools dynamically, so `phone_enter_code` requires zero CLI pass-through.
  - *Split ownership:* Avoided—`policy.Engine` remains sole policy decider; `CodeBuffer` and `LeaseState` are per-lease (no cross-task shared mutable state); `redactForDisk` is the single disk-persistence redaction path.
  - *Two ways to do one task:* Avoided—`PresetAutonomous` extends the existing `Preset` enum rather than adding a parallel `no_loop` flag; `autoUnlock` remains strictly lock-screen while `ScreenGuard` handles unlocked screens.
  - *Importable internals:* Avoided—helper matchers, box geometry, and loop detectors are unexported; `seams.yaml` blocks unauthorized imports/calls.
  - *Hand-synced list:* Avoided—CLI tool catalog is live from `tools/list`; `seams.yaml` is verified by `TestForkSeamsAllowedFilesExistAndUseTheSeam`; `policy.Op` coverage is unit-tested.

### Tradeoffs accepted

- We accept **adding one MCP tool (`phone_enter_code`)** in exchange for making split-box OTP entry atomic, keypad-capable, `N-1`-verified before auto-submit, and disk-redacted without exposing raw keyboard geometry or relaxing `RuleCredentials` on password fields.
- We accept **keeping `ScreenGuard` rule operations closed-set (6 typed `OpKind`s)** in exchange for deterministic, fast, policy-checked, unit-testable recovery that cannot go off-script.
- We accept **never auto-tapping `"Resend"` or auto-retrying a failed `N-1` submit** in exchange for a hard guarantee against Apple Account lockout.
- We accept **keeping `autoUnlock` separate from `ScreenGuard`** in exchange for preserving the existing 1-attempt-per-PIN hardware lock-screen safety boundary (`driver.Unlocker` runs before WDA has an app UI tree).

### Why not the alternatives

1. **Rejected Alternative A: Client-side / CLI script orchestrator (edge-case wrapper in `cmd/pinky` or `phone run` helper library).**
   - *Interface depth comparison:* Exposes far more complexity to callers and fails on non-CLI MCP agents (Claude Code, Codex, remote HTTP), creating two ways to control the phone. Worse, a client-side wrapper **cannot** see WDA's `XCUIElementTypeKeyboard` keys (stripped by `wdaScreenElements`), **cannot** prevent unredacted OTP screens from hitting the gateway's SQLite history and screenshots, and **cannot** trigger `discoverSoon()` or switch USB/Wi-Fi transports during a transient drop.
2. **Rejected Alternative B: In-daemon LLM / VLM supervisor ("model-in-the-loop" recovery on every unexpected screen).**
   - *Interface depth comparison:* Appears to hide everything behind a single "magic" layer, but violates tools-before-models, adds seconds of latency and token cost to deterministic system prompts, introduces non-deterministic OCR/ordering mistakes on 6-digit codes (risking account lockout without a deterministic `N-1` verifier), and cannot be tested offline without mocking a frontier model. Keeping deterministic rules in the daemon and letting the calling agent's model handle genuinely novel unmatched screens gives the right boundary.

### Open questions and risks

1. **Preset gate defaults:** In `PresetAutonomous`, should `Payments` default to fail-fast `Deny` for nonzero amounts (with `$0`/free `"Get"` allowed), or does the owner keep no payment method on the dedicated phone's Apple Account and prefer `Allow`? And should `Sign Out` default to `Allow` (reversible safeguard) or `Deny`?
2. **App Store / system password prompt scope:** Should `fill_account` in `default.yaml` map `"default"` to a single configured account name in `rules.yaml` (e.g. `default_account: apple-agent`), or require explicit per-app account mapping?
3. **Masked OTP boxes:** Some web forms immediately mask each OTP box to `•` on entry. At the `N-1` barrier, we can prove `count(filled) == N-1` and `box[N-1]` empty, but not individual digit values—is checking `count == N-1` + keypad tap confirmation sufficient for masked OTP grids?
4. **WDA `KeypadKeys` latency:** Reading `d.source` once to locate `'0'..'9'` adds one tree fetch on `enter_code` entry; caching the keypad rect map while `KeyboardFrame` and orientation stay unchanged avoids repeated fetches across digits, but needs a test confirming iOS never shifts the numeric keypad mid-entry.

### Next implementation step

Add `internal/policy/presets.go`'s `PresetAutonomous` + `RuleDeleteAccount`/`RuleSignOut` split and `internal/gateway/codes.go` + `record.go`'s unconditional disk redaction and `e.Masked` screenshot coverage first, so every subsequent feature (`codeentry`, `guard`, `autologins`) builds on a verified no-loop policy and zero-leak persistence seam.
