# Design: Agent CLI (with Code Mode) and Gateway Lease Queue for `iphone-control`

> **Scope:** Design only (no feature code written). Grounded in `/Users/nqh/Desktop/iphone-control` (`nqh-packages/iphone-control` fork of `DIYgod/pinky`, HEAD `55ff4825`) and reference semantics in `/Users/nqh/Desktop/ipad-local/.agents/skills/control-local-ipad/SKILL.md` and `/Users/nqh/Desktop/ipad-local/tools/curtain/`.

---

## 1. Problem & Phase A Grounding

Pinky (`DIYgod/pinky`) is designed around interactive MCP sessions where one human approves risky actions and fail-fast `lease_held` errors reject competing agents. For our dedicated agent-owned iPhone SE (iOS 26.2, headless `pinky daemon --http 127.0.0.1:7465` with `PINKY_HOME`, `PINKY_TRUST_AGENTS=1`, `PINKY_ALLOW_DEVICES=<udid>`), external agents need:

1. **A shell CLI with code mode** built directly on Pinky's existing `phone_*` MCP tools (no Python layer, no custom DSL, zero hand-synced tool lists).
2. **iPad-style lease arbitration in the gateway**: FIFO queue, `--wait` timeout, named holders, 15 s post-release same-name reservation, and a lease-free JSON status read—while keeping upstream merges trivial.

### Grounded Constraints & Traps Found in Existing Code

Every constraint below was verified in the repository:

1. **Gateway lock & lease ownership (`internal/gateway/gateway.go:143-176`, `internal/gateway/state.go:14-147`, `internal/gateway/record.go:289-371`):**
   - `Gateway.mu` guards `devices`, `sessions`, `d.lease`, `d.person`, `g.paused`, `d.removed`.
   - `device` already carries `wake chan struct{}` closed/replaced by `g.wakeLocked(d)` (`internal/gateway/gateway.go:1035-1038`) on state transitions.
   - `endLeaseLocked` (`internal/gateway/record.go:289-371`) is the single funnel for ending a lease (`Release`, `Session.end`, `Stop`, `StopAll`, `PauseDevice`, `RemoveDevice`, `expireOffline`, `expireIdle`, `Gateway.Close`).
   - `d.person` (person takeover via mirror `Touch`/`Key`/`Text` or `TakeOver`, `internal/gateway/client.go:477-530`) lives on the **device**, not the lease, and pauses the active lease (`TaskPaused`) without ending it; only `HandBack` clears it (`internal/gateway/client.go:534-575`). On `Connect` when `d.person == true`, upstream grants the lease in `StatusPaused` (`internal/gateway/agent.go:848-852,1057-1059`).
2. **Trap 1 — `CallBudget` (45 s) starts at the top of `Connect` (`internal/gateway/agent.go:584`, `internal/gateway/budget.go:28-33`):**
   - `Session.Connect` calls `ctx = g.withBudget(ctx)` immediately at line 584, attaching a 45 s deadline to `ctx`. If queue waiting (`--wait 60` or `--wait 120`) ran inside that budget, `firstScreen` would time out immediately after winning the lease. Queue waiting must run **outside** `CallBudget`, applying `withBudget` separately to `approveAgent` and to post-grant setup (`resolveTaskApps`, `taskConnection`, `firstScreen`).
3. **Trap 2 — Closed local session grace & active-lease stealing (`internal/gateway/resume.go:65-108,195-209`, `internal/gateway/agent.go:374,671-683`, `internal/mcp/server.go:267-272`):**
   - Every local socket MCP session gets `info.Reconnect = true` (`internal/mcp/server.go:271`).
   - When a `Reconnect` session closes without releasing (e.g., `SIGKILL` on a CLI run), `Session.Close()` (`internal/gateway/agent.go:374`) calls `g.keepLocked(l)`, keeping `d.lease` alive for `LocalResumeGrace` (60 s, `internal/gateway/gateway.go:117`) instead of ending it and advancing the queue.
   - Worse, `Session.continues` (`internal/gateway/resume.go:65-79`) lets a newer `Reconnect` session with the **same `agent.Name` and `task`** steal an open older session's lease via `adoptLocked` whenever `l.inflight == 0`—which would let two concurrent CLI runs sharing `IPHONE_LEASE_HOLDER=alice` steal each other's active lease instead of queuing!
4. **Trap 3 — Upstream test invariants that must stay green untouched:**
   - `internal/mcp/http_test.go:90` asserts `len(tools.Tools) == 27` and `internal/mcp/mcp_test.go:125` asserts the exact list of MCP tool names. Adding a new MCP tool would break upstream tests and create merge friction.
   - `internal/gateway/background_test.go:268-273` connects `"Watcher"` immediately (0 ms) after `"Claude Code"` calls `Release`, and `internal/gateway/onphone_test.go:135-136` + `internal/gateway/resume_test.go:72` assert `DeviceIdle` / `"Idle"` immediately after `Release`. Therefore:
     - `Options.ReserveGrace` must default to `0` in `gateway.Options` (and be set to `15s` in `cmd/pinky/daemon.go`), and
     - during a reservation (`d.lease == nil`), `DeviceView.Status` remains `DeviceIdle` and `StatusLine` remains `"Idle"`, while `DeviceView.Lease` carries `ReservedFor` / `ReservedUntil` / `Waiting`.
   - `internal/gateway/gateway_test.go:402-411` and `internal/gateway/resume_test.go:54` expect `Connect` with no wait to fail fast with `ErrLeaseHeld`. So `ConnectRequest.Wait == 0` must remain fail-fast.
