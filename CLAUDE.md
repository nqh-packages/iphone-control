# Pinky

Pinky lets AI agents operate a person's real iPhone or Android phone over MCP, with the person watching, approving and taking over. It is a Go desktop app (MyGo native UI) with an Android companion app; see [README.md](README.md).

Read [docs/WORKING.md](docs/WORKING.md) before you change anything: who owns which part of the code, how to use the real phones safely, and the rules for code and commits. Build and test with `make check` (build, vet, test); [README.md](README.md) has the rest.


## This is iphone-control, a fork

`nqh-packages/iphone-control` is Pinky with a dedicated iPhone (an iPhone SE, iOS 26.2) driven by agents, not a person. Upstream is `DIYgod/pinky` (remote `upstream`); keep the diff against it small. Decisions and designs: [docs/notes/iphone-control/DESIGN.md](docs/notes/iphone-control/DESIGN.md) (CLI and lease line) and [docs/notes/iphone-control/EXTRAS.md](docs/notes/iphone-control/EXTRAS.md) (unlock, credentials, approval gates). The upstream rules below about releases, locales and changelogs do not apply here: a fork push is not a release.

- **Build and sign:** `CGO_ENABLED=0 go build -o build/pinky ./cmd/pinky`, then `codesign --force --sign "Apple Development: <name> (<id>)" build/pinky` (a rebuilt unsigned binary loses access to its keychain item).
- **Daemon:** `PINKY_HOME=<dir> PINKY_TRUST_AGENTS=1 PINKY_AUTO_UNLOCK=1 PINKY_ALLOW_DEVICES=<udid> build/pinky daemon --http 127.0.0.1:7465`. `PINKY_ALLOW_DEVICES` is the only phone the gateway knows; every other phone on this Mac is invisible to it.
- **Install the phone helper once:** `build/pinky helper install --team <team id> <udid>` (signed with the paid team, valid a year). iOS asks for the phone's passcode on its screen now and then to allow automation; until someone enters it the phone reads `needs_setup`.
- **Agent surface:** `pinkyctl phone status | tools | call | run` (docs/mcp.md, "From a shell"). The lease queues first come first served (`wait_ms`, `-wait`), a releasing name keeps the phone for 15 s, `phone status` is one JSON line. Name your run with `IPHONE_LEASE_HOLDER`.
- **Secrets:** the phone's PIN (`pinkyctl phone pin set|check|delete`) and credentials (`pinkyctl phone secret put`, staged by the owner's credential broker, typed as `{{secret:NAME}}`) never touch a file, argument, log or transcript. No tool prints them. Never run the broker or read the keychain item to learn a value.
- **Gates:** only payments, erasing the iPhone, leaving the Apple Account and removing the passcode wait for a person (`internal/policy/presets.go`). Everything else is the agent's.
- **Seam guards:** `internal/forkseams` (declaration `seams.yaml`) limits who may call the PIN reader, the vault and the staged-secret hand-out. Widening a rule means editing the declaration in the same commit and saying why.
- **Prove it:** `.agents/skills/verify-iphone-control` in the `ipad-local` repo drives the real phone (status, two named holders, a gate). Targeted tests while working; `make check` once at the end (the gate).
- **Never:** call the phone, the daemon, `helper install` or the credential broker from a test; use `git add -A`; commit a device identifier, serial, MAC or passcode.

## README

The README comes in English, [README.md](README.md), and in Simplified Chinese, [README.zh-CN.md](README.zh-CN.md), which follows it section by section (building from source is only summed up there). Change both in the same commit. Write the Chinese with the app's own names for menus and buttons (`cmd/pinky/locales/zh-Hans.json`), and give a new picture with words in it a Chinese copy next to the English one (`assets/readme/*-zh.*`).

[README.ja.md](README.ja.md) is a community translation into Japanese. It may lag behind the English, and a commit that changes the README need not change it.

## The app's languages

The app speaks English, Simplified Chinese and Japanese. Every message it shows lives in `cmd/pinky/locales`: add or change the English in `en.json` (with its `@key` description) and, in the same commit, the Chinese in `zh-Hans.json` and the Japanese in `ja.json`; when the English of a message changes, recheck both translations. Then run `go tool mygo generate cmd/pinky`; `go test ./cmd/pinky/locales` fails when a catalog lacks a message, and `go tool mygo i18n check cmd/pinky` lists what is missing. Write the Japanese with the terms `ja.json` already uses (スマホ, パソコン, 「設定 › スマホ」) and with the names Apple and Android give their settings in Japanese. [docs/notes/client.md](docs/notes/client.md) ("i18n") has the details. Release notes stay English and Chinese only (below): Pinky in Japanese shows the English ones.

## Releases

