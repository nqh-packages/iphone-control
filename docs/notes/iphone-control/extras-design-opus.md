# iphone-control extras: autonomous unlock and credential typing

Designer: Claude Opus 5.5. Gate: `architect` (Phase A grounding, two or more candidates per addition, synthesis) then
`codebase-design` (depth, seams, smallest surface). Repo: `/Users/nqh/Desktop/iphone-control` at `738a8d71`, upstream
merge base `cffc5bad`. Read only; nothing was built or run against the phone, the daemon, the Keychain or the broker.

Line cites are `path:line` in that repo unless marked otherwise. **Unproven** marks a claim no code or output settles.

---

## 0. Grounding (Phase A)

### Unlock path today

- The gateway wakes a phone before a call reads or acts on it and never unlocks by itself: decision text at
  `internal/gateway/wake.go:15-21`, `wake()` at `wake.go:30-52`.
- `Gateway.Unlock(deviceID)` (`wake.go:69-140`) is the one unlock path: it needs `Options.PIN`
  (`gateway.go:77-81`, `wake.go:74`), gets the PIN within 2 min (`wake.go:56-58,85-87`), takes the device's action lock
  `d.act.Lock()` (`wake.go:95`), stops input and pauses the recording (`wake.go:96-100`), enters the PIN only on a lock
  screen (`wake.go:103-111`), records a history step without the PIN (`wake.go:122`, `recordUnlock` at `wake.go:306-325`),
  logs `ok`/`err` and never the PIN (`wake.go:116-121`), maps a refused PIN to `ErrWrongPIN` + `driver.ErrLocked`
  (`wake.go:124-128`) and answers the agent's lock handoffs (`wake.go:138`, `answerUnlockHandoffs` at `wake.go:228-239`).
  It runs on `g.ctx` with a 30 s bound (`wake.go:91`), not on the agent's call context.
- Drivers never retry: `driver.Unlocker` contract at `internal/driver/driver.go:555-566`; `ErrPINNotEntered` means no
  attempt was used (`driver.go:1521-1523`). The WDA path is `internal/driver/ios/wda_power.go:15-22` (Home, swipe, pad
  found in the tree, keys tapped, lock checked before each key). The lock screen pad is driven by the driver directly; the
  policy's hidden-keypad rule (`internal/policy/keypad.go:10-61`, enforced at `internal/gateway/pipeline.go:912-918`)
  applies to agent taps in apps, not to `Unlock`.
- In the daemon `Options.PIN` is nil ("the daemon has no one to confirm it", `cmd/pinky/daemon.go:131-133,214`); the
  desktop app sets it to `pinSource.pin` (`cmd/pinky/app.go:90`), which asks Touch ID through `Store.Reveal`
  (`app.go:438-470`).
- `secret.Get` refuses every account but model keys and the traffic CA key, on purpose (`internal/secret/secret.go:98-124`).
  The macOS store's `read` (`internal/secret/keychain_darwin.go:375-396`) does not set `kSecUseAuthenticationUIFail`;
  only `Has` does (`keychain_darwin.go:294-298`). The item's ACL trusts the binary that created it, and a rebuilt ad hoc
  Pinky gets the system's confidential-information prompt (`keychain_darwin.go:24-27`). `errSecInteractionNotAllowed` is
  -25308 (`keychain_darwin.go:44`). `newKeychain(service)` already exists for a separate service (`keychain_darwin.go:31-33`).
- Where an agent meets a locked phone (`StatusLocked`, `internal/gateway/agent.go:71`):
  - connect: `readyScreen` at `agent.go:1045`, result at `agent.go:1069-1071`;
  - observe: gate with `action=false` passes a locked phone (`pipeline.go:356-358`), then `readyScreen` at
    `agent.go:1399`, result at `agent.go:1425-1428`;
  - hand-back: `agent.go:2039-2061`; read_list: `read_list.go:476`, `:542`;
  - act: first `gate` at `pipeline.go:474` **before** `readyScreen` at `pipeline.go:480`; the gate's locked branch asks the
    phone once more (`unlockedNow`, `pipeline.go:378-386`, `wake.go:284-303`) and refuses;
  - act re-checks with `gate` **under `d.act`** at `pipeline.go:487`, and `gate` always passes `fresh=true`
    (`pipeline.go:313-315`). Anything that calls `Unlock` from `gateOnce`/`unlockedNow` would deadlock there, because
    `Unlock` takes `d.act` (`wake.go:95`, `d.act` is a `sync.Mutex`, `state.go:24`).
  - `readyScreen` callers all run without `d.act`: `pipeline.go:480` (lock at `:482`), `pipeline.go:1714` (lock at
    `:1715`), `read_list.go:476` (lock at `:478`), and the agent.go reads above.
- Takeover: `d.person` refuses actions (`pipeline.go:360-370`); `personRecent = time.Minute` (`background.go:28`) is the
  window in which a person's touches count as recent use (`background.go:131,276`).
- One note channel already reaches the agent's next result: `l.bgNote`, appended by `finish` (`agent.go:509-518`,
  `state.go:277-280`).
- Call budget is 45 s (`gateway.go:41`); `budgetLeft` (`budget.go:49`) and `outOfTimeBeforeAction` (`budget.go:88`) say
  "nothing was done". A WDA read over Wi-Fi can take 20-40 s (`read_list.go:471-473`).

### Credential typing today

- One policy rule denies typing into password and code fields on phones: `engine.go:197-203` (`RuleCredentials`,
  `policy.go:84`), lifted only on virtual phones with `virtual_credentials` (`file.go:25-29`, `presets.go:163-192`).
  Bundled-defaults text: `defaults.yaml:27-28`.
- Two gateway pre-checks enforce the same rule outside the engine, both through `CredentialTypingAllowed(virtual)`:
  hidden keypad taps (`pipeline.go:912-918`) and typing without `ref` and without a focused field while a credential field
  is on screen (`pipeline.go:937-941`, `credentialOnScreen` at `pipeline.go:1063-1071`).
- `IsCredentialField` (`detect.go:613-646`) is the field predicate.
- App blocks: `blockedLocked` returns the first app-level deny rule (`presets.go:443-454`); `Evaluate` skips app-level deny
  rules only for a per-task "allow for this task" (`engine.go:229-236`), never for password managers (`engine.go:229`,
  `unblock.go:26-37`). Upstream's per-task allow keeps no pictures, recording or screen text (`unblock.go:14-24`).
  `apps.yaml` `exclude` takes an app out of every category (`apps.go:45,101-110,118-123`; header `presets.go:456-462`),
  which would also drop its finance category, and with it payment detection that keys on categories (`engine.go:193-194`,
  `detect(in, cats)`).
