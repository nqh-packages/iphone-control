# iphone-control: agent CLI with code mode, and a lease queue with named holders

Design by `claude-opus-5-5` (one runner of the architect pair). Read only. No feature code written.
Repo: `/Users/nqh/Desktop/iphone-control` at `55ff4825` (branch `main`). All `file:line` cites are from that tree.

Gate: the `architect` skill (Phase A grounding, Phase B with three structurally distinct candidates,
red-flag screen), then `codebase-design` on the result (section 7). I did not run `arena` in this worker:
I am one of the orchestrator's two runners, and the orchestrator's synthesis of both designs is the
arena step.

---

## 1. Problem

Agents need to drive the dedicated iPhone SE from a shell the way they drive the iPad: `status` and `run`.
They need FIFO waiting for the phone, a `--wait` timeout, named holders, a 15 s reservation for the same
name after a run ends, and a one-line JSON status. Pinky already has every verb (20 `phone_*` MCP tools)
and a lease. What it lacks is a shell surface with code mode, and a lease that waits instead of failing.

Constraints from Phase A:

- **One MCP session is one gateway session, and that session owns the lease.** `Server.session` opens a
  gateway `Session` on the first tool call and closes it when the MCP session ends
  (`internal/mcp/server.go:225-243`, `forget` at `:208-223`). A lease lives in `Session.leases`
  (`internal/gateway/agent.go:327-347`). So a lease that lasts for a whole script needs one MCP session for
  the whole script.
- **Reads need a lease too.** `phone_observe` goes through `acquireRead` (`agent.go:413-417`), and without a
  lease it returns "No phone is connected in this session; call phone_connect first."
  (`agent.go:472`). This differs from the iPad, where read-only calls need no lease.
- **Today's lease rule is fail-fast.** `connect` returns `lease_held` at `agent.go:684-689`. `Connect` holds
  `g.mu` without a break from `agent.go:600` to lease creation at `agent.go:720`; every `Unlock` before
  `720` is an early return (`:602 :609 :639 :646 :651 :655 :659 :664 :668 :676 :686 :703`). So a check put
  inside that critical section, followed by lease creation, is atomic.
- **The lease ends in one place.** `endLeaseLocked` (`internal/gateway/record.go:289`) clears `d.lease`
  (`record.go:302`) and closes the device's wake channel (`record.go:351`, `wakeLocked` at
  `internal/gateway/gateway.go:1035-1038`). The device struct calls that channel "closed and replaced on
  every state change, to wake waiting calls".
- **`phone_release` ends its leases** with the outcome it was given, at `agent.go:1340`. It accepts
  `done`, `failed`, `partial` and `cancelled` (`agent.go:1244-1253`).
- **Local sessions resume narrowly.** A local agent is `Reconnect` (`server.go:271`). A newer session of the
  same agent name continues an older session's lease when the older one is closed, *or* when its
  `phone_connect` names the same task, even while the older session is still open (`resume.go:65-79`;
  adoption at `agent.go:671`). A closed local session's kept lease does not keep the phone from anyone
  else: the next connect ends it (`agent.go:679-683`).
- **Every call answers within 45 s.** `CallBudget` and `MaxBlock` default to 45 s
  (`gateway.go:41-42, 111-112`). The budget is a context value that a nested call keeps
  (`budget.go:27-33`). `budgetBefore(ctx, t, reserve)` is the existing helper for waits that must leave
  time (`budget.go:59-66`), with `approvalReserve = 10 s` as precedent (`budget.go:77`). Waiting 60 s inside
  one call is not allowed.
- **Person control belongs to the device, not the lease.** `takeOver` sets `d.person`. Only `HandBack`
  clears it, and it holds across leases (`internal/gateway/client.go:473-530`, `:534-575`). A connect while
  the person is in control starts the lease paused (docs/mcp.md, phone_connect). Pause connection ends the
  task with `OutcomePaused` (`internal/gateway/pause.go:29-33`). Stop ends it with `OutcomeStopped`
  (`client.go:579-590`).
- **Precedent for a shell MCP client already exists.** `pinkyctl task` is an MCP client over the unix
  socket's MCP role, named by `-as` through `clientInfo.Title` (`cmd/pinky/task.go:116-185`, `dialTask`
  `:237-260`, `taskClient` `:266-272`). The control role needs no token, only the same uid
  (`internal/api/server.go:188-193, 220-224`). Its `devices` method returns `Gateway.Devices()`
  (`internal/api/control.go:173`, `client.go:39-72`). `pinkyctl devices` already reads it that way
  (`cmd/pinky/devices.go:36-40`).
- **The agent's name is its `clientInfo` title.** `DisplayName` uses a known client name, else the title,
  else the name (`server.go:292-323`). `task.go` sets `Title: name` (`task.go:267`). A holder name therefore
  needs no new concept: **holder = the gateway agent name.**
- **Fork precedent is small.** The two fork commits touch `cmd/pinky/daemon.go` and `gateway.go` only, with
  52 lines added (`git log --stat -2`). `TrustNewAgents` and `AllowDevices` are set at
  `cmd/pinky/daemon.go:213-215`.

## 2. Usage (caller's view, written first)

