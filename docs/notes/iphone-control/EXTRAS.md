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