5. **Existing local transport & CLI precedent (`internal/api/server.go:207-227`, `internal/api/control.go:173-174`, `cmd/pinky/task.go:237-272`, `internal/mcp/list.go:12-29`):**
   - The Unix socket (`$PINKY_HOME/run/pinky.sock`, mode `0600`, peer-UID checked at `internal/api/server.go:188-194`) serves both `RoleMCP` (newline-delimited JSON-RPC authenticated by `$PINKY_HOME/config/agent-token`, `remote=false` so saved screenshots return local paths per `internal/mcp/download.go:46` and names omit `" (remote)"` per `internal/mcp/server.go:265`) and `RoleControl` (`"devices"` returns `[]gateway.DeviceView` with no session or lease).
   - `cmd/pinky/task.go:237-272` already connects an in-process `sdk.Client` over `api.Dial` + `api.Greet(RoleMCP)` + `sdk.IOTransport`, setting `sdk.Implementation.Title` so `mcp.DisplayName` (`internal/mcp/server.go:292-322`) displays the exact caller name.
   - Every `phone_*` tool (`phone_observe`, `phone_apps`, `phone_device`, etc.) calls `s.acquire` / `s.acquireRead` (`internal/gateway/agent.go:419,1377,1526,1754`), which requires a lease; only `RoleControl` (`status`, `devices`) and `tools` are truly lease-free.
   - Fork diff today is just 2 small commits over `upstream/main` (`a61eec25`, `55ff4825`).

---

## 2. Usage (Caller's View)

Agents invoke one subcommand family under the existing binary: **`pinky phone`** (or `pinkyctl phone`). Environment: `PINKY_HOME` (socket/token dir, standard upstream), `IPHONE_LEASE_HOLDER` (default holder name), `IPHONE_DEVICE` (optional default device).

### Command Tree

| Command | Lease? | Reads / Writes / Changes | Output (stdout) |
|---|---|---|---|
| `pinky phone status [--device D] [--holder H]` | No | Reads `RoleControl` `"devices"`; never starts daemon | One JSON line |
| `pinky phone tools [--tool NAME] [--plain] [--offline]` | No | Reads MCP `tools/list` (falls back to `mcp.Tools` if `--offline` or daemon down) | JSON array (or object with `--tool`, or `name\ttitle` with `--plain`) |
| `pinky phone call [flags] <tool> [json \| @file \| -]` | Lazy (one-shot, auto-releases) | Opens MCP session, connects (waiting up to `--wait`), invokes `<tool>`, releases (triggers 15 s reservation) | One JSON `ToolResult` (or raw text with `--raw`) |
| `pinky phone run [flags] (<file.js> \| - \| -e '<code>')` | Lazy (lives for whole script, auto-releases) | Opens MCP session, runs sandboxed ES2020 JS (`goja`) with dynamically bound `phone.<verb>` functions, releases on exit/error/Ctrl-C | Only what `console.log(...)` prints; stderr gets `[code-mode: X -> Y (Z% reduction)]` |
| `pinky phone help` (`-h`, `--help`) | No | Prints usage + `agent-feedback` reporting line | Usage text (exit 0) |

### Shared Flags (`call` and `run`)

| Flag | Type | Default | Example / Notes |
|---|---|---|---|
| `--device` | string | `$IPHONE_DEVICE` or `""` | Single online/allowed phone auto-selected by gateway |
| `--holder` | string | `$IPHONE_LEASE_HOLDER` else `cli-<pid>` | Holder identity for queue, reservation, UI & history |
| `--wait` | int (s) | `60` | Max seconds to wait in FIFO queue (`0` = fail fast) |
| `--task` | string | `"cli: <tool>"` / `"cli: run"` | One-line task label shown in Pinky history |
| `--timeout` | duration | `120s` (`call`) / `300s` (`run`) | Overall command / script wall timeout |
| `--max-bytes` | int | `16384` (`call` only; `0` = unlimited) | Spill larger text payloads to temp file and return head + path |
| `--raw` | bool | `false` (`call` only) | Print Pinky's raw text block instead of JSON `ToolResult` |
| `--quiet`, `-q` | bool | `false` (`run` only) | Suppress `[code-mode: ...]` stats line on stderr |
| `--no-reserve` | bool | `false` | Skip the 15 s post-release reservation on exit |
| `-e` | string | `""` (`run` only) | Inline JS script |

### Exit Codes & Error Envelope

Diagnostics and structured errors go to **stderr** as one JSON line:
`{"error":{"code":"lease_held","message":"...","holder":"alice","tool":"phone_connect","line":0,"hint":"..."}}`

| Exit | Codes / Conditions | Caller action |
|---:|---|---|
| `0` | Success (`settled`, `-h`/`--help`) | Read stdout |
| `1` | Script JS error (includes `line`), unhandled `PhoneError`, `uncertain`, `approval_denied`, `approval_pending`, `stale`, `not_found`, `internal` | Fix script / observe before retrying |
| `2` | Invalid usage/flags/JSON, `invalid_argument`, `unsupported`, `scope_denied`, `blocked_by_policy` | Fix invocation; do not retry blindly |
| `3` | Daemon unreachable (socket missing/refused; never auto-starts daemon), `device_offline` | Check `PINKY_HOME` and daemon / USB cable |
| `4` | `lease_held` (wait timed out or `--wait 0`), `device_busy` | Retry later or raise `--wait` |
| `5` | `device_paused`, `device_needs_setup`, `device_locked`, `paused`, `user_took_over` | Needs person / handoff; check `pinky phone status` |
| `7` | `stopped` (person pressed Stop / Stop all) | End the task; do not reconnect |
| `130` | `SIGINT` / `SIGTERM` (Ctrl-C; lease cleanly released first) | Cancelled |