Quickstart an agent reads (this would live in the vendored skill `control-local-iphone`, ipad-local side):

```sh
phone status                      # {"free":true,"device":"iPhone SE","holder":null,"task":null,"reserved_for":null,"waiting":0,"link":"usb","health":"idle"}
phone tools                       # one line per tool: phone_tap · Tap · <first sentence of its description>
phone tools --json phone_swipe    # the full MCP definition (description and input schema) of one tool
phone call observe                # one call; the lease is taken and released around it
phone call tap ref=12             # key=value args; a value that parses as JSON is JSON, otherwise a string
phone call launch '{"app":"Settings"}'
PINKY_LEASE_HOLDER=ui-audit phone run --wait 120 script.sh    # code mode: one session and one lease for the whole script
phone run -e 'phone call launch app=Settings >/dev/null; phone call observe | grep -c button'
```

`phone` is `pinkyctl phone`. The ipad-local vendoring adds a two-line wrapper, so `cmd/pinky` gains no
argv[0] trick.

A script is any executable or shell text. Inside `phone run`, every `phone call` goes through the run's
one session:

```sh
#!/bin/sh
set -e
phone call launch app=Settings > /dev/null           # the first phone call takes the lease (queues up to --wait)
out=$(phone call tap ref="$(phone call observe | sed -n 's/^\[\([0-9]*\)\] button "General".*/\1/p')")
printf '%s\n' "$out" | head -1                       # only what the script prints enters the agent's context
# at exit: phone_release {outcome: done|failed by the exit status}; reserved for "ui-audit" for 15 s
```

Exit codes (one table for `call` and for the CLI's own failures; `run` returns the script's status):

| Code | Meaning | Caller does |
|---|---|---|
| 0 | the tool answered with a status (`settled`, `approval_pending`, `stale`, …). Read the first line | go on |
| 1 | the tool answered `error: <code>` (other than below) | fix the call; the message says how |
| 2 | usage error | fix the command line |
| 3 | no gateway on the socket (`PINKY_HOME`) | start `pinky daemon --http 127.0.0.1:7465` |
| 4 | `BUSY`: `lease_held` after `--wait` ran out | retry later; `phone status` says who holds it |
| 5 | the person has the phone or must act: `user_took_over`, `paused`, `stopped`, `device_locked`, `device_needs_setup`, `error: device_paused` | stop and tell the person, or `phone call handoff reason=...` |

Mapping uses `gateway.Status*` and `gateway.Err*` constants (`agent.go:60-99`), never string literals.
`cmd/pinky` already imports `internal/gateway` (`devices.go:19`).

Output rules (tool-design: code mode, large outputs):

- stdout gets the tool's text block, unchanged. Results are already compact text with diffs after
  actions (docs/mcp.md "Results").
- An image block (`phone_observe screenshot=true`) is never printed as base64. It is written to
  `$TMPDIR/pinky-phone/<run>/<n>.jpg`, and stdout gets the line `image: <path> · <bytes>`.
- At the end of `run`, stderr gets one line: `[code-mode: 41.2KB tool output -> 0.3KB printed (99.3%)]`.
  It is counted by the broker from the bytes it forwarded and the bytes the script wrote to stdout.
- Errors: the gateway's messages are already actionable (`lease_held` names the holder, `invalid_argument`
  names the bad field). The CLI adds only its own errors, each with a fix line, for example
  `phone: no gateway at <socket>. fix: pinky daemon --http 127.0.0.1:7465 (same PINKY_HOME)`.

## 3. Shape (chosen design)

### 3.1 Data first: the line and the reservation (gateway)

New file `internal/gateway/queue.go`. One owner for everything about waiting for a phone.

```go
// The line for a phone's lease, and who it is kept for after a run ends.
// Guarded by Gateway.mu. Lives on the device (state.go: device.line).
type line struct {
	tickets  []*ticket // first come first
	reserved string    // agent name the phone is kept for after its phone_release
	until    time.Time // when that reservation ends
}

// ticket is one session waiting for the phone. It stays alive only while its
// session keeps asking: during a phone_connect call that waits, and for
// ticketGrace after that call returns, so the session can call again (each call
// must answer within the call budget) without losing its place.
type ticket struct {
	session  *Session
	deadline time.Time // from the first call's wait_ms, capped at maxWait
	waiting  bool      // a phone_connect of the session is waiting now
	lastCall time.Time // when that session's last waiting call returned
}

const (
	ticketGrace = 5 * time.Second  // an abandoned ticket stops holding the line after this
	maxWait     = 10 * time.Minute // wait_ms above this is cut to it
)

// LeaseView is who has the phone, who it is kept for, and who waits. Read by the
// client API in DeviceView.Lease.
type LeaseView struct {
	Holder        string    `json:",omitempty"` // agent name of the session holding the lease
	Task          string    `json:",omitempty"` // that lease's task title
	ReservedFor   string    `json:",omitempty"` // the phone is kept for this agent name until ReservedUntil
	ReservedUntil time.Time `json:",omitzero"`
	Waiting       []string  `json:",omitempty"` // agent names in line, first first (live tickets only)
}
```

Gateway option (upstream default unchanged):