- How `phone_type` text travels (`internal/mcp/tools.go:107-113,475-485` → `gateway.Action.Text`):
  - validated by length (`pipeline.go:106-112`);
  - hashed into the approval binding (`digest`, `pipeline.go:175-198`);
  - stored as history args (`args`, `pipeline.go:212-223`);
  - put in `policy.Input.Text` (`pipeline.go:921`);
  - shown on an approval card (`pipeline.go:1451-1452`) and in the step text and result (`describe`/`sayAction`,
    `pipeline.go:2901,2934-2942`);
  - sent to the driver in `perform` (`pipeline.go:2134-2147`), called at `pipeline.go:1864,1878`.
  - `phone_batch` steps go through the same `act` (`agent.go:2104`).
- The WDA driver sends text as `/wda/keys` (`internal/driver/ios/wda.go:548-549`); `wdaClient.call` marshals the body
  (`wda.go:209`) and no slog call sits in it (grep of `wda.go:401-440`). WDA's `Type` leaves no input note (no
  `TakeInputNote` in `wda_device.go`). The CoreDevice `Device.Type` (iOS 27 path) may paste (`type.go:85-87`).
- Masking: `MaskScreen`/`MaskElements` rewrite `Label` only and set `Masked` (`codes.go:44-101`), right after each read
  (`pipeline.go:2370-2372`). Pictures are covered where elements are `Masked`, **only while `HideCodes()`**
  (`internal/gateway/codes.go:52-58`); recordings are never covered (`gateway/codes.go:29-30`). A typed one-time code sits in
  the field's `Content`, which no masker touches.
- The tests that pin today's refusals: `internal/mcp/mcp_test.go:329` (typing `hunter2` into Password),
  `internal/policy/virtualcreds_test.go:15-68`, `internal/policy/credentials_test.go`.

### Owner's precedents and constraints