Pinky releases often, several times a day when there is something to ship. Versions are the date in Asia/Shanghai and that day's count, `YYYY.MDD.N` (`2026.1107.2` is the second release of 7 November 2026), and are never written by hand. [docs/RELEASING.md](docs/RELEASING.md) explains the whole pipeline.

Whoever pushes to `main` decides whether the push is a release:

- **Release** when the push changes what people get: a fix, a feature, a change in behaviour or in what they see or read in the app, the companion app, or what the packages hold.
- **Don't release** when the push changes only docs, tests, CI, development tools, or code that changes nothing people get (a refactor without a change in behaviour). Such changes ride along with the next release.
- When unsure, release. Releases are cheap.

To release:

1. Write the notes in [CHANGELOG.md](CHANGELOG.md) under the `## Unreleased` heading at its top (add the heading when it is not there; notes of earlier pushes may already wait under it). Write short bullets in English for the people who use Pinky: what changed for them, not how. "Unlocking a phone from the computer now works when it asks for a fingerprint first", not "fix(android): handle the fingerprint prompt".
2. Write the same bullets in Simplified Chinese under `## Unreleased` at the top of [CHANGELOG.zh-CN.md](CHANGELOG.zh-CN.md), one for each English bullet and in the same order: Pinky in Chinese shows these in What's new and on the update card. Write natural Chinese for users, with the app's own names for menus and buttons from `cmd/pinky/locales/zh-Hans.json` in 「」 ("「设置 › 手机」"), and a space between Chinese and Latin letters or digits, as [README.zh-CN.md](README.zh-CN.md) does. Commit both files with the change.
3. On an up-to-date `main` with nothing else to commit, run

   ```sh
   scripts/release.sh tag -push -trailer "Co-Authored-By: <the line your commits end with>"
   ```

   It gives the notes in both files the next version, commits `chore(release): <version>`, tags it `v<version>` and pushes `main` and the tag together. It refuses when CHANGELOG.zh-CN.md has no notes under `## Unreleased` or another number of bullets than CHANGELOG.md. The tag starts the release workflow, which builds, signs, notarizes and publishes the release, and the Homebrew tap picks up its cask; build nothing yourself. `-n` shows what it would do.
4. If the push is refused because `origin/main` moved, the script undoes its commit and tag: `git pull --rebase origin main`, then run it again.

A push that is not a release leaves `## Unreleased` as it is, in both files. Never change a released section's heading, never move or reuse a tag, and never push a `v*` tag other than through `scripts/release.sh tag`.

Working on a branch that someone else merges? Leave CHANGELOG.md and CHANGELOG.zh-CN.md alone (parallel branches would conflict there) and end your report with the bullets you suggest for the notes, in English and in Chinese; whoever merges and pushes writes them.

## Fork seams

This fork keeps a few things behind a guard so a session that never read these notes cannot break them. `internal/forkseams/seams.yaml` declares each seam: the package, its symbols and the files that may use them. `internal/forkseams/forkseams_test.go` (run by `make check`) fails with `file:line: rule` and a `fix:` line for a use outside those files, for an allowed file that no longer exists, for a symbol declared twice or gone from its package. Its fixtures in `testdata/` prove each rule.

- The no-prompt passcode store (`secret.NewAgentPINs`, `NewAgentPINsOn`) is built only by `cmd/pinky/autounlock.go` (the daemon's `PINKY_AUTO_UNLOCK=1` wiring) and `cmd/pinky/phone_pin.go` (`pinkyctl phone pin set|check|delete`). Everything else reaches the passcode through `gateway.Options.PIN`. The two builder variables `autoUnlockPINs` and `phonePINs` are guarded the same way (`idents` in `seams.yaml`): only those two files name them.
- The credential vault is built and given its socket only by `cmd/pinky/secrets.go` (`vault.New`, `vault.Listen`), staged only by `cmd/pinky/phone_secret.go` (`vault.StageSecret`, the `pinkyctl phone secret put` the owner's credential broker runs), and a staged value leaves it only through `TakeSecret` in `internal/gateway/secrets.go`; `Deposit` is called only inside `internal/vault`. A seam may list `methods` instead of `symbols`, matched by name in any file not allowed. `TestSecretCanary` (`internal/gateway/secrets_test.go`) stages a sentinel, types it and fails if the result, history, export, log, an approval or an uncovered screenshot shows it; it must keep failing with `maskRead` stubbed out. A new place a typed value could be read back (a new read path, a new log line near `perform`) needs its row in that canary. The guard does not look inside folders named `build`, `vendor`, `testdata` or `node_modules` (`skippedDirs`); keep Go code out of them.
- To guard a new seam, add an entry to `seams.yaml`. To let one more file use a seam, add it to `allowed` in the same commit and say why. Never skip or delete the test.