```go
// gateway.go, Options
// LeaseReserve keeps a phone for the agent that released it, by name, so
// that agent's next phone_connect comes first: others get lease_held, or
// wait. Zero (the default) keeps none.
LeaseReserve time.Duration
```

`ConnectRequest` (`agent.go:183-202`) gains one field:

```go
// Wait queues for the phone, first come first served, instead of returning
// lease_held at once: the call waits while it may, then returns lease_held
// with the session's place in line; calling again within ticketGrace keeps it.
Wait time.Duration
```

### 3.2 Signatures (gateway)

```go
// queue.go

// Connect is phone_connect. With req.Wait it queues for the phone (this
// file); without, it is connect: lease_held at once, as before.
func (s *Session) Connect(ctx context.Context, req ConnectRequest) *Result {
	if req.Wait <= 0 {
		return s.connect(ctx, req)
	}
	return s.g.waitTurn(ctx, s, req)
}

// waitTurn calls connect until it returns anything but a queued lease_held,
// waking on the device's state changes (d.wake), a short tick (Options.PollInterval)
// for reservation and ticket expiry, ctx, or the end of the call's budget less
// approvalReserve. That leaves time for connect's first read of the screen.
// It returns the last lease_held, whose message gives the place in line.
// It tells a queued lease_held from every other one (shared.go's other-task
// refusal, busyElsewhereLocked) by Result.queued, which only turnLocked sets.
// It never parses the message.
func (g *Gateway) waitTurn(ctx context.Context, s *Session, req ConnectRequest) *Result

// turnLocked decides, inside connect's critical section, right after
// adoption (agent.go:671) and before the other-task check (agent.go:672),
// whether s may take d now. nil: go on (connect then creates the lease
// without unlocking, so the decision cannot go stale). A result: lease_held
// with Holder set, the place in line in Message, and queued set. With
// wait > 0 it adds or refreshes s's ticket. With wait == 0, no tickets and no
// reservation, it returns nil, and upstream's lease_held texts stand. The
// caller holds g.mu.
func (g *Gateway) turnLocked(s *Session, d *device, wait time.Duration) *Result

// reserveLocked keeps d for the agent that just released it, for
// Options.LeaseReserve. Called only from Release, after endLeaseLocked:
// only an agent's own "run ended" reserves. Stop, pause, idle expiry,
// offline and a dropped session never reserve. The caller holds g.mu.
func (g *Gateway) reserveLocked(d *device, agent string)

// leaseViewLocked is d's LeaseView, with dead tickets left out. The caller holds g.mu.
func (g *Gateway) leaseViewLocked(d *device) LeaseView
```

`Result` (`agent.go:101-140`) gains one unexported field, `queued bool`. That follows its existing
unexported `outOfTime` and `unread` fields (`agent.go:133-139`), so nothing renders or exports it.

`turnLocked` rule, in order:

0. With `wait > 0`, a lease kept for a closed local session that is not s's
   (`l.session.closed && !l.session.agent.Resume && l.inflight == 0`) is ended as disconnected. This is
   the same rule as `agent.go:679-683`, applied earlier. Without it, a crashed run of the **same holder
   name** with another task title blocks the next run for the whole 60 s grace:
   `runsOtherTask` (`shared.go:129-131`) calls `continues(l.session, l, "")`. For a local agent that is
   true when the older session is closed (`resume.go:75-76`). So `agent.go:672-676` answers
   `otherTaskResultLocked`'s `lease_held` and marks the session shared (`shared.go:136-149`). That is
   exactly the case the CLI's unique-per-run task title creates (R1). With `wait == 0` upstream's
   behaviour is untouched.
1. `d.lease != nil && d.lease.session == s`: nil. The session's own lease, and upstream's same-task and
   other-task handling for it, stay as they are (`agent.go:672-676, 690-719`).
2. Prune tickets whose session is closed, whose deadline passed, or that are not `waiting` and were last
   called more than `ticketGrace` ago. A reservation past `until` is dropped.