- iPad: the passcode lives in the login Keychain (`/Users/nqh/Desktop/ipad-local/AGENTS.md`, "Every device secret is
  stored encoded"). `tools/unlock/unlock` reads it with `security find-generic-password -w` and pipes it over ssh stdin
  (`tools/unlock/unlock`, `install_passcode`). An item that `security add-generic-password` creates trusts
  `/usr/bin/security`, so any shell on the Mac, agents' shells included, can read it silently with `-w`. That breaks "the
  tool can use it, no one can read it", so this design does not copy it.
- Broker (`~/.agents/skills/credentials/SKILL.md`): agents never see a secret. `cred run --login <id> --stdin '{password}'
  -- <command>` hands the secret to a child process on stdin, and `{otp}` is generated when the command starts. **Every
  call except `find` waits for the owner's approval on his phone.** No reveal operation exists. Nothing here calls it.
- Fork rule: keep the upstream diff small; lease decision 7 kept `internal/api` untouched
  (`docs/notes/iphone-control/DESIGN.md`, Decisions 7).

---

## 1. Usage (caller's view, written first)

### Owner, once per phone

```sh
# From a T3 secret card (delivery: file), or typed at a terminal (no echo):
pinkyctl phone pin set -device <udid> < "$file" && rm -f "$file"
pinkyctl phone pin check            # {"device":"<udid>","saved":true,"refused":false}
```

The owner lists the apps agents may sign in to in `rules.yaml`:

```yaml
credential_apps:            # bundle ids only, exact; names are not trusted
  - com.example.bank
  - com.example.shop
```

Daemon launch (fork launchd plist): `PINKY_AUTO_UNLOCK=1 pinky daemon`.

### Agent: a locked phone

Nothing to do. `phone_connect` / `phone_observe` / `phone_tap` on a locked phone come back unlocked. When Pinky cannot
unlock, the result is `device_locked` as before, with one more sentence and a `fix:` line, for example:

> The phone is locked. Call phone_handoff so the person can unlock it. Pinky did not unlock it: no passcode is saved for
> iPhone SE. fix: the owner runs `pinkyctl phone pin set -device <udid>`.

### Agent: signing in

```sh
cred find bank                                    # -> login lg_ab12
cred run --login lg_ab12 --reason "Sign in to the bank app on the agents' iPhone" \
  --stdin '{password}' -- pinkyctl phone secret put -app com.example.bank bank-pw
# {"staged":["bank-pw"],"app":"com.example.bank","expires_in_s":120}
pinkyctl phone call phone_type '{"ref":14,"text":"{{secret:bank-pw}}","submit":true}'
# settled: Typed “{{secret:bank-pw}}” into Password and submitted
```

One-time code from the broker (TOTP made when the command starts):

```sh
cred run --login lg_ab12 --reason "Enter the bank app's 2FA code" --stdin '{otp}' \
  -- pinkyctl phone secret put -app com.example.bank -ttl 25s bank-otp
pinkyctl phone call phone_type '{"ref":9,"text":"{{secret:bank-otp}}"}'
```

---

## 2. Addition A: autonomous unlock

### Candidates

| | Shape | Verdict |
|---|---|---|
| **U1** | **Gateway, on demand.** Two call sites that hold no `d.act` (`readyScreen`, and `act`'s first gate) call one fork function, `autoUnlock`, which reuses `Gateway.Unlock` unchanged. The PIN comes from a fork-owned store that never prompts and that owns the attempt state (refused, backoff). | **Chosen** |
| U2 | **CLI.** `pinkyctl phone` sees exit 5 / `device_locked`, reads the PIN itself and calls an unlock op over the control socket. | Lost. MCP clients get nothing. It is a second way to do one task, and the agent decides when to unlock (an agent switch). The CLI process holds the PIN. It needs an `internal/api` op, which lease decision 7 kept out. |
| U3 | **Event-driven.** The daemon unlocks in the background whenever `d.locked` flips true while a lease is held. | Lost. It unlocks when no call needs it, which keeps the phone awake against upstream decision 3 (`wake.go:15-21`). It races agent calls: the agent gets `device_locked` while the unlock runs. It adds goroutine and state the on-demand shape doesn't need. |

PIN store candidates:

| | Shape | Verdict |
|---|---|---|
| **S1** | **Fork store in package `secret`.** Service `app.pinky.agent`, account `secret.PINAccount(id)`. The item is created by the pinky binary itself, so the ACL trusts only that binary. The read never prompts (`kSecUseAuthenticationUIFail`). A refusal marker is a second item. | **Chosen** |
| S2 | `security add-generic-password` item (the iPad precedent). | Lost. Any shell reads it silently (`security ... -w`), agents' shells included. |
| S3 | PIN through the broker. | Lost. It needs one approval on the owner's phone per unlock, so the unlock is not autonomous. |

### Shape

**Data and ownership.** One owner per piece of state:

- the PIN and its attempt state (refused, backoff): `secret.AgentPINs` (fork);
- the decision to unlock now: `gateway.autoUnlock` (fork file);
- the attempt itself: upstream `Gateway.Unlock`, unchanged except one callback line.

```go
// internal/gateway/gateway.go (Options, +2 fields, upstream file)

// AutoUnlock lets an agent's call unlock a locked phone with Options.PIN
// itself, without the person, once per need: before the call reads or
// acts. It never unlocks while the person holds the phone. Default off.
AutoUnlock bool
// PINOutcome hears how each entry of a saved PIN ended (nil, ErrWrongPIN,
// driver.ErrPINNotEntered, other), so the PIN's owner can stop handing out
// a refused PIN. Nil: nobody listens.
PINOutcome func(deviceID string, err error)
```

```go
// internal/gateway/autounlock.go (new, fork)

// autoUnlock unlocks the lease's phone when Options.AutoUnlock is on and
// the phone is locked, nobody holds it, and the call has time; it reports
// whether the phone is unlocked now. It must be called without d.act: it
// goes through Unlock, which takes it. Why it did not unlock goes to the
// agent's next result (l.bgNote).
func (g *Gateway) autoUnlock(ctx context.Context, l *lease) bool

// unlockEstimate is what one unlock takes on the phone. It is a labelled
// calibration, set from the SE measurement in build step 11 (not decoded,
// not unlockTimeout's 30 s ceiling). autoUnlockReserve is the budget left
// for the read or action that follows.
const (
	unlockEstimate    = 12 * time.Second // calibration placeholder until step 11
	autoUnlockReserve = 8 * time.Second
)

// autoUnlocker is the fork's per-device state on Gateway (+1 upstream field):
// when the lock was last read for autoUnlock, so a call reads it at most
// once per wakeFresh (wake.go:26).
type autoUnlocker struct{ mu sync.Mutex; checked map[string]time.Time }

// unlockNote words an autoUnlock failure for the agent, with a fix line.
func unlockNote(name, deviceID string, err error) string
```

`autoUnlock` logic, in order:
1. `!g.opt.AutoUnlock || g.opt.PIN == nil` → return false, no note. Upstream behaviour and text are untouched.
2. Under `g.mu`, any of these returns false with no note: `l.ended`, `l.bg != nil`, `d.person`, or
   `time.Since(d.personAt) < personRecent`. The person's phone is theirs (`pipeline.go:360-370`, `background.go:28`).
3. Lock state, with no screen read:
   - cached `d.locked` true → go on;
   - cache unlocked and the lock last read under `wakeFresh` ago → return true;
   - otherwise one `g.checkLock(ctx, d)` (LockReporter: lockdown, `gateway.go:956-969`, `wda_power.go:24-40`; its cost on
     the SE is unproven) and record the time. Not locked → true.
4. `budgetLeft(ctx) < unlockEstimate + autoUnlockReserve` → false, note "Pinky unlocks it at the start of your next call;
   call phone_observe". The result stays `device_locked`, never uncertain. A connect that waited in the queue leaves
   about 15 s (DESIGN.md decision 4), so it gets this note, and the agent's next call unlocks with a fresh 45 s budget.
5. `err := g.Unlock(d.id)`. Unlock reads the PIN through `Options.PIN`, which returns `ErrNotFound` / `ErrPINRefused` /
   `ErrPINBackoff` / `ErrKeychainLocked` / `ErrKeychainDenied` before any key is pressed. On error: `l.bgNote =
   joinNote(l.bgNote, unlockNote(...))` and return false. On nil → true.
   - `Unlock` runs on `g.ctx` with its own 30 s bound (`wake.go:91`). If it outlives the call, the call's following read
     or action fails out of time with "nothing was done" (`budget.go:88`), and the phone stays unlocked for the next call.

Call sites (both hold no `d.act`):

```go
// internal/gateway/background.go:145 (readyScreen), +1 line after g.wake(ctx, d)
g.autoUnlock(ctx, l)

// internal/gateway/pipeline.go:473 (act), +1 line BEFORE the first gate,
// so the unlock gets the whole fresh budget and the gate's own locked
// branch (unlockedNow ≤3 s, withCurrentScreen ≤10 s, pipeline.go:378-397)
// never runs first
g.autoUnlock(ctx, l)
if res := g.gate(l, c.ApprovalID, true); res != nil {
```

`readyScreen` covers connect, observe, hand-back, read_list and approved actions (`agent.go:1045,1399,1548,2039,2047`,
`read_list.go:476`, `pipeline.go:1714`). The `act` line covers an action on a phone the cache already holds as locked,
which the first gate would refuse at `pipeline.go:378-386` before `readyScreen` runs. Inside one `act`, the second
`autoUnlock` (through `readyScreen`, `pipeline.go:480`) is a no-op, because the lock was read under `wakeFresh` ago. The
re-check under `d.act` (`pipeline.go:487`) and the in-act read (`pipeline.go:519-522`) keep their refusal: they fire only
when the phone locked between two lines, and the next call unlocks it. No hook goes into `gateOnce` or `unlockedNow`, so
there is no deadlock.

```go
// internal/gateway/wake.go:113-115 (Unlock), +3 lines after u.Unlock
err = u.Unlock(ctx, pin)
pin = ""
if g.opt.PINOutcome != nil { g.opt.PINOutcome(d.id, err) }
```

The callback hears person-initiated unlocks too, which is right: a PIN the phone refused is refused for both paths.

**PIN store (fork, package `secret`).**

```go
// internal/secret/agentpin.go (new, fork, no build tag: types and errors)

// AgentService is the keychain service of the passcodes the fork's daemon
// enters by itself. Only the pinky binary that saved an item may read it
// (the item's ACL), and reading never shows a prompt.
const AgentService = "app.pinky.agent"

var (
	ErrPINRefused     = errors.New("secret: the phone refused this saved passcode; Pinky will not enter it again")
	ErrPINBackoff     = errors.New("secret: the last unlock stopped before entering the passcode; Pinky waits before trying again")
	ErrKeychainLocked = errors.New("secret: the Mac's login keychain is locked, so the saved passcode cannot be read")
	ErrKeychainDenied = errors.New("secret: the keychain did not let this build of pinky read the passcode without a prompt")
)

// PINState is what can be known about a saved passcode without reading it.
type PINState struct{ Saved, Refused bool }

// AgentPINs keeps phone passcodes for unlocking without a person. It owns
// the attempt state: a passcode the phone refused stays refused (also
// across restarts) until a new one is saved; one that was not entered
// waits pinBackoff.
type AgentPINs struct { /* store Store; mu sync.Mutex; next map[string]time.Time; now func() time.Time */ }

func NewAgentPINs() *AgentPINs                                       // system keychain, AgentService
func (p *AgentPINs) Save(deviceID string, pin []byte) error          // validates printable ASCII 4..64; clears Refused
func (p *AgentPINs) Delete(deviceID string) error
func (p *AgentPINs) State(deviceID string) PINState                  // Has() only; never reads the value
func (p *AgentPINs) PIN(ctx context.Context, deviceID string) (string, error) // Options.PIN
func (p *AgentPINs) Outcome(deviceID string, err error)              // Options.PINOutcome

const pinBackoff = time.Minute
```

- Upstream `Has` cannot tell "nothing saved" from "keychain locked": with UI off, a keychain that needs unlocking reads
  as nothing saved (`keychain_darwin.go:294-296`). Without a fix, a locked keychain would send the owner to `pin set`,
  which is the wrong fix. So the fork store uses its own status read, `lookup(account) (found bool, err error)` in
  `agentpin_darwin.go`. It queries with `kSecUseAuthenticationUIFail` and returns the status code. For -25308 it asks
  `SecKeychainGetStatus` (legacy, default keychain) whether the keychain is locked:
  - locked → `ErrKeychainLocked`, fix: "log in on the Mac, or turn off Lock after sleep for the login keychain";
  - not locked → `ErrKeychainDenied`, fix: "run `pinkyctl phone pin set` with this build" (the ACL trusts another binary,
    Q5).
- **Unproven**, both to be settled in build step 10:
  - the exact status codes for each cause;
  - whether a launchd user agent's login keychain stays unlocked across sleep and screen lock.
- `PIN` returns:
  - `ErrNotFound` only when `lookup` says not found;
  - `ErrPINRefused` while the refused marker exists;
  - `ErrPINBackoff` inside the backoff;
  - otherwise the value, read with `kSecUseAuthenticationUIFail`.
- `Outcome`:
  - `ErrLocked` / `ErrWrongPIN` → `Save("refused:"+id, "1")`, the marker, which persists;
  - `ErrPINNotEntered` → in-memory backoff;
  - nil → clear the backoff.
- `internal/secret/agentpin_darwin.go` (new, fork) adds `readNoPrompt` beside the upstream `read`, using the same
  `loadSecurity`/`objects`/`query` (`keychain_darwin.go:173-226`). Upstream's `secret.Get` and its invariant
  (`secret.go:98-124`) are untouched. The fork's no-prompt reader is a separate, guarded entry point (§4).
- `internal/secret/agentpin_other.go` (new, fork): `NewAgentPINs` over `unsupported{}`.

The wrong-PIN rule: the phone gets each saved passcode wrong at most once, whatever restarts happen in between, until
someone runs `pin set` again. A refusal also stops the person's own Unlock button, with the same fix line, because the PIN
is the same.

**Daemon wiring (`cmd/pinky/daemon.go`, fork lines).**

```go
auto := os.Getenv("PINKY_AUTO_UNLOCK") == "1" && !c.Fake
var pins *secret.AgentPINs
if auto { pins = secret.NewAgentPINs() }
// gateway.Options:
PIN:        pinFunc(c.PIN, pins),     // pins.PIN when auto, else c.PIN (nil in the daemon)
AutoUnlock: auto,
PINOutcome: outcomeFunc(pins),        // nil when !auto
```

**CLI: saving the PIN (`cmd/pinky/phonepin.go`, new, fork).**

```
pinkyctl phone pin set    [-device ID]   read one line from stdin; on a tty, prompt on stderr with echo off
pinkyctl phone pin check  [-device ID]   {"device":..., "saved":bool, "refused":bool}; never reads the value
pinkyctl phone pin delete [-device ID]
```

- There is no `get` or `show`.
- The id must be the gateway's own device id, because a mismatch makes every unlock answer "no passcode saved".
  - When the daemon is up, `pin set` reads `phone status` (`cmd/pinky/phone.go`, the `device` key) and refuses an id the
    gateway does not list.
  - When it is down, it checks the id against `PINKY_ALLOW_DEVICES`, the exact ids the gateway accepts
    (`gateway.go`, `AllowDevices`; `cmd/pinky/daemon.go:276-286`).
  - `-device` defaults to the one id there. Several or none is a usage error (exit 2) that lists the choices.
- Echo off uses `golang.org/x/sys/unix` termios (already a direct dependency, `go.mod:21`), so no new module.
- The value never sits in an argument, an environment variable or a file the CLI writes. The delivered-file form reads it
  through stdin redirection, and the caller deletes the file.
- `set` refuses empty input, a second line (it reads exactly one) and characters the pad cannot type: printable ASCII
  only, as `pinStrokes` requires (`internal/driver/ios/power.go:168-180`).
- It prints `{"device":...,"saved":true}`.
- The CLI is the same binary as the daemon (`cmd/pinky/main.go:103-108`), so the item's ACL trusts the daemon.

**Logs, history, recordings.**

- Unlock logs only `ok`/`err` (`wake.go:116-121`). Driver errors carry no PIN (`wake.go:117`).
- The history step text is fixed (`wake.go:306-318`). The recording pauses during entry (`wake.go:98-100`).
- The auto path adds one slog line, `auto unlock` with `device`, `ok` and `why` (a fixed reason code, never a value).

---

## 3. Addition B: credential typing

### Candidates

| | Shape | Verdict |
|---|---|---|
| **C1** | **Stage, then type.** The broker runs `pinkyctl phone secret put` as its child with the secret on stdin. The CLI hands it over a fork socket to an in-memory vault in the daemon: one use, a TTL, bound to one app. `phone_type` carries `{{secret:NAME}}`, which the gateway resolves at `perform`, the last line before the driver. | **Chosen** |
| C2 | **The gateway calls the broker at type time.** | Lost. The broker waits minutes for the owner's phone, but a call has 45 s (`gateway.go:41`). The gateway would need its own pending/resume protocol that copies the broker's `waiting`/`resume`, and it would have to map apps to broker logins. |
| C3 | **New MCP tool `phone_type_secret{login,ref}`.** | Lost. It adds a tool surface (lease decision: no new MCP tool), has C2's budget problem, and duplicates `phone_type`'s whole prepare/approval path. |
| C4 | **A fork Keychain store of app passwords**, no broker. | Lost. The owner chose the broker. It would be a second credential store with its own rotation, and it has no TOTP. |

Block-lifting candidates for apps on the list:

| | Shape | Verdict |
|---|---|---|
| **L1** | **The policy lifts exactly the two bundled rules, `Finance apps` and `Password managers`** (`RuleFinance`, `RulePasswords`, `appchange.go:36-37`; `defaults.yaml` rules), for apps on `credential_apps`, in `blockedLocked` and in `Evaluate`'s rule loop. Categories stay, so payments in a bank app still ask. These are never lifted, so "strictest wins" (`defaults.yaml:4-6`) holds: `Blocked by you` (a built-in rule, `presets.go:39,74-76`), owner-written app-level deny rules, System apps, and Pinky's own apps (`engine.go:187-190`). | **Chosen** |
| L2 | The owner adds the app to `apps.yaml` `exclude`. | Lost. It removes the finance category, and payment detection with it (`engine.go:193-194`). It is also a second list the owner must keep in step with `credential_apps`. |
| L3 | Seed the lease's per-task allow list (`l.unblocked`) with `credential_apps`, keeping upstream's no-pictures treatment. | Not chosen. Password managers stay denied there (`engine.go:229`), changes reach only the next lease, and it needs gateway edits. It stays an option if the owner wants text-only (open question Q2). |

### Shape

**Placeholder grammar (one declaration).**

```go
// internal/vault/ref.go (new, fork)

// A secret reference is the whole text of a phone_type: {{secret:NAME}},
// NAME of 1-40 characters [a-z0-9._-]. It is the only way a secret reaches
// a phone, and the only text Pinky ever records for it.
func ParseRef(text string) (name string, isRef bool, err error)
// err: the text holds "{{secret:" but is not exactly one reference
// ("a secret reference must be the whole text; type the rest in another phone_type").
func Ref(name string) string
func ValidName(name string) bool
```

Mixed text such as `user {{secret:pw}}` is refused, so a secret never has neighbours that could be read back in a field.

**Vault (fork package, the one owner of staged and recently typed values).**

```go
// internal/vault/vault.go (new, fork)

type Vault struct { /* mu; slots map[string]*slot; typed []typed; now func() time.Time; allowed func(app string) bool */ }

// New returns an empty vault; allowed is the policy's credential_apps check
// (Engine.SecretApp), asked again at Put and at Take.
func New(allowed func(appID string) bool) *Vault

// Put stages values by name for one app, for ttl (default 120 s, at most
// 300 s), each used once. It refuses an app that allowed refuses.
func (v *Vault) Put(app string, ttl time.Duration, named map[string][]byte) error

// Staged reports whether Take would hand out name for app now, without
// using it (ErrNotStaged, ErrWrongApp, ErrAppNotAllowed).
func (v *Vault) Staged(name string, app screen.App) error

// TakeSecret hands out name's value for app once and forgets it; the value
// is kept, for masking only, for typedFor (10 min).
func (v *Vault) TakeSecret(name string, app screen.App) (string, error)

// Mask replaces every value typed in the last typedFor wherever it shows
// in a read with "••••": Element.Label (iOS puts a field's typed value
// there, screen.go:83-88), Element.Content, OCR elements (Elements with
// Source OCR) and Event.Text (screen.go:174-175). It marks the changed
// elements Masked, so pictures of them are covered (gateway/codes.go).
// Element.ID and Path are platform identifiers, never typed text, and are
// left alone. It reports how many it changed.
func (v *Vault) Mask(s *screen.Screen) int
```

`Mask` runs before `s.Events` is taken out of the read (`pipeline.go:2372-2378`), so toasts and notifications that echo
a typed value are masked too.

- App matching is by **bundle id only, exactly** (`app.ID`). A missing id refuses. An app could call itself "Bank"; it
  cannot take the bank's id.
- Values are `[]byte`, zeroed on take, on expiry and when they leave the masking window (best effort in Go). They live
  in memory only, so a daemon restart drops them.

**Staging transport (fork socket, not `internal/api`).**

```go
// internal/vault/socket.go (new, fork)
func (v *Vault) Serve(ctx context.Context, ln net.Listener) error   // one request per connection
func StageSecret(ctx context.Context, sock string, app string, ttl time.Duration, names []string, values io.Reader) (Staged, error)
type Staged struct { Names []string `json:"staged"`; App string `json:"app"`; ExpiresInS int `json:"expires_in_s"` }
```

- The socket path is `<the daemon socket's directory>/vault.sock` (dir of `dirs.Socket()`, `cmd/pinky/phone.go:129`),
  mode 0600.
- Each connection is checked with `getpeereid` (`unix.GetsockoptXucred`, darwin), which must match the daemon's uid.
- Frame: a JSON header line `{app, ttl_ms, names}`, then one line per name. The reply is the `Staged` JSON or `{error,
  fix}`.
- `cmd/pinky/phonesecret.go` (new, fork) is `pinkyctl phone secret put -app ID [-ttl D] NAME...`. It reads one stdin line
  per NAME. On a tty it reads with echo off, for the owner's own use. It prints `Staged` and has no other verbs.

**Policy (smallest change).**

```go
// internal/policy/file.go (+4 lines): File
// CredentialApps lists, by bundle id or package name, the apps where agents
// may type a secret from the credential broker ({{secret:NAME}}) into
// password and one-time-code fields. A finance app or password manager on
// it is not blocked; its payments still ask. Literal text into those
// fields stays refused everywhere. The owner edits it; agents cannot.
CredentialApps []string `yaml:"credential_apps,omitempty"`

// internal/policy/policy.go (+3 lines): Input
// Secret is set when the text is a secret reference (vault.ParseRef); its
// value is never in Text.
Secret bool

// internal/policy/secrets.go (new, fork)
func (e *Engine) SecretApp(appID string) bool                   // exact id in CredentialApps
func (e *Engine) secretVerdictLocked(in Input, kinds []string) (Verdict, bool) // decides every in.Secret type
func (e *Engine) secretLiftLocked(app screen.App, r Rule) bool  // app.ID on the list and r.Name is RuleFinance or RulePasswords
```

`secretVerdictLocked`, for `in.Secret` only (literal text never reaches it):

| condition | verdict |
|---|---|
| target unknown, or not `IsCredentialField` | Deny, rule `credentials`: "A secret goes only into a password or one-time-code field; this is not one. Nothing was done. fix: pass the ref of the password or code field." |
| `app.ID` not in `credential_apps` | Deny, rule `credentials`: "<App> is not on the owner's credential_apps list, so Pinky types no secret there. Nothing was done. fix: ask the owner to add <id> to credential_apps in rules.yaml, or call phone_handoff." |
| otherwise | fall through to the rest of `Evaluate` with the credentials check satisfied (scope, task apps, read-only and the presets still apply) |

Upstream edits in `engine.go`:
```go
// engine.go:197, replaces the condition's head (+3 lines)
if in.Secret {
	if v, done := e.secretVerdictLocked(in, kinds); done { return v }
} else if in.Op == OpType && IsCredentialField(in.Target) && !(in.Virtual && e.file.VirtualCredentials) {
// engine.go:234 (+1 condition)
if (lift || e.secretLiftLocked(app, r)) && r.Decision == Deny && r.Match.appLevel() {
// presets.go:449 (+1 condition)
if r.Decision == Deny && r.Match.appLevel() && r.matchesApp(app, cats) && !e.secretLiftLocked(app, r) {
```

The two gateway pre-checks (`pipeline.go:912,937`) stay as they are. The keypad check refuses taps, not text, and
secrets never go to an in-app keypad in v1 (Q3). The no-focus check refuses a secret too, which is wanted: a secret needs
a known field.

**Gateway (resolve at the last moment).**

```go
// internal/gateway/gateway.go (Options, +3 lines)
// Secrets resolves {{secret:NAME}} in phone_type; nil refuses every reference.
Secrets *vault.Vault

// internal/gateway/secrets.go (new, fork)

// secretReady checks a type step whose text is a reference before the
// policy and any approval: a store exists, codes are masked (HideCodes,
// so pictures of a typed code get covered), the target is known, and the
// value is staged for the app in front. It sets p.input.Secret.
func (g *Gateway) secretReady(a Action, p *prep, cur *screen.Screen) *Result

// secretText returns what perform types: a's text, or the staged value for
// a reference, taken now (vault.TakeSecret). An error wraps
// driver.ErrNotFound, so actionError says nothing was done
// (pipeline.go:2218-2219); it is returned before the focusing tap.
func (g *Gateway) secretText(a Action, p *prep) (string, error)

// maskSecrets masks values typed lately in a read (vault.Mask).
func (g *Gateway) maskSecrets(s *screen.Screen)
```

Upstream hook lines in `pipeline.go`:
```go
// prepare, at the END of the type case, after the ref / focused / no-focus
// branches that set p.target (:922-941), before `case "swipe"` (+3);
// prepare returns before g.evaluate runs, so the policy sees p.input.Secret
if res := g.secretReady(a, p, cur); res != nil { return p, res }
// perform, type case, at the top of :2140 (+4); text is passed at :2147 in place of a.Text
text, err := g.secretText(a, p)
if err != nil { return err }
// ... return dev.Type(ctx, text, ...)
// observeOCR, after g.policy.MaskScreen(s) at :2372 (+1)
g.maskSecrets(s)
```

`prep` keeps the app the reference was checked for (`p.input.App`, `pipeline.go:859-861`). `TakeSecret` matches it again
at `perform`, so an app switch between prepare and perform refuses. The switch check `switchedApp` (`pipeline.go:524-528`)
already turns such a switch into `stale`.

**What each reader sees.**

| reader | shows | why it is safe |
|---|---|---|
| agent result, step text | `Typed “{{secret:bank-pw}}” into Password` | `describe` uses `a.Text` (`pipeline.go:2935`) |
| history args | `{"ref":14,"text":"{{secret:bank-pw}}"}` | `args` uses `a.Text` (`pipeline.go:221-222`) |
| approval binding / card | digest and title of the reference | `pipeline.go:183,1452` |
| next screen read | password field: dots (iOS; WDA's value for a secure field unproven); code field: `••••`, `Masked` | `vault.Mask` after `MaskScreen` |
| screenshots to the agent and in history | code field covered | `Masked` elements are covered while `HideCodes()` (`gateway/codes.go:52-58`). `secretReady` refuses when `mask_codes` is off ("fix: turn on Hide codes from agents"). |
| recording | paused for the type step (unproven on resume); after it, a password field shows dots, and a code field shows digits until it leaves the screen | recordings stay on the Mac and never go to agents (`gateway/codes.go:29-30`); see risk R3 |
| logs | nothing: no slog line carries `a.Text` or the value; WDA's client logs no body (`wda.go:209,401-440`) | guarded by the canary (§4) |
| broker, owner's phone | the command `pinkyctl phone secret put -app <id> <name>` and the reason | the owner sees which app gets it before approving |

**Failure messages** (each ends with `fix:`):

- no vault: "This Pinky has no secret store, so {{secret:…}} cannot be typed. Nothing was done. fix: run the fork's
  daemon (pinky daemon) or call phone_handoff."
- not staged or expired: "No secret named bank-pw is staged for com.example.bank (staged secrets last 120 s and are used
  once). Nothing was done. fix: stage it again: cred run --login <id> --reason '…' --stdin '{password}' -- pinkyctl phone
  secret put -app com.example.bank bank-pw"
- wrong app: "bank-pw was staged for com.example.bank, but com.other is in front. Nothing was done."
- mixed text: invalid_argument, from `ParseRef`.
- off list, not a credential field: from the policy (above).
- Put to an app not on the list, at staging time, so the owner's approval isn't wasted on a doomed call: "com.x is not on
  credential_apps; fix: the owner adds it in rules.yaml."

Apps not on the list: literal text into a credential field is denied exactly as today (`engine.go:197-203`, message
unchanged, and `mcp_test.go:329` passes unchanged). A reference is denied with the `credential_apps` message. Approvals
the person starts still work: an Ask verdict from a preset (Payments) opens the same approval path, and the binding is the
reference's digest.

---

## 4. Guards (owner's rule: declaration, a failing check with `fix:`, fixtures that prove the guard)

| Seam | Declared in | Guard | Fixture proof |
|---|---|---|---|
| No-prompt PIN reader `secret.NewAgentPINs` is built only by the daemon and the `pin` CLI | `internal/forkseams/seams.yaml` | `internal/forkseams/forkseams_test.go`: parses every `.go` file (`go/parser`, stdlib), finds selectors of the declared symbols, fails with `file:line rule fix:` for a use outside `allowed`. Also fails when an allowed path does not exist, or the symbol is declared twice. | `testdata/violations/*.go.txt`, one per rule: the test asserts each is reported. |
| Only `internal/gateway/secrets.go` calls `(*vault.Vault).TakeSecret`; only `internal/vault/socket.go` calls `Put` | same file | same test | same fixtures |
| A secret never leaves the vault except to the driver | `internal/gateway/secrets_test.go` doc comment | **Canary**: stage `S3NT1NEL-7f2a` (fake iOS-like device, password field and code field that echo typed text into `Content`), run `phone_type` by ref, `phone_batch` with a type step, then `phone_observe` with screenshot. Assert the sentinel is absent from the MCP results, step text and args, the history DB rows, the captured slog buffer (`Options.Logger`) and the agent's next screen, events included: the fake also emits a toast event that contains the sentinel. For the screenshot, the canary asserts the code field's rectangle is filled with `codeFill` (`gateway/codes.go:33`); a byte search of PNG data would prove nothing. Assert the fake driver received it once. `fix:` names the reader that leaked. | The test first runs with `maskSecrets` stubbed out and expects a failure, proving it can fail. |
| Exfiltration | `internal/gateway/secrets_test.go` | reference into a chat field with `submit` → blocked; app off list → blocked; staged for A, B in front → refused, and the slot is still unused; second Take → not staged; expired → not staged; mixed text → invalid_argument; `mask_codes` off → refused | table rows |
| Policy contract | `internal/policy/secrets_test.go` | table over secret × on-list × field kind × virtual × category (finance, passwords, blocked-by-you). Literal text stays denied on a listed app. A listed finance app is not blocked and Payments still asks. A Blocked-by-you app stays blocked. An unlisted reference gets a reason that names `credential_apps`. | existing `virtualcreds_test.go`, `credentials_test.go`, `mcp_test.go:329` keep passing |
| Unlock contract | `internal/gateway/autounlock_test.go` | fakes implementing `Unlocker` + `LockReporter`. AutoUnlock off → `device_locked` text byte-identical to upstream. On:<br>- connect on a locked phone → one Unlock, then settled;<br>- action on a cached-locked phone → unlock then act, under a 5 s test deadline (catches the `d.act` deadlock);<br>- wrong PIN → one attempt, then every later call has no attempt and gets the `pin set` fix line;<br>- not entered → no second attempt inside the backoff;<br>- no PIN → `device_locked` within 1 s, with no Unlock;<br>- person holding the phone → no Unlock;<br>- low budget → no Unlock, and not `uncertain`;<br>- PIN absent from logs and history;<br>- slow fake read (20 s) after a queued connect → note, no Unlock, never `uncertain`. | each row |
| PIN store | `internal/secret/agentpin_darwin_test.go` (test service, as `keychain_darwin_test.go`) | save then state; read with interaction disabled (`setInteractionAllowed(false)`, `keychain_darwin.go:399-408`); refused marker survives a new `AgentPINs`; Save clears it; a status seam maps -25308 to `ErrKeychainLocked` when the keychain reports locked and to `ErrKeychainDenied` otherwise, and neither becomes `ErrNotFound` | fake status rows for each code |
| CLI never prints a secret | `cmd/pinky/phonepin_test.go` | every `phone pin` and `phone secret` verb run with a memory store holding a sentinel: stdout and stderr never contain it; `set` rejects empty, two lines and non-ASCII; `secret put` without `-app` → exit 2 | — |

The guard and how to extend it go in the fork's `AGENTS.md`: "a new secret-holding symbol adds a `seams.yaml` entry".

---

## 5. Surface and diff

Public surface added:
- agents: the `{{secret:NAME}}` convention in `phone_type`;
- broker child: `pinkyctl phone secret put`;
- owner: `pinkyctl phone pin set|check|delete`, `credential_apps` in rules.yaml and `PINKY_AUTO_UNLOCK=1`.

There are no new MCP tools and no new `internal/api` ops.

### Upstream files touched (estimated lines added/changed)

| file | change | ± |
|---|---|---|
| `internal/gateway/gateway.go` | Options: AutoUnlock, PINOutcome, Secrets; Gateway field `auto autoUnlocker` | +13 |
| `internal/gateway/wake.go` | PINOutcome call after `u.Unlock` | +3 |
| `internal/gateway/background.go` | `g.autoUnlock(ctx, l)` in readyScreen | +1 |
| `internal/gateway/pipeline.go` | act gate retry; prepare `secretReady`; perform `secretText`; observeOCR `maskSecrets` | +11 |
| `internal/policy/policy.go` | Input.Secret | +3 |
| `internal/policy/file.go` | File.CredentialApps | +6 |
| `internal/policy/engine.go` | secret branch; rule-loop lift | +4 |
| `internal/policy/presets.go` | blockedLocked lift | +1 |
| `internal/policy/defaults.yaml` | comment: credential_apps, the reference-only exception | +8 |
| `cmd/pinky/daemon.go` | wiring: pins, vault, socket, env | +14 |
| `cmd/pinky/phone.go` | dispatch `pin`, `secret`; help lines | +6 |
| `internal/mcp/tools.go` | `typeIn.Text` jsonschema description names `{{secret:NAME}}` (`tools.go:109`), so agents learn it from the live `tools/list` | +1 |
| `docs/mcp.md` | phone_type: references; locked phones | +20 |

About 90 lines across 13 upstream files (about 50 of them code), each edit a hook line or a field.

### New fork files

| file | est. lines |
|---|---|
| `internal/gateway/autounlock.go` | 70 |
| `internal/gateway/secrets.go` | 90 |
| `internal/gateway/autounlock_test.go` | 260 |
| `internal/gateway/secrets_test.go` | 300 |
| `internal/policy/secrets.go` | 60 |
| `internal/policy/secrets_test.go` | 160 |
| `internal/secret/agentpin.go` | 90 |
| `internal/secret/agentpin_darwin.go` | 70 |
| `internal/secret/agentpin_other.go` | 15 |
| `internal/secret/agentpin_darwin_test.go` | 100 |
| `internal/vault/ref.go` | 40 |
| `internal/vault/vault.go` | 150 |
| `internal/vault/socket.go` | 130 |
| `internal/vault/vault_test.go` | 220 |
| `cmd/pinky/phonepin.go` | 140 |
| `cmd/pinky/phonesecret.go` | 80 |
| `cmd/pinky/phonepin_test.go` | 160 |
| `internal/forkseams/seams.yaml` | 25 |
| `internal/forkseams/forkseams_test.go` | 140 |
| `internal/forkseams/testdata/...` | 30 |
| `docs/notes/iphone-control/EXTRAS.md` (decisions) | 60 |

Left alone:
- `internal/api/*`; the `phone_type` handler in `internal/mcp/tools.go:481-485` (the reference is ordinary text; only
  the description line changes);
- `internal/driver/*`, `internal/secret/secret.go`, `internal/secret/keychain_darwin.go`;
- `cmd/pinky/app.go` (the desktop app keeps Touch ID);
- `internal/gateway/codes.go`, `internal/policy/keypad.go`.

Outside the gateway, by design:
- PIN provisioning (`pin set`) and secret staging (`secret put`) are CLI verbs.
- The PIN store is in package `secret`; the vault and its socket are package `vault`.
- Wiring is in `daemon.go`.

The gateway learns two booleans' worth of behaviour and three hook calls.

---

## 6. Tradeoffs accepted

- We accept one owner approval on the phone per staging (the broker's rule) in exchange for never holding app secrets
  outside the broker. A password plus a TOTP costs two approvals unless Q6 holds.
- We accept that a listed bank app is an ordinary app (screenshots, recording) in exchange for one list and kept payment
  detection. L3 is the stricter alternative.
- We accept that an unlock may use one wrong attempt per saved passcode, ever, in exchange for unlocking with no person.
- We accept that the act re-check under `d.act` and the in-act read still answer `device_locked` when the phone locks
  between two lines, in exchange for no lock-order change upstream. The next call unlocks.
- We accept over-masking: any 4+ character string equal to a value typed in the last 10 minutes is masked anywhere on
  screen. In exchange, the masker needs no field knowledge.
- Reusing `l.bgNote` for the unlock note keeps `finish` unchanged. Its name is narrower than its use now. A rename is an
  upstream-wide edit, so it is left.

## 7. Risks and open questions (the orchestrator must see Q1)

- **Q1. Autonomy vs the broker.** Every `cred run` waits for the owner's approval on his phone (credentials SKILL.md,
  Rules). Credential typing is therefore never fully autonomous; unlock is. Options:
  - accept it;
  - the broker grows a standing approval for one login and one destination command (a broker change, outside this fork);
  - C4, which the owner already ruled out.
- **Q2.** Should allowed banking apps keep upstream's text-only treatment (no pictures, no recording, `unblock.go:14-24`)?
  If yes, take L3 in place of L1.
- **Q3. In-app passcode keypads.** Many banking apps draw their own capture-proof keypad (`keypad.go:10-16`), which
  `phone_type` cannot reach. v1 excludes them: they stay refused (`pipeline.go:912-918`). A v2 sketch: `phone_type` with a
  reference while `HiddenKeypad` covers the target, and the gateway taps the digit keys by label.
- **Q4. The list is only as strong as file access.** `rules.yaml` is re-read every 2 s (`gateway.go:58-60`), and an agent
  with a shell on the Mac could edit it. Mitigations to weigh:
  - the daemon logs and emits an event when `credential_apps` changes;
  - the list moves into a Keychain item the CLI writes.
- **Q5. ACL across rebuilds (unproven).** With ad hoc signing each `go build` changes the binary the item trusts, and the
  daemon then gets `ErrKeychainDenied` until `pin set` runs again. Fix candidate: sign `pinky` with the owner's Apple
  Development identity (team `9V2FMKQ3WU`), whose designated requirement survives rebuilds. Build step 10 proves or
  refutes it.
- **Q6 (unproven).** Can one `cred run --stdin` template span lines (`'{password}\n{otp}'`)? If yes, `secret put` takes
  two names in one approval. `cred` was not run.
- **R1.** The iOS 27 CoreDevice `Device.Type` may paste (`internal/driver/ios/type.go:85-87`), which would leave a secret on
  the phone's pasteboard. The SE runs iOS 26.2, which uses WDA keys (`wda.go:548-549`). Before iOS 27 secret typing, add
  `driver.TypeOptions.NoPaste`.
- **R2.** iOS may show a secure field's last typed character for a moment (unproven). Screenshots are taken after settle;
  the recording is paused for the step.
- **R3.** The recording resume behaviour after `pauseVideoLocked` (`video.go:664-670`) is unproven, and a typed code
  stays visible in a local recording until the field leaves the screen.
- **R4.** Lockdown `PasswordProtected` as "locked now" (`wda_power.go:24-60`) is upstream's reading, unproven here. A
  wrong reading makes `autoUnlock` a no-op (Unlock checks the lock first, `wake.go:105`), never a stray PIN.
- **R5.** WDA's value for a secure field (dots or empty) is unproven; the canary covers what Pinky reads either way.
- **R6.** `autoUnlock` reads the lock once per `wakeFresh` per call when the cache says unlocked. The cost of that
  lockdown read on the SE over Wi-Fi is unproven. If it is large, trigger on the cached `d.locked` only, and accept that a
  read which finds the phone freshly locked answers `device_locked` once.
- **R7.** `unlockEstimate` is a calibration, not a decoded value. Step 11 measures it, and it is labelled as such in code.

## 8. Build order (each step ends on a command that prints pass or fail)

1. **Policy: `credential_apps`, `Input.Secret`, secret verdict, lift.**
   `go test ./internal/policy -run 'Secret|Credential|VirtualCredentials|Blocked'` passes, and
   `go test ./internal/mcp -run Credential` still refuses `hunter2`.
2. **Vault core: `ParseRef`, `Put`/`Staged`/`TakeSecret`/`Mask`, TTL, one use, app by id.**
   `go test ./internal/vault -run 'Ref|Vault|Mask'`.
3. **Gateway hooks for secrets and the canary.**
   `go test ./internal/gateway -run 'Secret' -race`; the canary fails with `maskSecrets` stubbed, then passes.
4. **Vault socket and `pinkyctl phone secret put`.**
   `go test ./internal/vault -run Socket` and `go test ./cmd/pinky -run PhoneSecret`.
5. **`secret.AgentPINs` (darwin keychain, test service).**
   `go test ./internal/secret -run AgentPIN`.
6. **`pinkyctl phone pin set|check|delete`.**
   `go test ./cmd/pinky -run PhonePin`.
7. **Gateway auto-unlock: Options, `autoUnlock`, `PINOutcome`, two call sites.**
   `go test ./internal/gateway -run 'AutoUnlock|Unlock' -race`, including the deadlock row under its deadline.
8. **Daemon wiring and seam guard.**
   `go test ./cmd/pinky -run Daemon` and `go test ./internal/forkseams`; each violation fixture is reported.
9. **Docs** (`docs/mcp.md`, `defaults.yaml` comment, `EXTRAS.md`, fork `AGENTS.md` guard line), then the repository gate
   once: `make check`.
10. **ACL across rebuilds (Q5).** Build and `pin set` from a delivered file, then rebuild and start the daemon. Pass:
    `pinkyctl phone pin check` shows `saved` and the first locked call's log reads `auto unlock ok=true` with no prompt.
    Fail: `ErrKeychainDenied`, which decides the signing question.
11. **Device, unlock (happy path only; a wrong PIN is proven with fakes, never on the SE).** Lock the SE, then run
    `pinkyctl phone call phone_observe`. Pass: `settled`, unlocked, and a history step "Unlocked from the computer" with no
    PIN in the step, log or recording.
12. **Device, sign-in.** Use an owner-created test login in an app on `credential_apps`: broker stage, then `phone_type`
    by ref, then observe. Pass: signed in, and the result, history, log and screenshot hold only the reference or `••••`.
    Run `scripts/scan-identifiers.py`-style evidence hygiene before committing evidence.