### Example Invocations

```bash
# 1. Lease-free status check (one JSON line)
pinky phone status
# {"device":"00008030-...","name":"iPhone SE","free":true,"holder":null,"task":null,"reserved_for":null,"reserved_left_s":0,"waiting":0,"link":"usb","state":"idle","health":"ok"}

# 2. Check whether phone is free for our own holder name (counts own 15 s reservation as free:true)
IPHONE_LEASE_HOLDER=alice pinky phone status | jq -e '.free'

# 3. Inspect live tool catalog (no hand-maintained docs needed)
pinky phone tools --plain
pinky phone tools --tool phone_swipe | jq '.inputSchema.properties'

# 4. One-shot tool call (lazy connect, waits up to 60s, releases into 15s reservation)
IPHONE_LEASE_HOLDER=alice pinky phone call phone_launch '{"app":"Settings"}'

# 5. One-shot call reading JSON args from stdin (pipe-friendly)
jq -n '{app:"com.apple.Preferences"}' | IPHONE_LEASE_HOLDER=alice pinky phone call launch -

# 6. Code mode: hold ONE lease across multiple steps, filter a 100-element screen down to 1 JSON line
IPHONE_LEASE_HOLDER=alice pinky phone run --wait 90 -e '
  phone.launch({ app: "Settings" });
  const s = phone.observe();
  const row = s.elements.find(e => e.label === "General");
  if (!row) throw new Error("General not visible");
  const after = phone.tap({ ref: row.ref, intent: "open General" });
  console.log(JSON.stringify({ status: after.status, items: after.elements.length }));
'
# stdout: {"status":"settled","items":14}
# stderr: [code-mode: 7.2KB -> 0.03KB (99.6% reduction)]

# 7. Code mode with explicit connect options (read_only / apps allow-list) and screenshot saved to disk
IPHONE_LEASE_HOLDER=alice pinky phone run -e '
  phone.connect({ task: "capture settings", apps: ["Settings"], read_only: true });
  phone.launch({ app: "Settings" });
  const r = phone.observe({ save: "settings.png" });
  console.log(JSON.stringify({ saved: r.saved }));
'

# 8. Code mode from file or stdin, releasing immediately without 15s reservation
IPHONE_LEASE_HOLDER=alice pinky phone run --no-reserve verify_flow.js
```

Help output includes the mandatory feedback line:
`Report friction: ~/.agents/skills/agent-feedback/scripts/agent-feedback submit --tool iphone --kind friction|bug|missing-capability|confusing-output|idea --goal ... --happened ... --fix ...`

---

## 3. Chosen Design (Shape, Types & Signatures)

### Module Map & Seams

```
  [External Agent / Shell]
             │
             ▼
  cmd/pinky/phone.go (35-line adapter; 2 lines in main.go, 3 lines in daemon.go)
             │
             ▼
  internal/phonecli/  ◄─── Deep Module 1: CLI + Code Mode (goja)
    • Seam (Dialer port): connects to Unix socket (prod) or in-memory server (tests)
    • Never imports internal/driver, internal/screen, internal/policy, ui
    • Zero hardcoded tool catalog: reads tools/list dynamically at runtime
             │
     ┌───────┴────────┐
     │ RoleControl    │ RoleMCP (newline JSON-RPC + token from $PINKY_HOME/config/agent-token)
     ▼                ▼
  internal/api/ (UNTOUCHED!) ──► internal/mcp/ (+3 lines server.go, +8 lines tools.go)
                                        │
                                        ▼
                                 internal/gateway/
                                   • queue.go ◄─── Deep Module 2: leaseQueue (FIFO + 15s reserve)
                                   • Guarded by existing Gateway.mu (no new locks)
                                   • Projected via DeviceView.Lease (read by RoleControl "devices")
```

### Part B: Gateway Lease Queue (`internal/gateway/queue.go`)

All queue and reservation state lives inside a single unexported struct `leaseQueue` embedded as `queue leaseQueue` on `device` (`internal/gateway/state.go:63`), guarded exclusively by `Gateway.mu`.

#### Exported Additions in `internal/gateway`

```go
// internal/gateway/api.go — added type + one field on DeviceView
type LeaseView struct {
	Holder        string    `json:"holder,omitempty"`
	Task          string    `json:"task,omitempty"`
	ReservedFor   string    `json:"reserved_for,omitempty"`
	ReservedUntil time.Time `json:"reserved_until,omitempty"`
	Waiting       int       `json:"waiting,omitempty"`
	Waiters       []string  `json:"waiters,omitempty"` // holder names in FIFO order
}

// On DeviceView (internal/gateway/api.go:71):
//   Lease LeaseView
```

```go
// internal/gateway/gateway.go — added field on Options (zero = disabled, preserving upstream tests)
//   ReserveGrace time.Duration // post-release same-name reservation; daemon sets 15s, 0 disables
```