3. `mine` = (`reserved == s.agent.Name`) or (no reservation and (no tickets or the first ticket is s's)).
4. `d.lease == nil && mine`: remove s's ticket and the reservation (it is used up), then return nil.
5. Otherwise, with `wait == 0`: if `d.lease != nil` and there are no tickets and no reservation, return
   nil (upstream `lease_held` at `:684-689` answers). Else return `lease_held` "kept for X for N s" or
   "N waiting".
6. Otherwise (`wait > 0`): add or refresh s's ticket (deadline = max(old, now+min(wait, maxWait)),
   `waiting = true`). Return `lease_held`, `Holder` = holder or reserved name, message "In line for
   <phone>: 2nd, behind worker-a. Call phone_connect again with wait_ms to keep your place."

`waitTurn` sets `waiting = false, lastCall = now` on its ticket when it returns.

### 3.3 Upstream edits (gateway), each a line or two

| File | Edit |
|---|---|
| `internal/gateway/agent.go:582` | rename `func (s *Session) Connect` to `connect` (the new `Connect` is in queue.go) |
| `internal/gateway/agent.go` after `:671`, before `:672` | `if res := g.turnLocked(s, d, req.Wait); res != nil { g.mu.Unlock(); return res }` |
| `internal/gateway/agent.go:133-139` | `queued bool` unexported on `Result` |
| `internal/gateway/agent.go:183-202` | `Wait time.Duration` in `ConnectRequest` |
| `internal/gateway/agent.go` after `:1340` | `g.reserveLocked(l.dev, s.agent.Name)` |
| `internal/gateway/state.go` device struct (`:60-147`) | `line line // the queue for its lease (queue.go)` |
| `internal/gateway/gateway.go:30-91` | `LeaseReserve time.Duration` with its comment |
| `internal/gateway/api.go:71-100` | `Lease LeaseView` in `DeviceView` |
| `internal/gateway/client.go` in `Devices`, near `:63-66` | `v.Lease = g.leaseViewLocked(d)` |

The check goes after adoption (`:671`), so the same task in the same session (or an adopted one) is
still the session's own lease. It goes before the other-task check (`:672`), so a queue caller is never
refused or marked shared over a crashed same-name run's leftover lease (rule 0). The upstream kept-lease
ending (`:679-683`) stays for `wait == 0` callers. The reservation is set in `Release`, not in `endLeaseLocked`, so no second list of outcome strings
exists. `Release` already takes `partial` and `cancelled` as literals (`agent.go:1247`). Reserving by
call site, not by outcome, means no outcome needs listing.

### 3.4 MCP surface (one optional argument)

`internal/mcp/tools.go:71-80` `connectIn` gains:

```go
WaitMS int `json:"wait_ms,omitempty" jsonschema:"Wait for the phone instead of returning lease_held at once: first come first served, up to this many ms (at most 600000). One call waits up to about 30 s, then returns lease_held with your place in line; call phone_connect again within 5 s to keep it."`
```

It is passed as `Wait: time.Duration(in.WaitMS) * time.Millisecond` at `tools.go:431-434`, with
`bounds(p["wait_ms"], 0, 600000)` in the schema patch (`tools.go:351`). There is no new tool. `TestToolList`'s
hand list of names (`internal/mcp/mcp_test.go:118-125`) does not change.

### 3.5 CLI (`cmd/pinky/phone.go`, new; `main.go` one case)

```go
// cmd/pinky/main.go, in the command switch next to "task" (main.go:104)
case "phone":
	return runPhone(os.Args[2:])

// cmd/pinky/phone.go

// runPhone is `pinkyctl phone status|tools|call|run`.
func runPhone(args []string) error

// phoneStatus is the one JSON line `phone status` prints: the contract the
// vendored skill reads. testdata/phone-status.golden.json pins its keys.
type phoneStatus struct {
	Free        bool    `json:"free"`         // online, no holder, no line, not kept for another name, the person not in control
	Device      string  `json:"device"`       // the phone's name
	Holder      *string `json:"holder"`       // agent name, null when free
	Task        *string `json:"task"`         // holder's task title
	ReservedFor *string `json:"reserved_for"` // kept for this name (counts as free for that name)
	Waiting     int     `json:"waiting"`      // live tickets
	Link        string  `json:"link"`         // driver.Info.Connection: usb or wifi (driver.go:46)
	Health      string  `json:"health"`       // gateway.DeviceStatus verbatim: idle, working, in_control, locked, needs_setup, offline, connecting, needs_you (api.go:52-67)
}

// phoneClient is one MCP session to the gateway over the unix socket's MCP role,
// named holder (clientInfo.Title), as dialTask does but without starting Pinky:
// a missing gateway is exit 3.
func dialPhone(ctx context.Context, holder string) (*sdk.ClientSession, error)

// phoneRun owns one session for a script: it lazily calls phone_connect
// (task, wait_ms = time left of --wait, device) before the first call that
// needs a lease, forwards every call, and calls phone_release
// (outcome done or failed by exit status) when the script ends, on SIGINT and
// SIGTERM too.
type phoneRun struct {
	cs        *sdk.ClientSession
	holder    string
	task      string
	device    string
	deadline  time.Time
	connected bool
	in, out   int64 // bytes forwarded / bytes the script printed, for the code-mode line
}

// call forwards one tool call through the run's session. Tool names are taken
// with or without the phone_ prefix, as phone_batch takes them (docs/mcp.md
// phone_batch), and checked against the session's tools/list, never a list
// in this file.
func (r *phoneRun) call(ctx context.Context, tool string, args map[string]any) (text string, image []byte, isErr bool, err error)

// serve accepts `phone call` from the script over PINKY_PHONE_RUN, a unix
// socket in a 0700 temp dir: one JSON line each way,
// {"tool":"tap","args":{"ref":12}} -> {"text":"...","image":"<path>","error":false}.
func (r *phoneRun) serve(ctx context.Context, ln net.Listener)
```

Commands:

| Command | Reads | Changes | Output |
|---|---|---|---|
| `phone status [-device D]` | control role `devices` (no token) | nothing | one JSON line, exit 0, or 3 |
| `phone tools [--json] [name]` | MCP `tools/list` | nothing; no lease | names and first sentences, or the full definitions |
| `phone call <tool> [json \| k=v ... \| -]` | MCP `tools/call` | inside `run`: through the run's session; outside: a one-call run (connect, call, release) | the tool's text; exit 0-5 |
| `phone run [-wait S] [-as NAME] [-task T] [-device D] (<file> \| -e '<sh>' \| -)` | its script | one session, one lease for its lifetime | the script's stdout; the code-mode line on stderr; the script's exit status |

- `-as` defaults to `$PINKY_LEASE_HOLDER`, else `Pinky CLI` (`task.go:33` precedent). `-wait` defaults
  to 60 s, as on the iPad. `-task` defaults to `<script name> · <pid>`, which is unique per run (see risk R1).
- `phone_connect` and `phone_release` called explicitly inside a run still work: the broker forwards them
  and keeps `connected` in step with them.
- **Sync with upstream:** `tools`, the tool-name check and the forwarding all come from the live
  `tools/list`. A tool upstream adds works in `phone call` with no CLI change, and the guard test proves it
  (section 5).
- **Transport:** the unix socket's MCP role, through `api.Dial`, `api.EnsureToken`, `greetMCP` and
  `sdk.IOTransport` exactly as `dialTask` does (`task.go:237-260`). Not MCP HTTP: see the rejected
  candidate C.
- **Text:** CLI usage and messages are plain English, like `pinkyctl task`'s usage (`task.go:197-219`) and the
  gateway's agent messages (`agent.go:687`). The `cmd/pinky/locales` rule (AGENTS.md "The app's languages")
  covers the app's own UI, which this does not touch.
- **Feedback line** (owner's rule): the `agent-feedback` pointer goes in the vendored skill and wrapper on
  the ipad-local side, not in upstream help text.

### 3.6 Fork-only wiring

`cmd/pinky/daemon.go:213-215`: `LeaseReserve: 15 * time.Second,` next to `TrustNewAgents` and `AllowDevices`.
Upstream's gateway default stays 0, so upstream tests, `TestSessionCloseReleasesLease`
(`mcp_test.go:335-351`) among them, keep passing unchanged.

### 3.7 B answered point by point

- **Where the line, reservation and holder name live:** `device.line` in `queue.go`'s type, guarded by
  `g.mu`. The holder name is `Session.agent.Name` (`agent.go:327-331`), which comes from `clientInfo.Title`.
- **`phone_connect` for a waiter:** blocks up to the call budget less 10 s, waking on `d.wake`. Then it
  either gets the lease (normal first screen) or returns `lease_held` with its place. The CLI calls again
  until `-wait` runs out, then exits 4.
- **`phone_release`:** unchanged, plus `reserveLocked` when `LeaseReserve > 0`. The release closes
  `d.wake` (`record.go:351`), so the head waiter wakes at once. It still meets the reservation unless it has
  the same name, and it waits it out (≤ 15 s).
- **Status read:** `Devices()` fills `DeviceView.Lease` (`client.go:39-72`). The CLI reads it over the
  control role and derives `free` for its own `-as` name: a reservation for that name counts as free.
- **A session that dies mid-queue:** its ticket is pruned at the next `turnLocked` (session closed, or
  not waiting for `ticketGrace`). The next live ticket goes first within ≤ 5 s. The design does not depend
  on whether the SDK cancels a handler's ctx when the session closes (**unproven**, not checked in
  go-sdk v1.8.0). `waitTurn` checks `s.closed` on every tick anyway.
- **A holder that dies while holding:** the session closes, and `keepLocked` keeps the lease for 60 s
  (`resume.go:195-209`). For a queue caller, `turnLocked` rule 0 ends that lease, and it does so before
  `runsOtherTask`, which would otherwise refuse a same-name caller for the full 60 s (`shared.go:129-131`).
  This holds for any holder name, the dead run's own included. But the waiter must wake, and `Session.end` does not close `d.wake`
  (`agent.go:379-405`), so the `PollInterval` tick (250 ms, `gateway.go:51`) covers it. No reservation is
  made, because the dead run never called `phone_release`.
- **Person takeover:** the lease stays with its holder (`client.go:477-530`) and the line does not move.
  If the holder idles out (10 min, `gateway.go:852-867`), the next ticket's lease starts paused, because
  `d.person` holds across leases. It gets `paused` (CLI exit 5) and should call `phone_handoff`. Status shows
  `health: in_control, free: false`.
- **Pause connection:** the task ends with `OutcomePaused`. `waitTurn` returns upstream's `device_paused`
  through `connect` (`agent.go:650-653`) to every waiter. That is exit 5.
- **Stop / Stop all:** no reservation. The line moves. The stopped session itself is barred
  (`agent.go:654-662`).
- **Handoff / approval open:** the lease is held, so the line waits.

## 4. Synthesis decision (within this runner)

Base: candidate A below. I took one idea from B (the CLI owns the run's lifetime and releases at the end, as
the iPad does) and one from C (status is a read of gateway state, never the CLI's own bookkeeping). I first
had a separate 5 s "grant" reservation, as the iPad has ("a lease still being granted"). I dropped it once
`agent.go:600-720` was confirmed to be one critical section: the turn decision and lease creation are
atomic, so a grant window adds nothing.

### Candidate A (chosen): gateway-owned line + generic MCP forwarder with a per-run shell broker

As in section 3.

### Candidate B (rejected): queue outside the gateway, in the CLI; code mode in embedded JavaScript (goja)

The gateway would stay byte-identical to upstream. `phone run` would take a flock plus ticket files under
`$PINKY_HOME/run/phone-line/`, keep the 15 s reservation in a file, and retry `phone_connect` when it gets
`lease_held`. Code mode would be a goja VM with one function per tool from `tools/list`
(`phone.tap({ref: 12})`) and `print`.

Why it lost:
- **Split ownership.** The gateway's lease and the CLI's line are two owners of one decision. Any agent on
  MCP (stdio or HTTP) skips the line and wins every race (red flag "split ownership").
- Stale tickets after a crash need their own liveness machinery, which duplicates what the gateway already
  knows (`Session.closed`).
- `phone status` would need two sources (files plus control `devices`).
- goja adds a module to `go.mod`/`go.sum`, a recurring upstream merge conflict, plus a second language
  surface the owner ruled out ("no new script dialect"). Tool results are text for models
  (docs/mcp.md "Results"), so a JS script parses text with regexes just as the shell does, and sandboxing
  buys nothing for an agent that already has a shell.
- Depth: zero gateway diff, but the complexity reappears in every caller that is not the CLI.

### Candidate C (rejected): new MCP tools `phone_queue`/`phone_status`; code mode through MCP HTTP sessions kept in an env var

`phone run` would `initialize` over `POST /mcp`, export `Mcp-Session-Id`, and have each `phone call` POST with
it. No broker process.

Why it lost:
- HTTP sessions are `Resume` (`server.go:262-266`). A newer session of the same name takes over a lease
  the older one is not using in a call (`resume.go:72-74`; docs/mcp.md "Isolation"). Two runs with the same
  holder name would steal from each other mid-script.
- Names get " (remote)" (`server.go:265`), a second identity and scope per holder.
- It needs `--http` and the token in each child process. It adds two tools to the catalog and to
  `TestToolList`'s hand list (`mcp_test.go:125`), plus an MCP-side status the control role already
  serves.
- Bigger public surface for the same behaviour.

## 5. Guard for the shared seam (C)

Two seams, each with a declaration and a test that fails with a `fix:` line.

**Seam 1: the lease line (gateway).**
- Declared in `queue.go`'s doc comment and in docs/mcp.md phone_connect (`wait_ms` row plus a
  "Waiting for a phone" paragraph).
- `internal/gateway/queue_test.go`, using `newTestGateway`/`connect` (`gateway_test.go:35-67`) with
  `LeaseReserve: 200*time.Millisecond`. Every failure message ends
  `fix: the lease line's contract is in docs/mcp.md phone_connect and the vendored control-local-iphone skill; change them together`.
  - `TestQueueFIFO`: holder A, then B and C wait. A releases, so B gets it; B releases, so C gets it.
  - `TestQueueWaitTimeout`: `wait_ms` 300 ms against a held phone returns `lease_held` with "In line".
  - `TestQueueKeepsPlaceAcrossCalls`: B's call returns at the budget; B calls again within the grace and is
    still first ahead of C.
  - `TestQueueAbandonedHeadSkipped`: B waits, then stops calling; C gets the phone ≤ `ticketGrace` after A
    releases.
  - `TestQueueDeadSessionSkipped`: B's session closes mid-wait; C goes first.
  - `TestReservationSameName`: A releases; a new session named A connects first, ahead of waiting B; a
    session named D with wait 0 gets `lease_held` "kept for A".
  - `TestNoReservationAfterStopOrCrash`: Stop, and a session closed without release, leave no reservation.
  - `TestSameNameAfterCrash`: session `worker` with task `a · 1` connects and closes without release. A new
    session `worker` with task `b · 2` and `wait_ms` gets the phone at once. It does not get
    `lease_held` "another task of an earlier session", and it is not marked shared.
  - `TestTakeOverHoldsLine`: takeover during A's lease; B keeps waiting; after A's release B's lease starts
    `paused`.
  - `TestPauseEndsWaits`: `PauseDevice` returns `device_paused` to waiters.
  - `TestUpstreamLeaseHeldUnchanged`: with `wait_ms=0` and `LeaseReserve=0`, the result text equals today's
    `"<phone> is in use by <agent> · <task>."` (`agent.go:687`). `TestLeaseHeld` (`gateway_test.go:402`)
    stays as it is.

**Seam 2: the CLI contract.**
- Declared by the Go types in `phone.go` (`phoneStatus`, the exit-code constants) and pinned by
  `cmd/pinky/testdata/phone-status.golden.json`. The vendored skill reads that file's shape.
- `cmd/pinky/phone_test.go`, against an in-process gateway with demo phones (the `internal/mcp` harness
  pattern, `mcp_test.go:28-77`, served on a temp `PINKY_HOME` socket).
  - `TestPhoneStatusKeys`: the status JSON's keys and types equal the golden. Failure:
    `fix: update docs/mcp.md "From a shell: pinkyctl phone" and control-local-iphone's SKILL.md, then the golden`.
  - `TestPhoneForwardsUnknownTool`: the test adds a tool `phone_probe` to the server with
    `sdk.AddTool(srv.SDK(), …)` (`server.go:102`). `phone tools` lists it, and `phone call probe x=1` returns
    its text, with no CLI change. This proves there is no hand list.
  - `TestPhoneRunOneLease`: a script makes three calls. `DeviceView.LeaseGen` (`api.go:79`) goes up by one
    over the whole run, the lease ends at exit, and status shows `reserved_for` equal to the `-as` name.
  - `TestPhoneExitCodes`: 0 settled, 1 `invalid_argument`, 2 bad flags, 3 no socket, 4 `lease_held`
    after `-wait 1`, 5 `paused` (takeover).
  - `TestPhoneImageToFile`: `observe screenshot=true` prints `image: <path>`, and the file is a JPEG.
  - `TestPhoneRunReleasesOnSignal`: SIGTERM to `run` releases (status free or reserved), exit 143.

Run: `go test ./internal/gateway -run 'Queue|Reservation|SameNameAfterCrash|TakeOverHoldsLine|PauseEndsWaits|UpstreamLeaseHeld|LeaseHeld' && go test ./cmd/pinky -run Phone && go test ./internal/mcp -run 'ToolList|PendingAndErrors|SessionClose'`.
All three print `ok` or `FAIL`.

## 6. Smallest surface and smallest upstream diff (D)

Public surface added:
- `ConnectRequest.Wait`, `Options.LeaseReserve`, `DeviceView.Lease`, type `LeaseView`
- MCP `phone_connect.wait_ms`
- CLI `pinkyctl phone status|tools|call|run`

Nothing else is exported.

| File | Action | Est. lines |
|---|---|---|
| `internal/gateway/queue.go` | **add** | ~180 |
| `internal/gateway/queue_test.go` | **add** | ~260 |
| `internal/gateway/agent.go` | edit: rename `Connect`→`connect` (1), turn check (1), `Wait` field (4), `queued` field (1), `reserveLocked` call (1) | ~8 |
| `internal/gateway/state.go` | edit: `line line` on device | 1 |
| `internal/gateway/gateway.go` | edit: `LeaseReserve` option | 4 |
| `internal/gateway/api.go` | edit: `Lease LeaseView` on `DeviceView` | 2 |
| `internal/gateway/client.go` | edit: fill `v.Lease` in `Devices` | 1 |
| `internal/mcp/tools.go` | edit: `WaitMS` on `connectIn`, pass it, bound it | 3 |
| `cmd/pinky/main.go` | edit: `case "phone"` plus one usage line next to `task run|status|…` (`main.go:163-165`) | 3 |
| `cmd/pinky/daemon.go` | edit (fork-only): `LeaseReserve: 15*time.Second` | 1 |
| `cmd/pinky/phone.go` | **add** | ~350 |
| `cmd/pinky/phone_test.go`, `cmd/pinky/testdata/phone-status.golden.json` | **add** | ~250 |
| `docs/mcp.md` | edit: `wait_ms` row, "Waiting for a phone", "From a shell: pinkyctl phone" (after `:651-663`) | ~40 |

Left alone: `internal/api/*` (control `devices` already returns `DeviceView`, `control.go:173`),
`cmd/pinky/task.go`, `shim*.go`, `internal/mcp/server.go`, `render.go`, `resume.go`, `record.go`, `ui/`,
`cmd/pinky/locales`, `docs/SKILL.md`, `CHANGELOG*` (fork, not an upstream release), `go.mod`/`go.sum` (no
new dependency).

Upstream-file diff without new files: about 23 lines across 9 files. Every edit is an added field, line or
case, except one function rename.

## 7. codebase-design pass

- **Deletion test, `queue.go`:** delete it and three things come back in every caller: who goes next, how
  long a released phone is kept, and when a waiter has given up. Today that is every MCP client's retry
  loop on `lease_held`. It earns its keep. Its interface is one request field (`Wait`), one option, one view.
  `turnLocked`/`reserveLocked` are internal seams used by agent.go only (package-private, so not importable
  from outside: red flag "importable internals" avoided).
- **Deletion test, the broker (`phoneRun.serve`):** delete it and each `phone call` in a script opens its own
  session. The lease, the screen diff baseline and the approvals then end with every call, so code mode
  stops existing. It earns its keep. Its JSON-line protocol is an **internal seam with one adapter** (the
  `phone call` client in the same file). It is not a public interface and is documented only as "inside
  `run`, `phone call` uses the run's session".
- **One way per task:** one way to wait (`wait_ms`), one way to name a holder (`clientInfo.Title` via `-as`
  or `PINKY_LEASE_HOLDER`), one status source (`DeviceView.Lease`). A one-shot `phone call` is defined as a
  one-call run, the same code path, not a second implementation.
- **No hand-synced lists:** tool names come from `tools/list`. Exit-code mapping uses `gateway.Status*`
  and `gateway.Err*`. Reservation is decided by call site, not by an outcome list.
- **Caller must not know internals:** a caller passes `wait_ms` and reads `lease_held` plus its own deadline.
  Tickets, grace and pruning stay hidden.

## 8. Tradeoffs accepted

- We accept that a waiting agent makes repeated `phone_connect` calls (one per ~30 s) in exchange for keeping
  Pinky's rule that every call answers within 45 s (`budget.go:10-19`).
- We accept that reads need the lease (`agent.go:413-417, 472`), so a script queues at its first phone call
  of any kind, unlike the iPad's first state-changing call. In exchange, the gateway's read path is not
  forked.
- We accept a 250 ms poll in `waitTurn` alongside `d.wake`, in exchange for not adding a wake to `Session.end`.
- We accept that the 15 s reservation applies to every agent of the fork's daemon, MCP clients included,
  in exchange for one rule with no per-client exceptions.

## 9. Risks and open questions

- **R1. Same holder plus same task title continues the other run's lease and skips the line.** A local
  agent's `phone_connect` naming the same task adopts an open older session's idle lease
  (`resume.go:75-76`, adoption at `agent.go:671`, before the turn check). The CLI's default task title is
  unique per run (`<script> · <pid>`). An explicit `-task` shared by two concurrent runs of one name is
  allowed to continue the other's lease. Should `phone run` refuse a `-task` another live run of the same
  holder uses?
- **R2. A holder name equal to a real client's display name** ("Claude Code") shares that agent's identity,
  scope and Stop (`server.go:257-260, 292-323`; Stop inheritance `resume.go:145-172`). Should the CLI prefix holder names (`cli:`)?
- **R3. Every new holder name leaves a persistent agent record** in Settings › Agents (`client.go:945-972`).
  With `PINKY_TRUST_AGENTS=1` it is approved silently, but the list grows. Is that acceptable, or should the
  CLI recommend a small fixed set of names?
- **R4. One-shot `phone call` outside `run` is costly and cannot carry approvals.** Each one pays a
  `phone_connect` first-screen read (0.5–3 s, about 7 s on a WebDriverAgent home screen: docs/mcp.md
  Limits). Its release withdraws open approvals (`record.go:354`). An `approval_id` from one invocation is
  dead in the next. The rule for agents: anything that may ask the person, or more than two calls, goes in
  `run`.
- **R5.** Whether the SDK cancels a handler's ctx when its session closes is **unproven**. The design does
  not rely on it (`waitTurn` checks `s.closed`).
- **R6.** The reservation is in memory: a daemon restart drops it and the line, and waiting calls fail
  with the session. Acceptable for a 15 s hold?
- **R7.** `waitTurn` leaves `approvalReserve` (10 s) for the first screen read. On a slow WebDriverAgent
  home screen (≈7 s) that is tight. The result could be `uncertain` / out-of-time on the first read
  (`budget.go:81`). Should the reserve be 15 s for connect?

## 10. Build order (each step has a pass/fail test)

1. **Types and option, no behaviour.** Add `line`/`ticket`/`LeaseView`, `Options.LeaseReserve`,
   `ConnectRequest.Wait`, `DeviceView.Lease`, rename `Connect`→`connect` and add the forwarding `Connect`.
   Test: `go build ./... && go test ./internal/gateway ./internal/mcp` (all upstream tests pass unchanged).
2. **Turn check inside connect.** `turnLocked` with `wait == 0` semantics only (reservation and live line
   refuse), plus `reserveLocked` from `Release`. Test:
   `go test ./internal/gateway -run 'Reservation|NoReservationAfterStopOrCrash|UpstreamLeaseHeldUnchanged|LeaseHeld'`.
3. **Waiting.** `waitTurn`, tickets, grace, pruning, budget, `Result.queued`, rule 0. Test:
   `go test ./internal/gateway -run 'Queue|SameNameAfterCrash|TakeOverHoldsLine|PauseEndsWaits'`.
4. **Status view.** `leaseViewLocked` in `Devices`. Test: `go test ./internal/gateway -run LeaseView`
   (holder, task, reserved_for, waiting order).
5. **MCP argument.** `wait_ms` on `phone_connect`. Test:
   `go test ./internal/mcp -run 'ToolList|PendingAndErrors|ConnectWait'` (new `TestConnectWait`: two MCP
   clients, the second with `wait_ms` gets the phone after the first releases).
6. **CLI `status` and `tools`.** Test: `go test ./cmd/pinky -run 'PhoneStatusKeys|PhoneForwardsUnknownTool'`.
7. **CLI `call` (one-shot) and exit codes.** Test: `go test ./cmd/pinky -run 'PhoneExitCodes|PhoneImageToFile'`.
8. **CLI `run` with broker.** Test: `go test ./cmd/pinky -run 'PhoneRunOneLease|PhoneRunReleasesOnSignal'`.
9. **Fork wiring and docs.** `daemon.go` `LeaseReserve: 15s`; docs/mcp.md sections. Test:
   `go vet ./cmd/pinky && go test ./cmd/pinky -run Phone` (golden matches docs example), then `make check`
   once as the big gate.
10. **Device check (orchestrator, big gate only).** With the daemon on the iPhone SE:
    `phone status`, then two `phone run` from two holders. The second waits, then runs, and `reserved_for`
    shows the first name for ≤ 15 s. Evidence: the status JSON lines plus both runs' exit codes.

## Next implementation step

Step 1: add the types, the option and the `Connect`→`connect` rename, and confirm the upstream suite is
unchanged before any behaviour lands.
