# iphone-control: interrupts the tool clears by itself, and one-time codes typed from the screen

Design sketch (Opus runner). Read-only grounding of `/Users/nqh/Desktop/iphone-control` at `ed5ab4d7`. No code was changed.

## 0. Three facts from the code that shape the design

1. **"Hide codes from agents" being off conflicts with secret typing today.** `internal/gateway/secrets.go:36` `secretReady` refuses every `{{secret:NAME}}` while `policy.HideCodes()` is false (`secretsNeedHide`). So `phone fill` cannot type today. With the setting off, history screenshots (`shots.go:329`) and recordings on disk also keep any code that was on screen, which breaks "codes must not be logged to disk". So `{{code}}` does **not** depend on the setting being off. The gateway reads a code from the **raw** read, before `policy.MaskScreen` runs in `observeOCR`, and types it itself, so the agent never needs the digits. Typing passwords (`{{secret:}}`, `{{account:}}`) keeps the existing rule, so **Hide codes on is a precondition of this design** (§2.1).
2. **Probable cause of the split-box failure (unproven).** On iOS, `Type` pastes through the pasteboard (`internal/driver/ios/type.go:13-35`). A paste into `<input maxlength=1>` boxes whose page script handles `keydown` or `input` per box is typically dropped or cleared. Tapping keypad keys worked, and those are key events. Typing by keys is only a whole-device toggle (`Device.TypeByKeys`, for iosctl). `driver.TypeOptions` has no per-call flag. The design adds `TypeOptions.ByKeys` and enters codes into boxes only by keys. Device probe P1 settles the cause.
3. **Nothing detects the boxes or the digit row today.** Literal digits went into the boxes without a `blocked_by_policy`, so `policy.IsCredentialField` (`detect.go:613`) does not recognise them. It judges by label and identifier. The masker has the same gap: `maskLoneCode` (`codes.go:109`) needs a 4–8 digit number inside one label, so a code shown as six one-digit elements is never masked, even with Hide codes on. One geometric detector shared by the masker and the reader closes both gaps.

## 1. Problem

The owner wants agents to finish tasks on the phone with nobody answering anything. Today every surprise on screen has to be handled by the agent's own script, one case at a time: system sheets, "Save password?", a passkey sheet, first-run prompts whose buttons stay disabled until you scroll, a sign-in request pushed to this same phone, a one-time code displayed on this phone and typed into split boxes, a dropped link, a slow tree. A wrong guess can be costly. Retyping a code or tapping Resend repeatedly can lock the Apple Account. Tapping "Allow" on a sign-in request the agent did not start would let anyone who knows the password in.

Constraints honoured:

- The gateway is the only place a value meets the driver (`secretText`, seam `secret hand-out`).
- Preset gates are judged in `act → prepare → evaluate`, and nothing may route around them.
- `autoUnlock` already handles the lock screen and must stay its only handler.
- Results are text with a status line. A new status would ripple into exit codes and every client.
- No comments in code. Neutral names in the mechanism; Apple strings live only in data.
- Each new reader of a store gets a `seams.yaml` row and a fixture.

## 2. Usage (caller's view)

### 2.1 The agent's script, with no special cases

Preconditions, set once by the owner:

- Hide codes from agents is **on** (`mask_codes: true`), because `{{account:}}` and `{{secret:}}` are refused while it is off (`secretReady`).
- `com.apple.mobilesafari` is on `credential_apps`.

```sh
iphone run -as account-check -expect sign-in -e '
phone call launch url=https://account.apple.com >/dev/null
phone call wait_for text="Email or Phone Number" timeout_ms=20000 >/dev/null
phone call type ref=7 text=agents@example.invalid submit=true >/dev/null
phone call type text="{{account:agents}}" submit=true >/dev/null
phone call wait_for '"'"'{"any":["Sign-In and Security","Personal Information"],"timeout_ms":45000}'"'"'
'
```

What comes back (stdout, one block):

```text
settled · iPhone SE · matched: true · handled: save-password, sign-in-request, code-shown, code-boxes
Waited 31.4 s for “Sign-In and Security” / “Personal Information”: “Sign-In and Security” showed.
[app: Safari (com.apple.mobilesafari)] ...
```

Between the password and the match, the gateway cleared four interrupts by itself, inside `wait_for`'s poll loop:

1. It dismissed "Save Password?".
2. It allowed the sign-in request. That entry fires only because this lease declared `-expect sign-in` **and** typed a credential in Safari within the last 5 minutes.
3. It kept the code the phone displayed in memory and tapped the display's dismiss button.
4. It typed the code into the six boxes by keys. It read the first five back before sending the sixth.

The agent's script names none of these.

### 2.2 When something is not in the book

```text
stale · iPhone SE · interrupt: "Turn On iCloud Keychain?" · buttons: "Not Now", "Turn On"
Nothing was done: a prompt no interrupt entry covers is in front. fix: add an entry to ~/.pinky/interrupts.d/<name>.yaml and check it with `pinkyctl phone interrupts match <file> <screen.txt>`, or tap a button yourself.
```

### 2.3 Typing a code directly (no book, no expect)

```sh
phone call type ref=21 text="{{code}}"
# settled · iPhone SE
# Typed the code the phone showed 40 s ago (6 digits, read by tree and OCR alike) into 6 boxes; 5 of 6 read back before the last.
```

`{{code}}` means the newest one-time code this phone displayed during this lease. It has not been typed yet, was not rejected, and is not older than its lifetime.