```go
// internal/gateway/agent.go — added fields on existing request structs & limit
const MaxConnectWait = 10 * time.Minute // 600s max wait_s

// On ConnectRequest (internal/gateway/agent.go:183):
//   Holder string        // optional holder identity; defaults to Session.agent.Name
//   Wait   time.Duration // max FIFO queue wait; 0 = fail fast (upstream default)

// On ReleaseRequest (internal/gateway/agent.go:204):
//   NoReserve bool // skip post-release reservation and wake next FIFO waiter immediately
```

```go
// internal/gateway/state.go — added fields on existing unexported structs
// On device (state.go:63):
//   queue leaseQueue
// On lease (state.go:223):
//   holder    string
//   noReserve bool
// On Session (agent.go:327):
//   closedCh chan struct{} // closed under g.mu in s.end when s.closed becomes true
```

#### Deep Implementation in `internal/gateway/queue.go`

```go
package gateway

import (
	"context"
	"time"
)

type waiter struct {
	session  *Session
	holder   string
	notify   chan struct{} // buffered(1): O(1) wakeup without thundering herd
	enqueued time.Time
}

// leaseQueue owns one device's FIFO wait queue and post-release reservation.
// Every method requires Gateway.mu unless noted.
type leaseQueue struct {
	waiters       []*waiter
	reservedFor   string
	reservedUntil time.Time
	reserveGen    uint64
	reserveTimer  *time.Timer
}

// canClaimLocked reports whether holder (or waiter w, nil for immediate try)
// may claim an idle device right now (d.lease == nil).
// Rules:
//  1. Active reservation (now < reservedUntil): ONLY reservedFor may claim (jumps queue).
//  2. No active reservation: immediate caller may claim only if len(waiters) == 0;
//     queued waiter w may claim only if waiters[0] == w (strict FIFO).
func (q *leaseQueue) canClaimLocked(w *waiter, holder string, now time.Time) bool

func (q *leaseQueue) enqueueLocked(s *Session, holder string, now time.Time) *waiter
func (q *leaseQueue) removeLocked(w *waiter) bool
func (q *leaseQueue) positionLocked(w *waiter) int // 1-based; 0 if absent

// setReservationLocked starts a dur reservation for holder with a generation-guarded
// time.AfterFunc. Immediately wakes a same-name waiter already in the queue if present.
func (q *leaseQueue) setReservationLocked(g *Gateway, d *device, holder string, dur time.Duration)

// clearReservationLocked stops any pending timer and clears reservedFor/reservedUntil.
func (q *leaseQueue) clearReservationLocked()

// notifyNextLocked wakes the single waiter eligible to claim next:
// first waiter matching active reservedFor, else waiters[0].
func (q *leaseQueue) notifyNextLocked(now time.Time)

// notifyAllLocked wakes every waiter (used on abort: pause, remove, offline, StopAll, Close).
func (q *leaseQueue) notifyAllLocked()

// viewLocked projects the device's current lease, active reservation, and queue depth.
func (q *leaseQueue) viewLocked(d *device, now time.Time) LeaseView

// waitForLeaseLocked blocks (releasing and reacquiring g.mu) until s can claim d,
// req.Wait expires, ctx cancels, s closes, or d aborts (paused/removed/offline/stopped).
// Returns nil with g.mu HELD when s is cleared to call newLeaseLocked;
// returns non-nil *Result with g.mu UNLOCKED on failure/timeout/abort.
func (g *Gateway) waitForLeaseLocked(
	ctx context.Context, s *Session, d *device,
	title, holder string, wait time.Duration, progress Progress,
) *Result

// onLeaseEndedLocked runs inside endLeaseLocked (record.go:300) when d.lease == l.
// Starts the 15s reservation on clean release/disconnect when ReserveGrace > 0 and !l.noReserve;
// clears reservation and notifies waiters otherwise.
func (g *Gateway) onLeaseEndedLocked(d *device, l *lease, outcome string)

// abortAllQueuesLocked clears all reservations and wakes all waiters (StopAll / Close).
func (g *Gateway) abortAllQueuesLocked(markStopped bool)
```

#### Behavioral Contract for All Edge Cases

1. **`phone_connect` waiting & `CallBudget`:**
   - `approveAgent` runs with its own `g.withBudget(ctx)` (45 s).
   - `waitForLeaseLocked` runs **without** `CallBudget`, bounded by `req.Wait` and `ctx.Done()`, sending `progress(elapsed, wait, "Waiting in queue (position N, held by X)")` every 5 s.
   - Once granted, `newLeaseLocked` assigns `d.lease` under `g.mu`, clears any matching reservation, unlocks `g.mu`, and **then** applies `ctx = g.withBudget(ctx)` for `resolveTaskApps`, `taskConnection`, and `firstScreen`.
   - Same-session same-task reconnect (`agent.go:691`) bypasses the queue; same-session different-task (`runsOtherTask`, `shared.go:129`) still fails fast with `ErrLeaseHeld` (impossible to self-deadlock).
2. **`phone_release` & 15 s same-name reservation:**
   - Clean release (`done`, `failed`, `partial`, `cancelled`) or non-resuming disconnect (`OutcomeDisconnected` when `l.session.agent.Reconnect == false && l.session.agent.Resume == false`) with `g.opt.ReserveGrace > 0` and `!l.noReserve` sets `reservedFor = l.holder` for `ReserveGrace` (15 s).
   - If a waiter with `w.holder == l.holder` is already queued, `notifyNextLocked` wakes it immediately (no 15 s wait).
   - Otherwise, when the generation-guarded 15 s timer fires, it clears `reservedFor`, wakes `waiters[0]`, and emits `EventDevices`.
   - `phone_release { no_reserve: true }` skips reservation and wakes `waiters[0]` immediately.
   - `OutcomeStopped`, `OutcomePaused`, `OutcomeOffline`, `outcomeGatewayStopped` **never** create a reservation.
