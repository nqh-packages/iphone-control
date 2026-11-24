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
4. **Not in this design:** setting the phone's passcode the first time (a device step after unlock lands), and notification reading (a skill recipe, no gateway code).

## Build order

Slice 1, unlock: steps 5, 6, 7, 8 (unlock part), 9 (unlock docs), then device steps 10 and 11. Slice 2, credentials: steps 1 to 4, 8 (vault part), 9, then device step 12.

## Slice 1 (unlock) as built

Where the build differs from `extras-design-opus.md`, 2026-11-24:

- `secret.AgentPINs` takes a `PINStore` (`NewAgentPINsOn`, with `MemoryPINStore` for tests); `State` returns an error; the outcome callback is split into `Refused` and `Entered`, because `secret` must not import `driver` (the mapping is `pinOutcome` in `cmd/pinky/autounlock.go`). A handout starts the one-minute wait, and `Entered` clears it; `Unlock` also reports `nil` when it finds the phone already unlocked, so that wait is not left behind.
- A refusal is kept in the keychain and in memory, so a store that cannot take the marker still holds it for the run.
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
- The gateway hooks are `secretReady` (in `prepare`, end of the type case), `secretText` (in `perform`) and `maskSecrets` (in `observeOCR`). `secretText` takes the value before the focusing tap, as designed, so a failed tap spends the secret. A driver error that carries the value is scrubbed (`scrubSecret`). A reference that reaches `perform` without `secretReady` having marked it is refused.
- `secretReady` leaves "not on the list" to the policy, so the person reads one reason (`credential_apps`); it checks only the shape, the store, Hide codes and that the value is staged for the app in front. The policy checks the field first, so a reference into a chat field in an unlisted app is refused as "not a password field".
- The approval card shows the usual title ("Type into “Password”?"), not the reference.
- `maskRead` is a package variable so the canary test can switch masking off and prove it fails (`TestSecretCanaryFailsWithoutMasking`).
- The seam guard learned **methods**: a seam may list `methods`, matched by name in any file not allowed (a file need not import the package). Rows: `secret vault` (`New`, `Listen`), `secret staging` (`StageSecret`), `secret hand-out` (`TakeSecret`) and `secret store input` (`Deposit`, no file outside the package).
- `phone_type`'s `text` schema description names `{{secret:NAME}}`, so agents learn it from `tools/list` (`TestCredentialReference`).
- Known exposures, not covered by this slice:
  - **Recordings** are never covered (they stay on the computer), and nothing pauses them for a type step: `pauseVideoLocked` only drops frames not yet confirmed by a read, so it cannot keep the frames after the typing out. A typed one-time code shows in a recording until its field leaves the screen; a password field shows dots.
  - **Approval pictures** are the person's own and uncovered (`gateway/codes.go`). An approval that opens while a typed code is on screen shows it to whoever reads `NeedView.Screenshot` on the control socket (any process of this user). The canary opens no approval at that moment.
  - **The iOS driver's `Type` pastes through the phone's pasteboard** (`internal/driver/ios/type.go`: it saves the pasteboard, sets the text, pastes, restores), so a secret sits on the phone's general pasteboard for a moment, and falls back to keys only when the service is unavailable. Until the driver has a no-paste option (`driver.TypeOptions`, out of this slice), typing a secret on an iPhone through that path is not proven safe: settle it in device step 12 before relying on it.
  - The driver's input note (`TakeInputNote`) holds only fixed sentences and counts (`internal/driver/ios/type.go` `judgeTyped`, `internal/driver/android/typecheck.go` `noteHidden`, `noteUnread`, the "N characters where M were typed" counts), never field text, so nothing masks it; a typed value's length can show in a read-back error.
- Unproven until device step 12: whether WDA reports a secure field's value as dots or empty, that iOS shows no last typed character, and the socket's peer-uid check against a second user (the code mirrors `internal/api`'s).