### 2.4 The book is data

Built-in entries are embedded from `internal/interrupt/books/*.yaml`. Agents add their own in `$PINKY_HOME/interrupts.d/*.yaml`, which is hot-reloaded. Apple strings appear only here:

```yaml
entries:
  - name: save-password
    kind: dismiss
    when: { any: ["Save Password?", "Save this password"], button: "Not Now" }
    do: [ { tap: "Not Now" } ]

  - name: first-run-ads
    kind: dismiss
    when: { texts: ["Personalised Ads"] }
    do:
      - { scroll_until_enabled: "Turn Off Personalised Ads" }
      - { tap: "Turn Off Personalised Ads" }

  - name: passkey-sheet
    kind: dismiss
    when: { texts: ["passkey"], button: "Cancel", shape: sheet }
    do: [ { tap: "Cancel" } ]

  - name: device-account-sheet
    kind: dismiss
    when: { texts: ["with Apple Account?"], button: "Not Now", shape: sheet }
    do: [ { tap: "Not Now" } ]

  - name: sign-in-request
    kind: consent
    expect: sign-in
    after: credential_typed
    max: 1
    when: { texts: ["Sign In Requested"], button: "Allow" }
    do: [ { tap: "Allow" } ]

  - name: code-shown
    kind: dismiss
    when: { shape: code_shown, button: "Done" }
    do: [ { tap: "Done" } ]

  - name: code-boxes
    kind: credential
    expect: sign-in
    when: { shape: code_field }
    do: [ { type_code: true } ]
    rejected: ["Incorrect verification code"]
    resend: ["Resend Code", "Didn't get a verification code?"]

  - name: store-password
    kind: credential
    expect: store
    when: { texts: ["Apple Account"], shape: password_prompt }
    do: [ { type_account: agents, submit: true } ]

  - name: no-internet
    kind: report
    when: { app: [com.apple.mobilesafari], texts: ["not connected to the internet"] }
    report: "The phone's Wi-Fi has no internet. fix: phone run tools/iphone-control/flows/wifi-rejoin.sh"
```

### 2.5 Agent tools that ship with it

```sh
pinkyctl phone interrupts list                      # entries in force: name, kind, expect, source file
pinkyctl phone interrupts check <file.yaml>         # loads and validates one book; exit 0 or 2 with file:line: rule, fix:
pinkyctl phone interrupts match <file.yaml> <screen.txt> [-expect NAME] [-after credential_typed]
                                                    # offline: which entry fires on a saved screen read, and the steps it would run
```

`screen.txt` is the text of a `phone call observe`, which an agent saves from any run. `match` is the headless runner: it calls the same pure `interrupt.Book.Match` that the gateway calls.

## 3. The single concept

**An interrupt** is a screen in front that is not what the agent's step was about. **The book** says, as data, which interrupts the tool clears by itself and how. The gateway consults it at the one place every read already passes: after `autoUnlock` and `readyScreen`, before an action is judged, after an action settles, and inside `wait_for`'s poll loop. A cleared interrupt runs its steps **as ordinary actions through `act`**, so presets, blocked apps, credential rules and approvals judge them like any other action.

Four kinds, fixed by the loader. A file cannot widen a kind:

| Kind | May do | Needs |
|---|---|---|
| `dismiss` | `tap`, `scroll_until_enabled`, `wait` | nothing: fires for every lease |
| `consent` | `tap` | `expect` (the lease declared the context) **and** `after` (a trigger event of this lease inside a window) |
| `credential` | `type_code`, `type_account: NAME`, `tap` on a `resend` label (once, counted) | `expect` |
| `report` | nothing on the phone; the result gets the entry's message and fix | nothing |

A `dismiss` entry whose `when` uses the shape `code_shown` matches only while the code slot holds the code read from that same screen (§5.3). The display of a code is therefore never dismissed before its code is kept. That covers an ambiguous read, a code that was not read twice, and an alert outside the tree (P7). Dismissing it early would force a resend, which is the lockout path.

The App Store "Require password" prompt is the `store-password` entry. A run that downloads or buys declares `-expect store`, and the agents' own password is typed through `{{account:agents}}`. The payment gate still judges any buy that follows. The prompt can also be made rarer once, in Settings (Media & Purchases › Password Settings), as a plain settings change.

Value sources unify the same way. `phone_type` resolves one reference grammar, owned by `vault/ref.go`:

| Reference | Source | Owner of the store |
|---|---|---|
| `{{secret:NAME}}` | the vault (staged by the broker) | `internal/vault` (exists) |
| `{{account:NAME}}` | the agents' own logins in the Mac keychain, given to the gateway as `Options.Logins` | `internal/secret` (exists), wired by the daemon |
| `{{code}}` | the lease's code slot, filled from raw reads | `internal/gateway/codeslot.go` (new) |

`phone fill -account` becomes a thin alias for `phone_type text={{account:NAME}}`. Its CLI-side keychain read and its `StageSecret` allowance are deleted. That leaves one way to type the agents' own password.

## 4. Module map