3. **Holder session dies mid-lease or mid-queue:**
   - **Mid-lease (CLI process killed with `SIGKILL`):** `internal/mcp/server.go:267-272` does **not** set `info.Reconnect = true` when `ci.Name == "pinky-phone"`. When the Unix socket hits EOF, `mcp.Server.forget` calls `gs.Close()`, which calls `endLeaseLocked(l, OutcomeDisconnected, "")` immediately (no 60 s `LocalResumeGrace` hang!), entering the 15 s same-name reservation (or advancing the queue if `ReserveGrace == 0`).
   - **Mid-lease (local non-CLI `Reconnect` session like Claude Desktop):** if another agent is waiting in `d.queue` when `d.lease.session.closed && !Resume && l.inflight == 0`, the existing eviction rule (`agent.go:679-683`) runs inside `waitForLeaseLocked` and `s.end`, so a dead local MCP session never blocks queued waiters for 60 s.
   - **Mid-queue (waiter Ctrl-C / timeout / socket closed):** `select` wakes on `<-ctx.Done()`, `<-s.closedCh`, or `<-deadline.C`. Waiter acquires `g.mu`, removes itself from `d.queue.waiters`, calls `d.queue.notifyNextLocked(time.Now())` (so if it was head-of-line or matching-reservation, the next waiter is woken immediately—**zero leaked turns**), emits `EventDevices`, and returns.
4. **Interaction with person takeover, pause, handoff, Stop:**
   - **Person takeover (`d.person == true`, mirror click or `TakeOver`):** active lease stays `d.lease` in `TaskPaused` (as upstream does); queued waiters remain queued until the active holder releases or their `--wait` expires. If `d.lease == nil && d.person == true`, the next waiter granted the lease starts in `StatusPaused` (matching upstream `agent.go:848-852,1057-1059`), and `pinky phone status` reports `"free": false, "state": "in_control", "health": "in_control"`.
   - **Handoff (`phone_handoff`):** runs inside the active lease (`d.lease == l`, `l.inflight > 0`); queued waiters wait normally until `l` releases or `--wait` expires.
   - **Pause connection (`PauseDevice`, `internal/gateway/pause.go:34`):** ends active lease with `OutcomePaused`, calls `d.queue.clearReservationLocked()` + `d.queue.notifyAllLocked()`; every waiter wakes and returns `ErrDevicePaused` immediately.
   - **Remove phone (`RemoveDevice`, `internal/gateway/remove.go:53`):** clears reservation and wakes all waiters -> `ErrDeviceOffline`.
   - **Stop (`Stop(deviceID)`):** ends active lease with `OutcomeStopped`, clears reservation, wakes next non-barred waiter (`waiters[0]`).
   - **Stop all (`StopAll()`):** ends all leases, marks all queued sessions `stopped = true`, clears all reservations, wakes all waiters -> every waiter returns `StatusStopped`.
5. **Status read (`pinky phone status`):**
   - Calls existing `RoleControl` `"devices"` RPC (`internal/api/control.go:173-174`)—zero new RPC methods, zero MCP session, zero lease.
   - `Gateway.Devices()` (`internal/gateway/client.go:39-70`) populates `v.Lease = d.queue.viewLocked(d, time.Now())` under `g.mu`.

### Part A: CLI & Code Mode (`internal/phonecli/`)

#### Seam & Port (`internal/phonecli/client.go`)

```go
package phonecli

import (
	"context"
	"io"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/gateway"
)

// CLIClientName is sent in MCP clientInfo.Name so internal/mcp skips
// LocalResumeGrace and active-lease adoption for CLI runs.
const CLIClientName = "pinky-phone"

// Dialer is the single external seam of internal/phonecli.
// Production adapter: UnixDialer (internal/api Unix socket).
// Test adapter: in-memory MCP + fake DevicesFunc.
type Dialer interface {
	Devices(ctx context.Context) ([]gateway.DeviceView, error)
	ConnectMCP(ctx context.Context, holder, version string) (*sdk.ClientSession, error)
}

type UnixDialer struct{ Dirs api.Dirs } // never auto-starts daemon; uses api.Dial + api.EnsureToken

type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type Env struct {
	Version       string
	DefaultHolder string // $IPHONE_LEASE_HOLDER
	DefaultDevice string // $IPHONE_DEVICE
	TempDir       string
	PID           int
}

// ExitError carries a process exit code and optional structured error (already printed if Printed).
type ExitError struct {
	Code    int
	Err     error
	Printed bool
}

// Run is the sole entrypoint called by cmd/pinky/phone.go.
func Run(ctx context.Context, args []string, dial Dialer, env Env, streams IO) error
```

#### Parsed Tool Result & Dynamic Code Mode (`internal/phonecli/parse.go`, `codemode.go`)

```go
type Element struct {
	Ref   int    `json:"ref"`             // 0 for ocr-only lines
	Role  string `json:"role"`            // "button", "switch", "ocr", ...
	Label string `json:"label"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	State string `json:"state,omitempty"` // "focused", "off", ...
}

type ToolResult struct {
	OK        bool              `json:"ok"`
	Status    string            `json:"status,omitempty"`
	Error     string            `json:"error,omitempty"`
	Device    string            `json:"device,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	Text      string            `json:"text"`
	Elements  []Element         `json:"elements,omitempty"` // parsed from [ref]/ocr lines (excludes '-' diff removals)
	Saved     []string          `json:"saved,omitempty"`    // paths from "saved: <path> · <size>"
	ImagePath string            `json:"image_path,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
	FullPath  string            `json:"full_path,omitempty"`
}

