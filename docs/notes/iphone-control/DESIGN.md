# iphone-control: the design to build

Synthesis of two independent designs, `design-opus.md` and `design-gemini.md` (same directory), run through the architect and
codebase-design gate on 2026-11-24. Where the two differ, this file decides. The detail (signatures, line cites, tests) is in
`design-opus.md`, which is the base.

## Decisions

1. **Phone and host.** One dedicated iPhone SE on iOS 26.2, driven from this Mac through Pinky's iOS path (WebDriverAgent host app on the phone).
   Pinky touches only the phones named in `PINKY_ALLOW_DEVICES`: the providers do not look at any other (`cmd/pinky/providers.go`) and the gateway does not list it. Until 2026-11-27 only the gateway filtered, so the iOS provider still prepared the other phones, developer disk images included. `PINKY_TRUST_AGENTS=1` skips the New agent approval (`cmd/pinky/daemon.go`).
2. **Only external agents drive it.** The built-in runner answers `unsupported` in `pinky daemon`; nothing to change.
3. **The lease line lives in the gateway** (`internal/gateway/queue.go`), not in the CLI, so MCP clients and the CLI share one line.
   - FIFO tickets per device, entered with `phone_connect wait_ms` (at most 600000), kept alive by the holder's repeated calls plus 5 s.
   - Every call still answers inside the 45 s call budget (`budget.go`); waiting never runs longer in one call.
   - A 15 s reservation for the releasing name, set only by `phone_release` (`Options.LeaseReserve`: 0 upstream, 15 s in the fork's daemon).
   - Rule 0: a waiting caller ends a dead local session's kept lease before the other-task check, so a crashed same-name run never blocks the next for 60 s.
   - Person takeover holds the line; pause answers `device_paused` to waiters; stop and crash never reserve.
4. **CLI: `pinkyctl phone status | tools | call | run`** (`cmd/pinky/phone.go`), generic over the live `tools/list`. No hand-kept tool list.
   - Transport: the daemon's unix socket (MCP role for tools, control role for `status`). It never starts a daemon (exit 3).
   - **Code mode is shell** (`phone run` holds one MCP session and one lease; `phone call` inside it goes through the run's session over a per-run socket). Embedded JavaScript (Gemini's goja) was rejected: new dependency, second script dialect, and tool results are text anyway. It stays an option if scripts outgrow shell.
   - Output stays small: tool text unchanged, images to files, a `[code-mode: X -> Y]` line on stderr.
   - Exit codes: 0 ok, 1 tool error, 2 usage, 3 no gateway, 4 busy (`lease_held` after `--wait`), 5 the person has the phone or must act.
   - Holder name: `-as`, else `IPHONE_LEASE_HOLDER`, else `Pinky CLI`. Wait: `-wait`, default 60 s. The reserve left for the first screen read after a queued connect is 15 s, not 10 s.
5. **Status line** (`phone status`): `free, device, holder, task, reserved_for, reserved_left_s, waiting, link, health`, pinned by a golden file.
6. **Guards.** `queue_test.go` (about 11 behaviour tests, including that upstream's `lease_held` text is unchanged when nothing waits) and `phone_test.go`
   (golden status keys, a tool added to the server is forwarded with no CLI change, exit codes). Every failure ends with a `fix:` line.
7. **Diff against upstream:** about 23 lines across 9 upstream files, plus new files. No new dependency, no new MCP tool, `internal/api` untouched.

## Build order

1 types and option (no behaviour) · 2 turn check and reservation · 3 waiting, tickets, rule 0 · 4 status view · 5 MCP `wait_ms` ·
6 CLI status and tools · 7 CLI call and exit codes · 8 CLI run with broker · 9 fork wiring and docs, then `make check` once · 10 device check on the SE.
Then the v1 extras, each in its own slice: autonomous unlock, credential typing, notification reading. Then the Q5 approval gates.