| Path | New or changed | Owns |
|---|---|---|
| `internal/interrupt/book.go` | new | `Book`, `Entry`, `Kind`, `When`, `Step`; `Load` (validates kinds) and `Match` (pure) |
| `internal/interrupt/shape.go` | new | the built-in shapes (`dialog`, `sheet`, `code_shown`, `code_field`) as pure functions of a screen |
| `internal/interrupt/books/*.yaml` | new data | built-in entries (`ios-system.yaml`, `apple-account.yaml`) |
| `internal/gateway/interrupts.go` | new | `clearInterrupts`: runs a matched entry's steps through `act`, caps, stale check, `handled:` key |
| `internal/gateway/codeslot.go` | new | per-lease newest code; `keepShownCode` (writer), `takeCode` (taker), resend/reject invalidation |
| `internal/gateway/codeledger.go` | new | rejected-code counts per key, persisted as counts and times only (`$PINKY_HOME/code-attempts.json`) |
| `internal/gateway/fieldgroup.go` | new | split-field detection and group entry by keys with read-back before the last character |
| `internal/policy/codes.go` | changed | `FindCodes`: one detector (number-in-label **and** digit row) used by `MaskElements` and `keepShownCode` |
| `internal/policy/detect.go` | changed | `IsCredentialField` also true for a member of a code-field group (geometry) |
| `internal/vault/ref.go` | changed | `ParseRef` returns `Ref{Kind, Name}` for secret, account, code |
| `internal/gateway/secrets.go` | changed | `secretText` becomes `typedValue`: the one resolver of all three sources |
| `internal/gateway/pipeline.go` | changed | three call sites: `keepShownCode` in `observeOCR` before masking; `clearInterrupts` in `act`; group entry in `perform` |
| `internal/gateway/agent.go` | changed | `wait_for` loop calls `clearInterrupts`; unmatched result sends the whole screen |
| `internal/driver/driver.go`, `ios/type.go` | changed | `TypeOptions.ByKeys`; iOS honours it per call |
| `cmd/pinky/logins.go` | new | daemon wiring: `secret.NewAgentLogins` → `gateway.Options.Logins` |
| `cmd/pinky/phone_interrupts.go` | new | `phone interrupts list/check/match` |
| `cmd/pinky/phone.go` (run) | changed | `-expect NAME` flag → `phone_connect {expect:[...]}` |
| `cmd/pinky/phone_fill.go` | shrunk | `-account` maps to `{{account:NAME}}`; `phoneStageOwn` deleted |

Call chain for one cleared interrupt: `act` → `clearInterrupts` (gateway) → `Book.Match` (interrupt, pure) → `act` per step. That is three files.

## 5. Types and signatures

### 5.1 `internal/interrupt`

```go
package interrupt

type Kind int

const (
	Dismiss Kind = iota + 1
	Consent
	Credential
	Report
)

type Shape string

const (
	ShapeDialog         Shape = "dialog"
	ShapeSheet          Shape = "sheet"
	ShapeCodeShown      Shape = "code_shown"
	ShapeCodeField      Shape = "code_field"
	ShapePasswordPrompt Shape = "password_prompt"
)

type Trigger string

const TriggerCredentialTyped Trigger = "credential_typed"

type When struct {
	Apps   []string
	Texts  []string
	Any    []string
	Button string
	Shape  Shape
}

type StepOp int

const (
	OpTap StepOp = iota + 1
	OpScrollUntilEnabled
	OpWait
	OpTypeCode
	OpTypeAccount
	OpResend
)

type Step struct {
	Op      StepOp
	Label   string
	Account string
	Submit  bool
	Wait    time.Duration
}

type Entry struct {
	Name     string
	Kind     Kind
	When     When
	Steps    []Step
	Expect   string
	After    Trigger
	Window   time.Duration
	Max      int
	Rejected []string
	Resend   []string
	Report   string
	Source   string
}

type Lease struct {
	Expects  []string
	Triggers map[Trigger]time.Time
	Fired    map[string]int
	CodeKept bool
	Now      time.Time
}

type Book struct{ entries []Entry }

type LoadError struct{ File, Rule, Fix string; Line int }

func Load(files map[string][]byte) (*Book, []LoadError) { panic("not implemented") }

func (b *Book) Match(s *screen.Screen, l Lease) (*Entry, bool) { panic("not implemented") }

func (b *Book) Entries() []Entry { panic("not implemented") }

func HasShape(s *screen.Screen, sh Shape) bool { panic("not implemented") }
```

Invariants, enforced in `Load` (rejected with `file:line: rule` and `fix:`):

- A `consent` entry has a non-empty `Expect`, a known `After`, and `Window` ≤ 10 min (default 5 min). Its steps are only `OpTap`.
- A `credential` entry has `Expect`. Its steps are `OpTypeCode` or `OpTypeAccount` (one of them), plus at most one `OpResend`.
- A `dismiss` entry has no `Expect` or `After`. Its steps are only `OpTap`, `OpScrollUntilEnabled` and `OpWait`.
- A `report` entry has no steps and has a non-empty `Report` with a `fix:`.
- `Max` defaults to 2 and is capped at 3. Names are unique across all loaded files, and a user file may shadow a built-in only by the same name.
- `When` needs at least one of `Texts`, `Any`, `Shape`. An entry that matches on `Button` alone is refused, because it would fire on any screen.

`Match` is pure. It returns the first entry, in file order with user files first, such that:

- the screen satisfies `When`;
- `Fired[name] < Max`;
- for a `consent` entry, `Expects` contains `Expect` and `Now − Triggers[After] ≤ Window`;
- for a `credential` entry, `Expects` contains `Expect`;
- for any entry whose `When.Shape` is `code_shown`, `CodeKept` is true. `leaseView` sets it when the slot's mark equals the mark of the code `FindCodes` reads on this screen.