// StatusLineJSON is the one-line JSON printed by `pinky phone status`.
type StatusLineJSON struct {
	Device        string  `json:"device,omitempty"`
	Name          string  `json:"name,omitempty"`
	Free          bool    `json:"free"`
	Holder        *string `json:"holder"`
	Task          *string `json:"task"`
	ReservedFor   *string `json:"reserved_for"`
	ReservedLeftS int     `json:"reserved_left_s"`
	Waiting       int     `json:"waiting"`
	Link          *string `json:"link"`
	State         string  `json:"state"`
	Health        string  `json:"health"`
}

// leaseSession wraps one *sdk.ClientSession with lazy phone_connect and guaranteed defer phone_release.
type leaseSession struct {
	cs        *sdk.ClientSession
	dial      Dialer
	opts      runOptions
	tools     map[string]*sdk.Tool // populated dynamically from cs.ListTools
	connected bool
	released  bool
	rawBytes  int64 // total raw MCP response bytes received, for code-mode reduction metric
}

func (s *leaseSession) callTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error)
func (s *leaseSession) release(outcome, summary string)
```

**How `codemode.go` works (`github.com/dop251/goja`):**
- Pure Go ES2020 VM (`goja.New()`): **no filesystem, no network, no `process`/`env`, no `require`**.
- Wall/CPU timeout: `context.AfterFunc(scriptCtx, func() { vm.Interrupt("timeout") })` stops infinite loops cleanly.
- Dynamic binding from `cs.ListTools(ctx, nil)`:
  - For every tool returned by MCP, registers `phone[strings.TrimPrefix(t.Name, "phone_")] = func(argsObj) -> ToolResult` (so `phone.observe`, `phone.tap`, `phone.launch`, `phone.wait_for`, `phone.read_list`, `phone.connect`, `phone.release`, etc. all stay in 1:1 sync with upstream automatically).
  - Also exposes `phone.call(fullName, args)` (escape hatch if a future upstream tool lacks the `phone_` prefix), `phone.status()` (lease-free via `dial.Devices`), and `phone.sleep(ms)` (context-aware).
  - Tool errors (`isError: true` or transport error) throw a JS `PhoneError` with `.code`, `.status`, `.holder`, `.rule`, `.approval_id`, `.message`, `.text`.
  - `console.log` writes to `streams.Stdout` (counting emitted bytes); `console.error` writes to `streams.Stderr`.
  - When `Run` finishes, prints `[code-mode: X.XKB -> Y.YKB (ZZ.Z% reduction)]` to `streams.Stderr` (unless `--quiet`).
  - `defer s.release(outcome, "")` runs in Go with a fresh 5 s background context so even JS exceptions, timeouts, and `SIGINT` release the lease cleanly and trigger the 15 s reservation.

**How `call.go` keeps output small:**
- Rejects `phone_connect` / `phone_release` with exit 2 and a hint to use `pinky phone run`.
- Writes any `sdk.ImageContent` to `$TMPDIR/pinky-shot-<ts>.jpg` and returns `image_path` (never dumps base64).
- If `len(res.Text) > maxBytes` (default 16 KiB, `0` disables), writes full text to `$TMPDIR/pinky-out-<ts>.txt`, truncates `res.Text` to `maxBytes`, sets `res.Truncated = true`, `res.FullPath = path`, and appends an actionable hint to filter via `pinky phone run`.

### Part C: Shared-Seam Guard

Three-part guard running inside standard `make check` (`go test ./...`):

1. **Machine-readable contract declaration (`internal/phonecli/seam.json`, embedded via `//go:embed`):**
   - `allowed_internal_imports`: `["github.com/DIYgod/pinky/internal/api", "github.com/DIYgod/pinky/internal/gateway", "github.com/DIYgod/pinky/internal/mcp"]`
   - `forbidden_internal_imports`: `["github.com/DIYgod/pinky/internal/driver", "github.com/DIYgod/pinky/internal/policy", "github.com/DIYgod/pinky/internal/screen", "github.com/DIYgod/pinky/internal/history", "github.com/DIYgod/pinky/internal/approval", "github.com/DIYgod/pinky/ui"]`
   - `allowed_hardcoded_tools`: `["phone_connect", "phone_release"]` with `allow_files`: `["client.go", "call.go", "codemode.go"]`
   - `status_json_keys`: `["device", "name", "free", "holder", "task", "reserved_for", "reserved_left_s", "waiting", "link", "state", "health"]`
   - `exit_codes`: exact mapping from Pinky error/status strings to exit numbers (`0, 1, 2, 3, 4, 5, 7, 130`).
2. **Executable guards in `go test`:**
   - `internal/phonecli/seam_test.go`:
     - AST-parses every non-test `.go` file in `internal/phonecli/`: fails with `file:line: rule <id> — fix: <action>` on forbidden imports, hardcoded `phone_*` tool literals outside `allowed_hardcoded_tools`, or missing files in `allow_files` (prevents allow-list rot).
     - `TestCatalogSyncWithMCP`: spins up `mcp.New(gw, "test")` in-memory and asserts every tool in `mcp.Tools` is returned by `phone tools` and bound on `phone.<verb>` in `goja`.
     - `TestExitCodeAndStatusContract`: verifies every entry in `seam.json` matches runtime `exitCodeFor(...)` and marshaled `StatusLineJSON`.
   - `internal/gateway/queue_test.go`: tests all queue/reservation/takeover/pause/stop/disconnect invariants through the public `Gateway`/`Session` interface.
