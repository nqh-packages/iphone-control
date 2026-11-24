# iphone-control extras: autonomous unlock and credential typing

Synthesis of two independent designs (`extras-design-opus.md`, `extras-design-gemini.md`), 2026-11-24. This file decides; the detail
(signatures, line cites, tests, build order) is in `extras-design-opus.md`, which is the base.

## Decisions

1. **Autonomous unlock** lives in the gateway, on demand: `autoUnlock` is called from `readyScreen` and at the start of `act`, outside the device's
   action lock (calling it from `gateOnce` would deadlock, `Unlock` takes that lock). It reuses `Gateway.Unlock`. Opt-in with `PINKY_AUTO_UNLOCK=1` in the daemon.
   - The PIN comes from a fork-owned keychain store (`secret.AgentPINs`) that never prompts; a locked keychain, a denied ACL and "nothing saved" are three different errors with fix lines.
   - One wrong attempt per saved PIN, ever (until `pinkyctl phone pin set` runs again). A PIN never entered waits a minute before the next try.
   - A person's phone is respected (takeover, or touched in the last minute). No saved PIN answers `device_locked` at once with the fix command.
   - The PIN is saved with `pinkyctl phone pin set` (no echo on a terminal, or a delivered file that is deleted); there is no `get` or `show`.
2. **Credential typing** is stage then type, through the owner's credential broker only (decision of 2026-11-24: broker only; a fork Keychain password store and in-process TOTP, Gemini's second resolver, were rejected).
   - The broker runs `cred run ... -- pinkyctl phone secret put -app <bundle id> NAME`, handing the value over a 0600 socket to an in-memory vault in the daemon (single use, 120 s, bound to one app).
   - The agent calls `phone_type` with `{{secret:NAME}}`; the gateway swaps the value in at the last moment in `perform`, just before the driver.
   - Owner-owned `credential_apps` in `rules.yaml`. Only a password or code field of a listed app accepts a secret; literal text into those fields stays refused everywhere.
     Listed apps lose only the bundled Finance and Password-manager blocks; payments there still ask.
   - The result, step text, history and approvals hold only the placeholder; typed values are masked in screen reads and events and covered in screenshots. Typing a secret is refused while "Hide codes from agents" is off.
   - The broker asks the owner to approve each staging, so credential typing is not fully autonomous. This is accepted.
3. **Settled open questions:** a listed bank app gets ordinary treatment (screenshots, recording); apps' own passcode keypads stay refused in v1; `rules.yaml` is only as strong as file access;
   the daemon binary is signed with the owner's Apple Development identity so the keychain ACL survives rebuilds (verified in build step 10).
4. **Settled risks of autonomous unlock** (review of slice 1, 2026-11-24):
   - A process of the owner's own user can run `pinkyctl phone pin set` and so reset the one-wrong-try latch. iOS's own lockout bounds the guessing, and such a process can already do worse as the user. Accepted.
   - The person-respect check runs once, before the passcode is entered, and the driver cannot see touches made on the phone itself. A person who picks the phone up during the seconds an unlock takes is not noticed until it is done. Accepted; stated in `docs/mcp.md`.
5. **Not in this design:** setting the phone's passcode the first time (a device step after unlock lands), and notification reading (a skill recipe, no gateway code).

## Build order

Slice 1, unlock: steps 5, 6, 7, 8 (unlock part), 9 (unlock docs), then device steps 10 and 11. Slice 2, credentials: steps 1 to 4, 8 (vault part), 9, then device step 12.

## Slice 1 (unlock) as built

Where the build differs from `extras-design-opus.md`, 2026-11-24:

- `secret.AgentPINs` takes a `PINStore` (`NewAgentPINsOn`, with `MemoryPINStore` for tests); `State` returns an error; the outcome callback is split into `Refused` and `Entered`, because `secret` must not import `driver` (the mapping is `pinOutcome` in `cmd/pinky/autounlock.go`). A handout starts the one-minute wait, and `Entered` clears it; `Unlock` also reports `nil` when it finds the phone already unlocked, so that wait is not left behind.
- A refusal is kept in the keychain and in memory, so a store that cannot take the marker still holds it for the run. The in-memory refusal is tied to an HMAC of the refused passcode (key in memory only), so `pin set` of a different passcode from another process is tried by the running daemon, and the same one stays refused until a restart.
- An iOS unlock that stops between keys (a failed key, a lost look at the lock) wraps `driver.ErrLocked`, as Android's does, so it latches the refusal; before the first key it wraps `ErrPINNotEntered`. The outcome handler runs after the device's action lock is released, because it writes the marker to the keychain.
- `pin set -file` opens the file once (no symlink, no blocking), reads at most 66 bytes (a 64-character passcode and a CRLF; a bigger file is refused and left alone), and deletes only the file it opened. Ctrl-C at the prompt puts the terminal's echo back.
- The seam guard also names two package variables (`idents`): `autoUnlockPINs` and `phonePINs` may be named only by the two files that build the store.
- `secret.PINFix` words the fix line of each error once, for the CLI and for the agent's note.
- `pin set` also takes `-file`, which is zeroed and deleted after reading, valid or not.
- An agent that may only watch never triggers an unlock (the design was silent).
- Per-device turn in `autoUnlock`, so overlapping calls enter the one PIN once.
- `internal/forkseams` guards `secret.NewAgentPINs` and `NewAgentPINsOn`; the seam is declared in `seams.yaml`, and `CLAUDE.md` (`AGENTS.md`) says how to extend it.
- Unproven until device steps 10 and 11: that the keychain's no-prompt flag alone stops the legacy access-list prompt in the daemon, the status codes of a locked keychain and of a denied read, and `unlockEstimate`.

## Slice 2 (credentials) as built

Where the build differs from `extras-design-opus.md`, 2026-11-24:

- The vault's staging method is `Deposit`, not `Put` (design): the seam guard matches methods by name, and `.Put(` is too common a name to guard.
- `vault.New` takes the policy's `SecretApp` check, asked at `Deposit` and again at `TakeSecret`, so taking an app off `credential_apps` strands a secret already staged for it. A value shorter than 4 characters is refused at staging: the masker cannot hide a string that short without blanking ordinary text. At most 16 secrets are staged at once.
- `StageSecret`, `Listen` and `Serve` are in `internal/vault/socket.go`; the CLI is `cmd/pinky/phone_secret.go` and the daemon wiring `cmd/pinky/secrets.go`. The vault always starts with the daemon (it is inert while `credential_apps` is empty); a socket that cannot start leaves the gateway with no vault, so every reference is refused with a fix line. The socket is `<runtime folder>/vault.sock`; `Listen` refuses a folder that is open to other users.
- The gateway hooks are `secretReady` (in `prepare`, end of the type case), `secretText` (in `perform`) and `maskSecrets` (in `observeOCR`). `secretText` takes the value before the focusing tap, as designed, so a failed tap spends the secret. A driver error raised while typing a secret never travels: `scrubSecret` replaces its text with one fixed sentence ("typing the secret failed; check the field and stage it again") and keeps only its class (offline, locked, timeout) for the result, so a quoted character or a length in the driver's text reaches no reader. A reference that reaches `perform` without `secretReady` having marked it is refused.
- `secretReady` leaves "not on the list" to the policy, so the person reads one reason (`credential_apps`); it checks only the shape, the store, Hide codes and that the value is staged for the app in front. The policy checks the field first, so a reference into a chat field in an unlisted app is refused as "not a password field".
- The approval card shows the usual title ("Type into “Password”?"), not the reference.
- `maskRead` is a package variable so the canary test can switch masking off and prove it fails (`TestSecretCanaryFailsWithoutMasking`).
- The seam guard learned **methods**: a seam may list `methods`, matched by name in any file not allowed (a file need not import the package). Rows: `secret vault` (`New`, `Listen`), `secret staging` (`StageSecret`), `secret hand-out` (`TakeSecret`) and `secret store input` (`Deposit`, no file outside the package).
- `phone_type`'s `text` schema description names `{{secret:NAME}}`, so agents learn it from `tools/list` (`TestCredentialReference`).
- Known exposures, not covered by this slice:
  - **Recordings** are never covered (they stay on the computer), and nothing pauses them for a type step: `pauseVideoLocked` only drops frames not yet confirmed by a read, so it cannot keep the frames after the typing out. A typed one-time code shows in a recording until its field leaves the screen; a password field shows dots.
  - **Approval pictures** are the person's own and uncovered (`gateway/codes.go`). An approval that opens while a typed code is on screen shows it to whoever reads `NeedView.Screenshot` on the control socket (any process of this user). The canary opens no approval at that moment. Covering only the typed-secret rectangle there is not a small change: nothing on a read records which masked element the vault changed (the one-time-code rule marks elements `Masked` too, and the person's own card shows those on purpose), so it needs a new per-screen record. Left open; the case that matters is a field that shows its last typed character for a moment on an approval opened at that instant (one character of a password, or a whole short code).
  - **The iOS driver's `Type` pastes through the phone's pasteboard** (`internal/driver/ios/type.go`: it saves the pasteboard, sets the text, pastes, restores), so a secret sits on the phone's general pasteboard for a moment, and falls back to keys only when the service is unavailable. Until the driver has a no-paste option (`driver.TypeOptions`, out of this slice), typing a secret on an iPhone through that path is not proven safe: settle it in device step 12 before relying on it.
  - The driver's input note (`TakeInputNote`) holds only fixed sentences and counts (`internal/driver/ios/type.go` `judgeTyped`, `internal/driver/android/typecheck.go` `noteHidden`, `noteUnread`, the "N characters where M were typed" counts), never field text, so nothing masks it. The read-back errors that carry counts reach no reader while a secret is typed (`scrubSecret`).