Text matching is `wait_for`'s matcher (`screenShows`, moved into `internal/screen` as `screen.Shows`). One matcher serves both, so an entry and a `wait_for` agree on what "shows" means.

### 5.2 Gateway: clearing interrupts

```go
package gateway

type cleared struct {
	handled []string
	stuck   string
	unknown *screen.Element
}

const (
	maxInterruptsPerCall = 3
)

func (g *Gateway) clearInterrupts(ctx context.Context, l *lease, cur *screen.Screen) (cleared, *screen.Screen) { panic("not implemented") }

func (g *Gateway) runEntry(ctx context.Context, l *lease, e *interrupt.Entry, cur *screen.Screen) (*screen.Screen, *Result) { panic("not implemented") }

func (g *Gateway) leaseView(l *lease) interrupt.Lease { panic("not implemented") }

func (g *Gateway) markTrigger(l *lease, t interrupt.Trigger) { panic("not implemented") }
```

Behaviour:

- **Where it runs, and the lock.** `clearInterrupts` never runs while the device's action lock `d.act` is held. Each step goes back through `act`, which takes `lockAct` itself, so calling it under the lock would deadlock. This is the trap `autoUnlock` already avoids (EXTRAS.md: `Unlock` takes the lock). There are three call sites:
  - in `act`, right after `g.autoUnlock` / `gate` / `readyScreen` and **before** `lockAct`;
  - in the action tool handlers, after `act` returned and released the lock (the after-settle check);
  - on each poll of `wait_for`, which holds no action lock (it reads).
- **Reads.** The before-action check matches against the latest cached read (the settled read the previous result came from) and reads the phone again only when an entry fires. That keeps the iPhone SE's 0.5–7 s read cost off actions where nothing interrupts.
- **Scope.** It never runs on a locked screen, because `autoUnlock` owns that one. It never runs inside an interrupt's own step: steps carry a flag that skips `clearInterrupts`, so there is no recursion.
- **Steps are actions.** Each step becomes an `Action{Tool:"tap"|"swipe"|"type", Intent:"interrupt <name>"}` and goes through `act` with `c.noAsk = true`. If any step meets `Ask` or `Deny`, the entry stops and the result says so (`blocked_by_policy`, `rule:`). A book entry therefore cannot pay, erase or sign out, because the preset gates still judge it. Labels resolve against elements first and OCR lines second, which is how a tap by name already works when the tree is slow.
- **Caps.** At most `maxInterruptsPerCall` entries per call. Each entry fires at most `Max` times per lease. An entry whose `When` still matches after its steps ran is `stuck` and is not run again in this call.
- **The agent's target after an interrupt.** If something was cleared before an action that is aimed at a ref, the gateway compares the target, as it was on the agent's last screen, against the screen `startScreen` reads under the lock with the approval's stale comparison (`resume.go`: role, label, states, position within tolerance). If the target is unchanged, the action proceeds and the result carries `handled:`. If it changed, the action does not run and the result is `stale · handled: …`. No tap ever lands on a target that moved.
- **Unknown interrupts.** A `dialog` or `sheet` shape that no entry matches, and that covers the target or appeared since the agent's last read, is not touched. The result is `stale · interrupt: "<title>" · buttons: …`, with the fix line from §2.2.
- **Result keys.** There is no new status. The new keys are `handled: a, b`, `interrupt: "…"`, `stuck: name`. CLI exit codes are unchanged (`stale` is exit 0, like today).
- **Triggers.** `markTrigger(l, TriggerCredentialTyped)` is called by `typedValue` after a `{{secret:}}` or `{{account:}}` reference was typed into a field of app A. The trigger records the app, and a `consent` entry for a system prompt is valid only within its window after a credential was typed in any app during this lease. That binds "this lease started the sign-in" to an event only the lease itself can produce.

### 5.3 The code slot

```go
package gateway

type shownCode struct {
	digits []byte
	app    screen.App
	seenAt time.Time
	twice  bool
	mark   [32]byte
}

type codeSlot struct {
	mu       sync.Mutex
	cur      *shownCode
	spent    map[[32]byte]bool
	clearAt  time.Time
	key      [32]byte
}

const codeLifetime = 5 * time.Minute

func (g *Gateway) keepShownCode(l *lease, raw *screen.Screen) { panic("not implemented") }

func (g *Gateway) takeCode(l *lease, into screen.App) ([]byte, error) { panic("not implemented") }

func (g *Gateway) codeSpent(l *lease, outcome codeOutcome) { panic("not implemented") }

func (g *Gateway) codeResent(l *lease) { panic("not implemented") }

type codeOutcome int

const (
	codeAccepted codeOutcome = iota + 1
	codeRejected
	codeUnknown
)
```

Behaviour:

- **Capture.** `keepShownCode` runs in `observeOCR` on the raw screen, **before** `MaskScreen` and `maskSecrets`, while a lease exists. It calls `policy.FindCodes(raw.Elements)`.
  - Two or more distinct codes on one screen → ambiguous, nothing is kept.
  - Exactly one → it replaces `cur` if its `mark` differs and the code is not `spent`.
  - `mark` is an HMAC-SHA256 of the digits with `key`, a per-daemon random key held in memory, like the PIN refusal latch.
  - `twice` is set when the tree and an OCR pass of the same rectangle give the same digit sequence. This proves the source before anything is typed.