3. **Self-verifying fixtures (`internal/phonecli/seam_test.go`):**
   - Feeds synthetic Go source snippets containing a forbidden `internal/screen` import and a hardcoded `"phone_tap"` literal into the same AST checker and asserts both fail with expected rule + `fix:` line.

### Part D: Complete File List (Minimal Upstream Diff)

#### Modified upstream files (13 files, small localized edits)

| File | Exact change |
|---|---|
| `go.mod`, `go.sum` | Add `github.com/dop251/goja` (pure Go, zero CGO) |
| `internal/gateway/api.go` | Add `LeaseView` struct; add `Lease LeaseView` field to `DeviceView` (`:71`) |
| `internal/gateway/state.go` | Add `queue leaseQueue` to `device` (`:63`); add `holder string`, `noReserve bool` to `lease` (`:223`) |
| `internal/gateway/gateway.go` | Add `ReserveGrace time.Duration` to `Options` (`:30`); call `abortAllQueuesLocked(false)` in `Close()` (`:308`) |
| `internal/gateway/agent.go` | Add `Holder`, `Wait` to `ConnectRequest` (`:183`), `NoReserve` to `ReleaseRequest` (`:204`), `closedCh` to `Session` (`:327,353,386`), split `withBudget` around `waitForLeaseLocked` in `Connect` (`:584-725`), pass `l.noReserve` in `Release` (`:1340`) |
| `internal/gateway/record.go` | Call `g.onLeaseEndedLocked(d, l, outcome)` in `endLeaseLocked` (`:310`) |
| `internal/gateway/client.go` | Populate `v.Lease` in `Devices()` (`:66`); call `g.abortAllQueuesLocked(true)` in `StopAll()` (`:601`) |
| `internal/gateway/pause.go` | Call `d.queue.clearReservationLocked()` + `d.queue.notifyAllLocked()` in `PauseDevice` (`:51`) |
| `internal/gateway/remove.go` | Call `d.queue.clearReservationLocked()` + `d.queue.notifyAllLocked()` in `RemoveDevice` (`:64`) |
| `internal/mcp/tools.go` | Add optional `holder`, `wait_s` (`0..600`) to `connectIn` (`:71`) and `no_reserve` to `releaseIn` (`:82`) |
| `internal/mcp/server.go` | Skip `info.Reconnect = true` when `ci.Name == "pinky-phone"` in `agentLocked` (`:267-272`) |
| `cmd/pinky/daemon.go` | Add `--reserve-grace` (default `15s` in `runDaemon`) wired to `gateway.Options.ReserveGrace` |
| `cmd/pinky/main.go` | Add `case "phone": return runPhone(args)` (`:106`) and 2 lines in `usage()` (`:166`) |

#### New files added (zero upstream merge conflict surface)

- `internal/gateway/queue.go`, `internal/gateway/queue_test.go`
- `internal/phonecli/{cli.go,client.go,status.go,tools.go,call.go,codemode.go,parse.go,errors.go,seam.json,seam_test.go,cli_test.go}`
- `cmd/pinky/phone.go`

#### Explicitly left untouched

- `internal/api/*` (zero changes: existing `RoleMCP` and `RoleControl` `"devices"` suffice)
- `internal/mcp/{render.go,http.go,tasks.go,upload.go,download.go,list.go,sessionless.go,*_test.go}`
- `internal/gateway/*_test.go` (all existing tests remain green without edits)
- `internal/driver/*`, `internal/policy/*`, `internal/screen/*`, `internal/history/*`, `internal/approval/*`, `ui/*`, `cmd/pinky/locales/*`

---

## 4. Rejected Candidates & Why They Lost

### Candidate 2A (Rejected): Shell-Hosted Session Proxy (`pinky phone exec -- sh -c '...'` + `PINKY_SESSION_SOCK`)
- **Shape:** Zero new `go.mod` dependencies. `exec` holds an MCP session and opens an ephemeral Unix socket proxy; child shell commands invoke `pinky phone call` which forwards over `PINKY_SESSION_SOCK`.
- **Why it lost (interface depth & red flags):**
  - *Not sandboxed* (`tool-design` requires no fs/net access in code mode by default).
  - *Shallow interface / two ways to do one task*: introduces an ambient env-var IPC protocol, spawns a subprocess per phone step, forces JSON quoting inside shell strings, and pushes screen-text parsing onto every caller (`awk`/`jq`).

### Candidate 2B (Rejected): Static NDJSON Pipeline (`pinky phone run` reading JSONL tool calls on stdin)
- **Shape:** Zero new dependencies; streams `{"tool":"...","args":{...}}` lines over one MCP session.
- **Why it lost:** Cannot branch, loop, or extract `[ref]` from `phone_observe` output to feed `phone_tap`. Pinky already has `phone_batch` for static sequences; this solves neither interactive control flow nor context reduction.