- Unproven until device step 12: whether WDA reports a secure field's value as dots or empty, that iOS shows no last typed character, and the socket's peer-uid check against a second user (the code mirrors `internal/api`'s).

## Settled risks of credential typing

Review of slice 2, 2026-11-24:

- **The staging socket cannot tell the broker's subprocess from another process of the same user.** Its only check is the peer uid, the stance of the unlock latch above. A same-user process can stage its own literal with `pinkyctl phone secret put`, or fill the 16 slots. What is fixed: a name that still holds a live secret (unused, unexpired) is refused (`ErrNameInUse`, with a fix line), so a later `put` cannot replace a value the broker staged; the 16-slot cap stays. What is accepted: the boundary is the owner's account, and `credential_apps` limits where a staged value can go.
- **Masking is exact-value, any letter case.** `Vault.Mask` hides the typed value wherever it shows whole, in any letter case. It does not hide other forms of it: with dots or spaces between characters, a part of it, one character per element, or an OCR misread. It also hides the same text where it is ordinary (a value that is a common word hides that word on the screen), and a reader who can type guesses and watch what turns into `••••` has an oracle for the value. Accepted limits: the owner stages secrets the broker generated or the owner's own, at least 4 characters.
- **Listing a password manager in `credential_apps` lifts its whole block**, so the agent can read that app's screen and its stored entries. Do not list one; list the apps a secret is typed into.
- **A malformed `rules.yaml` fails closed for `credential_apps`.** The daemon keeps the rest of the rules in force, but while the file cannot be read the list counts as empty: no secret is staged or typed, and the refusal says `rules.yaml cannot be read` with the parse error. The change is noticed on the next file check (a few seconds).
- **Android's clipboard.** The Android driver can type through the clipboard (`driver.Clipboard`, shared across the phone). This phone is an iPhone, so the path is out of this slice; a secret typed on an Android phone is not proven safe, and the vault is not meant for one until it is.
- **The seam guard skips folders named `build`, `vendor`, `testdata` and `node_modules`** (`skippedDirs` in `internal/forkseams/forkseams_test.go`), so a use of a guarded symbol in code under one of those names is not found. Go code is never kept there in this repository; if that changes, narrow the skip list in the same commit.
- **Recordings** stay uncovered, as listed above.

## Where a typed secret goes on an iPhone

The iOS 27 CoreDevice typing path (`Device.Type`, `internal/driver/ios/type.go`) pastes through the phone's pasteboard, so a typed secret may sit on it for a moment there, until the driver puts the old contents back. The iOS 26 WDA path, the one on the owner's phone (`WDADevice.Type` in `internal/driver/ios/wda_device.go`, `keys` in `wda.go` posting to `/wda/keys`), types by keys and does not touch the pasteboard.

## Settings actions that always ask

Under presets 1, 2 and 3, a tap in Settings (`com.apple.Preferences`) asks before the few things that cannot be undone or lock the person out (`guardRules` in `internal/policy/presets.go`; tests in `guard_test.go`):

| Rule name | Label contains (case-insensitive) |
|---|---|
| `Erase this iPhone` | `Erase All Content and Settings`, `Erase iPhone` |
| `Apple Account` | `Sign Out`, `Delete Account` |
| `Passcode` | `Turn Passcode Off` |

Payments already ask under preset 2. Nothing else new asks: messages, deletions, installs, links and other settings changes stay free there. The same words in any other app ask nothing new. The rules ride with the preset, not in `rules.yaml`, so an existing file, untouched or hand edited, gets them without being rewritten; preset 0 ("only the rules below") has none.

Unproven: the labels are the iOS Settings strings as known, not read from the phone. A label that differs (another language, a changed iOS version) is not guarded until it is added to `guardRules`.