- **Take.** `takeCode` returns the digits once and moves `cur` into `spent`. It fails with a fix line when:
  - there is no code;
  - the code is older than `codeLifetime`, a labelled calibration (Apple's lifetime is unproven; probe P4);
  - the code was seen before `clearAt` (a resend since);
  - `twice` is false while OCR was available.
- **Invalidation.** `codeResent` sets `clearAt = now` and drops `cur`. `runEntry` calls it when it taps a `resend` label. `act` also calls it when the agent taps a label in any loaded entry's `Resend` list. Either way, a code seen before the resend can never be typed.
- **Outcome.** `codeSpent(codeRejected)` is called when, after the last character settles, either:
  - a `Rejected` text of the matching entry shows, or
  - the group is still on screen and all its boxes read empty.

  It increments the ledger. The mark stays in `spent`, so the same code is never retyped.
- **No logs.** Digits never reach a log, history step, result or export. The step text is "Typed the code the phone showed (6 digits)". After typing, the digits are added to the per-lease mask list that `maskSecrets` uses (10 min), so later reads show `••••••`. That matches how a typed secret is treated.

### 5.4 `policy.FindCodes`: one detector

```go
package policy

type CodeSpan struct {
	Elements []int
	Digits   []byte
	Bounds   screen.Rect
	Row      bool
}

func FindCodes(els []screen.Element) []CodeSpan { panic("not implemented") }

func codeRow(els []screen.Element) []CodeSpan { panic("not implemented") }
```

`FindCodes` returns the code numbers that `maskNumbers` and `maskLoneCode` find today, plus digit rows:

- 4 to 8 elements, each label one digit;
- centres within half the median height of one another on y;
- heights within ±25 %;
- gaps between neighbours within ±50 % of the median gap;
- sorted by x;
- an element right next to the row talks about a code (`hasCodeWords` or `namesCode`) **or** the row is inside a `dialog`/`sheet`.

`MaskElements` is rewritten on top of `FindCodes`, so "what is a code" has one definition for masking and for capture.

### 5.5 Field groups and entry by keys

```go
package gateway

type fieldGroup struct {
	boxes  []screen.Element
	secure bool
}

type groupResult struct {
	entered   bool
	verified  int
	of        int
	submitted bool
	why       string
}

func findGroup(s *screen.Screen, ref int) (fieldGroup, bool) { panic("not implemented") }

func (g *Gateway) typeGroup(ctx context.Context, l *lease, dev driver.Device, grp fieldGroup, value []byte) groupResult { panic("not implemented") }

func readBoxes(ctx context.Context, g *Gateway, l *lease, grp fieldGroup) ([]rune, bool) { panic("not implemented") }
```

```go
package driver

type TypeOptions struct {
	Append bool
	Submit bool
	ByKeys bool
}
```

`findGroup` finds 4 to 8 input elements, including the ref's own, on one row (the same geometry rules as `codeRow`) and each narrower than `screen.Width / (n+1)`. `policy.IsCredentialField` is true for each member. Literal text into a group is therefore refused like any code field, and only `{{code}}`, `{{secret:}}` and `{{account:}}` reach it.

`typeGroup` is the code-entry primitive:

1. Tap box 1. Type character 1 with `ByKeys: true`. Read the boxes.
   - Box 1 holds it and focus moved → mode `advance`.
   - Box 1 holds it and focus stayed → mode `tap-each`: tap box i before each character.
   - Box 1 is empty → clear and try `tap-each` once. Still empty → stop with `why: "the boxes take no keys"`. Nothing is submitted.
2. Type characters 2 … n−1 the same way.
3. **Proof before the last character.** Read boxes 1 … n−1 and compare them, in x order, with `value[:n-1]`. Boxes n … are empty.
   - The read uses the tree's values first.
   - If those are empty or hidden, it OCRs the union of the box rectangles. This read stays in the gateway and is never masked away from it.
   - Mismatch → `backspace` n times, retry once from step 1 in `tap-each` mode. A second mismatch → stop, `entered: false`, `why: "k of n read back wrong"`. Nothing is submitted.
4. Type character n. Most such pages submit on the last box. `submit` presses Return only when the boxes are still on screen afterwards and the caller asked for it.
5. **Neither read works** (secure boxes with no OCR text): send the last character **once per code**, with `verified: 0`, and leave the ledger as the guard. The tradeoff is in §9.

Every outcome returns `settled` or `uncertain` with counts and never digits ("5 of 6 read back before the last").

### 5.6 Ref grammar, value resolver, ledger

```go
package vault

type RefKind int

const (
	RefSecret RefKind = iota + 1
	RefAccount
	RefCode
)

type Ref struct {
	Kind RefKind
	Name string
}

func ParseRef(text string) (Ref, bool, error) { panic("not implemented") }
```

```go
package gateway

func (g *Gateway) typedValue(l *lease, a Action, p *prep) ([]byte, error) { panic("not implemented") }

type codeLedger struct {
	path string
	mu   sync.Mutex
	hits map[string][]time.Time
}

const (
	maxRejected    = 2
	rejectedWindow = 24 * time.Hour
)

func (c *codeLedger) allow(key string, now time.Time) (bool, time.Duration) { panic("not implemented") }

func (c *codeLedger) reject(key string, now time.Time) error { panic("not implemented") }
```

- `typedValue` is the one resolver, and the only caller of `TakeSecret`, `Options.AgentLogins.Read` and `takeCode`.
- `secretReady` checks are per kind:
  - `{{secret:}}` and `{{account:}}` keep every existing check: Hide codes on, the app on `credential_apps`, a password or code field.
  - `{{code}}` skips the Hide codes check and the `credential_apps` check. With Hide codes off, the code was already on the screen unmasked, so typing it from the gateway adds no exposure. The value comes from this phone's own screen, not from a store the owner curates. It keeps the field check: the target must be a code field or a code-field group in the app in front.
- It zeroes the returned bytes after `perform`.
- For a `RefCode` it first asks `codeLedger.allow(key)`. `key` is the lease's `expect` name when one matches, else the app's bundle id.
- `maxRejected` and `rejectedWindow` are a **labelled calibration**, not Apple's threshold, which is unproven. At the cap, `{{code}}` is refused with "wait <d> or ask the owner" and nothing is typed.
- The ledger file holds key, count and times only: no digits, no marks.

## 6. The four preset gates under "no loop"

An `Ask` that nobody answers waits 60 s (`approval timeout`) and then denies. In no-loop mode an Ask gate is a slow refusal. The honest choice per gate is a **standing decision** the owner makes once in `rules.yaml`: `allow`, `refuse` (instant, with a fix line) or `ask`. The owner decides this; below is the default with the tradeoff.

| Gate | Can the agents undo it alone? | Default | Tradeoff |
|---|---|---|---|
| Payments | No. Money leaves. | **ask** (unchanged) | The only gate normal work can hit, such as buying an app. A standing `allow` with a monthly cap would remove the loop for small amounts; the cap needs a spend ledger that does not exist yet. |
| Erase All Content and Settings | No. It removes the helper, the Mac's pairing trust and Developer Mode, which need a person at the phone to restore. It ends the agents' own access. | **refuse** (instant) | No agent task needs it, and asking just wastes 60 s. Unproven: the exact list of what erase removes that needs a person (probe P6). |
| Sign Out / Delete Account | Sign Out: maybe. The agents hold the password, but this phone may stop being a trusted device for 2FA codes after sign-out, and the codes would then go elsewhere (unproven, probe P5). Delete Account: no. | Delete: **refuse**. Sign Out: **refuse** until P5 proves sign-in back with no person, then **allow** | Allowing Sign Out before P5 risks locking the agents out of their own account. |
| Turn Passcode Off | Partly. Turning it back on needs taps on the passcode keypad, which `phone_tap` refuses today (`not_in_screenshot` keypad rule). It also drops Apple Pay cards and some keychain items on the phone. | **refuse** | Allowing it would also need a new reversible path, a `phone pin restore` that types the saved passcode into Settings, and that is a separate design. |

Implementation: `guardRules` keep their rules. A new `guards:` map in `rules.yaml` (`payments: ask`, `erase: refuse`, …) sets each rule's `Decision`. `presetRules` reads it. Defaults live in `defaults.yaml`.

## 7. Failure and recovery: what the tool does vs what it reports

| Situation | The tool does | It reports |
|---|---|---|
| Locked | `autoUnlock` (exists) | its fixed reasons (exists) |
| Known interrupt | runs the entry through `act`, capped | `handled: names` |
| Interrupt still there after its entry | stops, does not loop | `stuck: name` + screen |
| Unknown dialog or sheet over the target | nothing | `stale · interrupt: "title" · buttons:` + fix to add an entry |
| Agent's target moved after an interrupt | does not tap | `stale · handled:` |
| `wait_for` with a prompt on top | clears it inside the poll | `handled:`; an unmatched result sends the whole screen, not a diff |
| Slow tree, OCR fallback with no refs | entries tap labels via OCR lines; one extra read with the slow-tree timeout before the agent gets a ref-less screen | `read: ocr` key when refs are missing |
| Link drop (USB ↔ Wi-Fi) | the lease survives the 60 s reconnect grace (exists). An action call waits for the link within its budget before answering `device_offline`. The owner sets the phone's connection to `auto` once, and the driver's own rule picks USB when cabled, Wi-Fi otherwise. The tool never rewrites the owner's saved choice. | `link:` in `phone status`; `uncertain` if the drop came mid-action |
| `device_service` lost on Wi-Fi | nothing: it needs an admin password on the Mac | `device_needs_setup · setup: device_service`. An orchestrator can supply sudo through its secret prompt. |
| Wi-Fi with no internet | `report` entry | message + fix pointing to a rejoin flow script |
| Code boxes take no keys or read back wrong | one retry in the other mode, then stops before the last character | `uncertain · entered: false`, counts only |
| Code rejected | never retypes it; ledger +1. A `credential` entry may tap Resend once per lease; the next code shown is used. | `rejected`; at the cap `{{code}}` is refused with the wait time |
| No code or expired code | waits for a code inside `wait_for` (the slot fills on any read) | `not_found` with fix "wait for the phone to show a new code" |

## 8. Seams and guards

New `seams.yaml` rows (each with a `testdata/` fixture of a forbidden use, as the existing ones have):

```yaml
  - name: code slot writer
    package: github.com/DIYgod/pinky/internal/gateway
    methods: [keepShownCode]
    allowed: [internal/gateway/pipeline.go]
    fix: "a code enters the slot only from the raw read in observeOCR, before masking. If another read path needs it, add it here and say why."
  - name: code slot taker
    package: github.com/DIYgod/pinky/internal/gateway
    methods: [takeCode]
    allowed: [internal/gateway/secrets.go]
    fix: "a code leaves the slot only in typedValue, just before the driver types it."
  - name: agents' login store in the daemon
    package: github.com/DIYgod/pinky/internal/secret
    symbols: [NewAgentLogins]
    allowed: [cmd/pinky/phone_login.go, cmd/pinky/logins.go]
    fix: "the daemon wiring (cmd/pinky/logins.go) gives the gateway Options.Logins; phone_login.go saves. Nothing else builds the store."
  - name: login read in the gateway
    package: github.com/DIYgod/pinky/internal/gateway
    idents: [AgentLogins]
    allowed: [internal/gateway/secrets.go, internal/gateway/gateway.go]
    fix: "Options.AgentLogins is declared in gateway.go and read only by typedValue in secrets.go."
```

`idents` rows check only files in the package's own directory (`forkseams_test.go:206`), so the last row guards the gateway side. `cmd/pinky/logins.go`, which sets the field, lies outside that directory and is guarded by the `NewAgentLogins` symbols row above. The field is named `AgentLogins`, not `Logins`, so the ident match does not hit unrelated names.

Changed rows:

- `secret staging` drops `cmd/pinky/phone_fill.go`, because `fill -account` no longer stages.
- `no-prompt login store variable` drops `phone_fill.go`.
- `secret store input` (`Deposit`, `allowed: []`) is unchanged. The code slot is not the vault.

Other guards:

- **Book invariants** are checked in `interrupt.Load`. `TestBuiltinBooksLoad` loads every embedded file. `TestBookRules` has one fixture per refused shape under `internal/interrupt/testdata/bad/`.
- **Code canary.** `TestCodeCanary`, in the style of `TestSecretCanary`, shows a sentinel code on a fake screen as a digit row and as a label, with Hide codes **on and off**. It types `{{code}}` and fails if the digits appear in a result, history, export, log, approval, or an uncovered screenshot. It must also fail with `keepShownCode`'s pre-mask ordering swapped.
- **Interrupt pairing lint.** `TestEveryShapeHasFixture`: each `Shape` constant has a matching fixture screen under `internal/interrupt/testdata/shapes/`, so a new shape cannot ship without proof.
- **AGENTS.md** of the fork gains one bullet: "Interrupts: data in `internal/interrupt/books` and `$PINKY_HOME/interrupts.d`; a consent entry needs `expect` and `after`; check with `phone interrupts check|match`."

## 9. Tradeoffs accepted

- We accept that `{{code}}` makes the gateway read codes the agent could also read, in exchange for working with Hide codes **on** and keeping digits out of every agent output and log.
- We accept a submit with no read-back proof on pages whose boxes neither report values nor show OCR-able digits. That costs at most one attempt per code, capped at 2 rejected codes per key per 24 h. The alternative, never submitting there, makes those pages impossible without a person, which is the loop the owner removed.
- We accept that a `dismiss` entry fires for every lease without being asked, in exchange for scripts that never mention "Save password?". The loader confines dismiss entries to taps, scrolls and waits, and policy judges each tap.
- We accept stale results after an interrupt moved the agent's target, which costs the agent one more call, in exchange for never tapping a moved target.
- We accept agent-written book files in `$PINKY_HOME`, in exchange for the book learning without the owner. The kind rules and policy bound what an entry can do, and consent still needs the lease's own expect and trigger.
- We accept per-key timing for a code (about 6 keys × the key pacing), in exchange for not pasting into boxes.

## 10. Alternatives considered

1. **A flow runner per task** (a declarative state machine such as "apple-account-sign-in": states recognised by screen signatures, transitions as actions; the agent runs `phone flow run NAME`).
   - What it hides: a whole task.
   - What it exposes: one flow per task, so the next task (App Store sign-in, iCloud settings) needs a new flow that repeats the same "Save password?" and passkey handling.
   - Interrupts arrive at any point in any flow, so each flow would list all of them, which is the hand-synced list red flag.
   - It is shallow per flow and wide in total. The interrupt book is orthogonal to tasks: one entry serves every task.
2. **A model picks the button** (on an unknown dialog, ask `decide choice` with the run's intent and the dialog's buttons).
   - It hides more, since no book is needed, but it puts a non-deterministic judgment on consent and trust prompts: exactly the prompts where a wrong answer lets someone else into the account.
   - It breaks "same input, same path".
   - Kept only as an agent-side helper that *proposes* a book entry for `phone interrupts match` to check. It is never a gateway step.
3. **A watcher in the CLI (`phone run`)** that polls and taps while the script runs.
   - MCP agents would not get it.
   - It races the agent's own calls on the same lease.
   - It cannot read the raw screen before masking, so it would need the digits in agent-visible text.
   - In the gateway, it rides the one read path that already exists.

## 11. Red-flag screen

| Flag | Verdict |
|---|---|
| Shallow module | `interrupt` exposes `Load`, `Match`, `HasShape`; the gateway side is one method. Deep. |
| Information leakage | Text matching is one function (`screen.Shows`) for `wait_for` and entries. Code shape is one function (`FindCodes`) for masking and capture. |
| Temporal decomposition | No load, validate, run stages across packages. `Load` validates in one place; the gateway only runs. |
| Pass-through | `phone fill -account` becomes a flag alias with no logic of its own; its staging path is deleted. |
| Split ownership | Code slot: one writer, one taker (seams). Ledger: one owner. Triggers: written only by `typedValue`. |
| Two ways to do one task | Lock screen stays `autoUnlock` only (the book never runs on a locked screen). The agents' password has one path (`{{account:}}`). |
| Importable internals | `codeSlot`, `fieldGroup` and `codeLedger` are unexported in `gateway`; `interrupt` exports types and three functions. |
| Hand-synced list | Resend labels are taken from loaded entries, not a second list. Shapes are tied to fixtures by a test. |

## 12. Test plan (commands print `ok` or `FAIL`)

Unit and fixture tests use no phone and no daemon, as the fork's AGENTS.md requires:

```sh
go test ./internal/interrupt -run 'TestBuiltinBooksLoad|TestBookRules|TestMatchConsentNeedsExpectAndTrigger|TestMatchMaxPerLease|TestEveryShapeHasFixture'
go test ./internal/policy -run 'TestFindCodesDigitRow|TestFindCodesOrderByX|TestMaskDigitRow|TestCodeGroupIsCredentialField'
go test ./internal/vault -run 'TestParseRefKinds'
go test ./internal/gateway -run 'TestCodeCanary|TestCodeSlotNewestWins|TestCodeSlotResendClears|TestCodeSlotNeverRetypesRejected|TestCodeLedgerCap'
go test ./internal/gateway -run 'TestTypeGroupAdvance|TestTypeGroupTapEach|TestTypeGroupStopsBeforeLastOnMismatch|TestTypeGroupNoReadSubmitsOnce'
go test ./internal/gateway -run 'TestInterruptBeforeActionStaleWhenTargetMoved|TestInterruptStepsGoThroughPolicy|TestInterruptStuckStops|TestUnknownDialogStale|TestWaitForClearsInterrupt|TestAutoUnlockOwnsLockScreen'
go test ./internal/forkseams
go test ./cmd/pinky -run 'TestPhoneInterruptsMatch|TestPhoneFillAccountAlias|TestRunExpectFlag'
make check
```

Scenarios behind the key ones:

- `TestTypeGroupStopsBeforeLastOnMismatch`: a fake driver whose boxes drop key 3. Two attempts. The last key is never sent, and the result is `entered: false · 2 of 6`.
- `TestMatchConsentNeedsExpectAndTrigger`: the "Sign In Requested" fixture does **not** fire without `expect`, does not fire with `expect` but no credential typed, does not fire 6 minutes after the trigger, and fires once inside the window.
- `TestInterruptStepsGoThroughPolicy`: an entry whose tap label is "Erase iPhone" in Settings returns `blocked_by_policy · rule: Erase this iPhone`, and the fake driver sees no tap.
- `TestCodeCanary`: §8.

Agent tool check (headless runner):

```sh
pinkyctl phone interrupts check internal/interrupt/books/apple-account.yaml && echo ok
pinkyctl phone interrupts match internal/interrupt/books/apple-account.yaml internal/interrupt/testdata/shapes/sign-in-request.txt -expect sign-in -after credential_typed | grep -q '^fires: sign-in-request' && echo ok || echo FAIL
```

## 13. Unproven: device probes before relying on it

These belong in `verify-iphone-control` (ipad-local) as scripted drives on the real phone, each with an expected result that can fail:

- **P1 Keys vs paste in Safari split boxes.** Type six digits into a test page of six `maxlength=1` inputs (a local page, not Apple's), once by paste and once with `ByKeys`. Expected: paste leaves boxes empty, keys fill them. This settles fact 2.
- **P2 Read-back of web input values through WDA.** Same page. Do the boxes' values appear in the tree? If not, OCR of the box rectangles is the read-back.
- **P3 Auto-submit on the last box** on the test page and, once, on the real sign-in page.
- **P4 Code lifetime.** How long a displayed code is accepted. Until measured, `codeLifetime = 5 min` is a labelled calibration.
- **P5 Sign Out and trusted device.** After a sign-out and sign-in back, does this phone still receive codes? This decides the Sign Out default.
- **P6 What erase removes** that needs a person: confirm from Apple documentation, without erasing.
- **P7 The code display's tree.** Are the six digits separate elements with x positions (as observed), inside an alert the WDA tree of the front app includes? If the alert is outside the tree, the capture reads OCR only, and `twice` cannot be set (the rule then relaxes to "OCR read twice, same digits").

## 14. Open questions

- Should a `dismiss` entry that an **agent** wrote, rather than a built-in, fire for every lease, or only for the lease whose holder name matches the file's owner? The default here is every lease.
- Is `rejectedWindow`/`maxRejected = 2/24 h` acceptable, given that the true Apple threshold is unknown?
- **Requirement, not a question: Hide codes goes back on.** `{{code}}` works either way, but `{{secret:}}` and `{{account:}}` are refused while it is off, and on-disk history and screenshots keep codes while it is off.
- **Needs the owner's yes:** moving the read of the agents' own login from the short-lived CLI process (`phoneStageOwn`) into the long-running daemon (`Options.AgentLogins`). It changes the secrets design the owner approved (EXTRAS.md §2). If the owner says no, `credential` entries keep only `type_code`, and the `store-password` entry becomes a `report` entry whose fix is `phone fill -account agents -submit`.

## 15. Next implementation step

`policy.FindCodes` with the digit-row detector and its fixtures. It fixes masking today and is the base for capture, field groups and the `code_shown`/`code_field` shapes.