### Candidate 3 (Rejected): Daemon-Side Code Mode (28th MCP tool `phone_eval` in `internal/mcp/tools.go`)
- **Shape:** Embed `goja` inside `internal/mcp` and expose `phone_eval` as an MCP tool.
- **Why it lost:**
  - *Breaks upstream tests immediately:* `internal/mcp/http_test.go:90` asserts `len(tools.Tools) == 27` and `internal/mcp/mcp_test.go:125` enumerates every tool name.
  - *Blast radius:* a runaway JS allocation or loop inside the long-lived daemon can OOM the daemon and kill all other agents' sessions and the WDA tunnel.
  - *MCP call-timeout ceiling:* a multi-step script running inside one MCP tool call hits client-side 60 s tool-call timeouts.
  - *Bigger upstream diff* in `internal/mcp/`.

### Rejected Gateway Queue Shapes
- **Mac-side file lock (`flock`) outside `Gateway`:** *Split ownership*—MCP HTTP clients and Pinky UI would bypass the lock, and `Devices()` wouldn't reflect waiters or reservations.
- **Separate `queueMu` mutex on `device`:** *Lock-ordering hazard* with `Gateway.mu` during `endLeaseLocked` / `PauseDevice` / `StopAll`; reusing `Gateway.mu` matches every other device field (`internal/gateway/state.go:14-147`).

---

## 5. Risks & Open Questions

1. **Text parsing coupling (`internal/phonecli/parse.go`):** Parsing `Element` rows from `Screen.Render()` (`internal/screen/screen.go:299`) and `StatusLine` (`internal/mcp/render.go:33`) assumes upstream keeps the `[ref] role "label" @x,y` format (documented as public contract in `docs/mcp.md:125-139`). *Mitigation:* `res.Text` is always preserved verbatim so scripts can fall back to text regexes, and `TestCatalogSyncWithMCP` + `cli_test.go` catch any upstream render format drift immediately.
2. **Unproven (read-only task):** `goja` binary size delta and compile time on Go 1.27.3 (`go.mod:3`) were not measured because repo edits and builds were out of scope; `goja` is pure Go with zero CGO and widely deployed in Go CLIs (Caddy, k6).
3. **Open question — `go.mod` purity vs `goja`:** Is adding `github.com/dop251/goja` to `go.mod` acceptable in the fork (only 1 line in `go.mod`, cleanly isolated to `internal/phonecli/codemode.go`), or does the owner strictly want zero new dependencies?
4. **Open question — `phone status` when multiple phones exist:** Currently designed to auto-select when 1 phone is allowed/connected (`PINKY_ALLOW_DEVICES=<udid>`) and require `--device` (or return exit 2 listing them) when >1 exist. Should `--all` return a JSON array when multiple phones exist?
5. **Open question — HTTP transport flag:** Designed for Unix socket first (`internal/api`) behind the `Dialer` port. Should `--url` / `PINKY_HTTP_URL` ship in v1 or only when a remote machine needs the CLI?

---

## 6. Numbered Build Order & Pass/Fail Verification

1. **Step 1 — Gateway `leaseQueue` & `DeviceView.Lease`** (`internal/gateway/{queue.go,api.go,state.go,gateway.go,agent.go,record.go,client.go,pause.go,remove.go,queue_test.go}`)
   - **Pass/fail test:** `go test -race ./internal/gateway/...` passes all existing upstream tests unchanged AND new `TestLeaseQueue*` suite proves: FIFO ordering, 15 s same-name reservation jump & expiry, `wait==0` fail-fast, waiter cancel/close mid-queue (no leaked turn), `NoReserve`, `TakeOver`/`HandBack`, `PauseDevice`, `Stop`, `StopAll`, `Gateway.Close` timer cleanup, and `Devices().Lease` projection.
2. **Step 2 — MCP `phone_connect` / `phone_release` parameters & CLI session non-reconnect** (`internal/mcp/{tools.go,server.go}` + `cmd/pinky/daemon.go`)
   - **Pass/fail test:** `go test -race ./internal/mcp/...` passes existing `http_test.go` (`len(tools.Tools) == 27`) and `mcp_test.go` unchanged, PLUS a new test proving `phone_connect { wait_s, holder }` queues over MCP and a `"pinky-phone"` client disconnect ends its lease immediately into reservation instead of holding `LocalResumeGrace`.
3. **Step 3 — `internal/phonecli` core (`status`, `tools`, `call`, `parse`, `errors`)** (no `goja` yet)
   - **Pass/fail test:** `go test -race ./internal/phonecli/...` against a real `api.Server` + `gateway.Gateway` + `demo.Device` verifies: `status` JSON shape & `free` under own vs other reservation, `tools` dynamic catalog match, `call` lazy connect + auto-release + large-output temp-file spill + image-to-file + exact exit codes (`0/2/3/4/5/7`).
4. **Step 4 — `internal/phonecli` code mode (`codemode.go` with `goja`) & `cmd/pinky/phone.go`** (`go.mod`, `cmd/pinky/{main.go,phone.go}`)
   - **Pass/fail test:** `go test -race ./internal/phonecli/... ./cmd/pinky/...` proves: multi-step JS script holds ONE lease across `launch` -> `observe` -> `tap` -> `console.log`, reports `[code-mode: ... reduction]` on stderr, enforces timeout on `while(true){}`, blocks fs/net access, releases lease on JS exception & `SIGINT` (exit 130), and `pinky phone -h` exits 0 with the `agent-feedback` line.
5. **Step 5 — Shared-seam guard & full repo check** (`internal/phonecli/{seam.json,seam_test.go}`)
   - **Pass/fail test:** `make check` (`build`, `vet`, `test`) passes green, AND `go test -v -run TestSeam ./internal/phonecli` proves both live compliance and positive-fixture failure on forbidden imports and hardcoded tool names.
