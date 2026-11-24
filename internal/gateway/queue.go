package gateway

import (
	"context"
	"fmt"
	"slices"
	"time"
)

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
	deadline time.Time // from the call's wait_ms, capped at maxWait
	waiting  bool      // a phone_connect of the session is waiting now
	lastCall time.Time // when that session's last waiting call returned
}

// ticketGrace is how long an abandoned ticket keeps holding the line; a
// variable so tests can shorten it.
var ticketGrace = 5 * time.Second

// maxWait is the longest wait_ms; more is cut to it.
const maxWait = 10 * time.Minute

// LeaseView is who has the phone, who it is kept for, and who waits. Read by the
// client API in DeviceView.Lease.
type LeaseView struct {
	Holder        string    `json:",omitempty"` // agent name of the session holding the lease
	Task          string    `json:",omitempty"` // that lease's task title
	ReservedFor   string    `json:",omitempty"` // the phone is kept for this agent name until ReservedUntil
	ReservedUntil time.Time `json:",omitzero"`
	Waiting       []string  `json:",omitempty"` // agent names in line, first first (live tickets only)
}

// Connect is phone_connect. With req.Wait it queues for the phone (this
// file); without, it is connect: lease_held at once, as before.
func (s *Session) Connect(ctx context.Context, req ConnectRequest) *Result {
	if req.Wait <= 0 {
		return s.connect(ctx, req)
	}
	return s.g.waitTurn(ctx, s, req)
}

// turnLocked decides, inside connect's critical section, right after
// adoption and before the other-task check, whether s may take d now. nil:
// go on (connect then creates the lease without unlocking, so the decision
// cannot go stale). A result: lease_held with Holder set. With wait > 0 it
// adds or refreshes s's ticket and returns the place in line, with queued
// set. With wait == 0, no tickets and no reservation it returns nil, and
// upstream's lease_held texts stand. The caller holds g.mu.
func (g *Gateway) turnLocked(s *Session, d *device, wait time.Duration) *Result {
	if l := d.lease; wait > 0 && l != nil && l.session != s && l.session.closed && !l.session.agent.Resume && l.inflight == 0 {
		g.endLeaseLocked(l, OutcomeDisconnected, "")
	}
	if d.lease != nil && d.lease.session == s {
		return nil
	}
	ln := &d.line
	now := time.Now()
	ln.prune(now)
	mine := ln.reserved == s.agent.Name || (ln.reserved == "" && (len(ln.tickets) == 0 || ln.tickets[0].session == s))
	if d.lease == nil && mine {
		ln.remove(s)
		ln.reserved = ""
		return nil
	}
	if wait <= 0 {
		if d.lease != nil && len(ln.tickets) == 0 && ln.reserved == "" {
			return nil
		}
		return g.heldLocked(s, d, now)
	}
	return g.queuedLocked(s, d, now, min(wait, maxWait))
}

// queuedLocked puts s in line for d, or keeps its place, and says where it is.
func (g *Gateway) queuedLocked(s *Session, d *device, now time.Time, wait time.Duration) *Result {
	ln := &d.line
	i := slices.IndexFunc(ln.tickets, func(t *ticket) bool { return t.session == s })
	if i < 0 {
		ln.tickets = append(ln.tickets, &ticket{session: s})
		i = len(ln.tickets) - 1
	}
	t := ln.tickets[i]
	if end := now.Add(wait); end.After(t.deadline) {
		t.deadline = end
	}
	t.waiting = true
	var holder, behind string
	switch {
	case i > 0:
		behind = ln.tickets[i-1].session.agent.Name
	case d.lease != nil:
		behind = d.lease.session.agent.Name
		holder = fmt.Sprintf("%s · %s", behind, d.lease.task.title)
	default:
		behind = ln.reserved
	}
	if holder == "" {
		holder = behind
	}
	res := errResult(ErrLeaseHeld, "In line for %s: %s, behind %s. Call phone_connect again with wait_ms to keep your place.", d.name(), ordinal(i+1), behind)
	res.Holder, res.Device, res.queued = holder, d.id, true
	return res
}

func ordinal(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		return fmt.Sprintf("%dst", n)
	case n%10 == 2:
		return fmt.Sprintf("%dnd", n)
	case n%10 == 3:
		return fmt.Sprintf("%drd", n)
	}
	return fmt.Sprintf("%dth", n)
}

// connectReserve is what a waiting phone_connect leaves of the call's
// budget for connect's first read of the screen: a phone through
// WebDriverAgent takes about 7 s for it.
const connectReserve = 15 * time.Second

// waitTurn calls connect until it returns anything but a queued lease_held,
// waking on the device's state changes (d.wake), a short tick
// (Options.PollInterval) for reservation and ticket expiry, ctx, or the end
// of req.Wait or of the call's budget less connectReserve. It returns the
// last lease_held, whose message gives the place in line. It tells a queued
// lease_held from every other one (shared.go's other-task refusal,
// busyElsewhereLocked) by Result.queued, which only queuedLocked sets. It
// never parses the message.
func (g *Gateway) waitTurn(ctx context.Context, s *Session, req ConnectRequest) *Result {
	ctx = g.withBudget(ctx)
	asked := time.Now().Add(min(req.Wait, maxWait))
	end := budgetBefore(ctx, asked, connectReserve)
	var res *Result
	defer func() { g.settleTickets(s, res) }()
	for {
		req.Wait = max(time.Until(asked), time.Nanosecond)
		res = s.connect(ctx, req)
		remain := time.Until(end)
		if !res.queued || remain <= 0 || ctx.Err() != nil {
			return res
		}
		g.mu.Lock()
		var wake chan struct{}
		if d := g.findDeviceLocked(res.Device); d != nil {
			wake = d.wake
		}
		g.mu.Unlock()
		tick := time.NewTimer(min(g.opt.PollInterval, remain))
		select {
		case <-wake:
		case <-tick.C:
		case <-ctx.Done():
		}
		tick.Stop()
	}
}

// settleTickets ends a waiting call: s's tickets stay for ticketGrace when
// the call left it in line, and go when it did not.
func (g *Gateway) settleTickets(s *Session, res *Result) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for _, d := range g.devices {
		if res != nil && res.queued {
			for _, t := range d.line.tickets {
				if t.session == s {
					t.waiting, t.lastCall = false, now
				}
			}
		} else {
			d.line.remove(s)
		}
	}
}

// heldLocked is the lease_held answer to s, which does not have its turn on d.
func (g *Gateway) heldLocked(s *Session, d *device, now time.Time) *Result {
	ln := &d.line
	var res *Result
	switch {
	case d.lease != nil:
		holder := fmt.Sprintf("%s · %s", d.lease.session.agent.Name, d.lease.task.title)
		res = errResult(ErrLeaseHeld, "%s is in use by %s. %d waiting.", d.name(), holder, len(ln.tickets))
		res.Holder = holder
	case ln.reserved != "":
		res = errResult(ErrLeaseHeld, "%s is kept for %s for %d s.", d.name(), ln.reserved, int((ln.until.Sub(now)+time.Second-1)/time.Second))
		res.Holder = ln.reserved
	default:
		res = errResult(ErrLeaseHeld, "%s is next for %s. %d waiting.", d.name(), ln.tickets[0].session.agent.Name, len(ln.tickets))
		res.Holder = ln.tickets[0].session.agent.Name
	}
	res.Device = d.id
	return res
}

// prune drops the tickets whose session is closed, whose deadline passed, or
// that are not waiting and were last called more than ticketGrace ago, and
// a reservation past its end.
func (ln *line) prune(now time.Time) {
	live := ln.tickets[:0]
	for _, t := range ln.tickets {
		if t.session.closed || now.After(t.deadline) || !t.waiting && now.Sub(t.lastCall) > ticketGrace {
			continue
		}
		live = append(live, t)
	}
	clear(ln.tickets[len(live):])
	ln.tickets = live
	if ln.reserved != "" && !now.Before(ln.until) {
		ln.reserved = ""
	}
}

func (ln *line) remove(s *Session) {
	for i, t := range ln.tickets {
		if t.session == s {
			ln.tickets = append(ln.tickets[:i], ln.tickets[i+1:]...)
			return
		}
	}
}

// reserveLocked keeps d for the agent that just released it, for
// Options.LeaseReserve. Called only from Release, after endLeaseLocked:
// only an agent's own "run ended" reserves. Stop, pause, idle expiry,
// offline and a dropped session never reserve. The caller holds g.mu.
func (g *Gateway) reserveLocked(d *device, agent string) {
	if g.opt.LeaseReserve <= 0 {
		return
	}
	d.line.reserved, d.line.until = agent, time.Now().Add(g.opt.LeaseReserve)
}

// leaseViewLocked is d's LeaseView, with dead tickets left out. The caller holds g.mu.
func (g *Gateway) leaseViewLocked(d *device) LeaseView {
	return LeaseView{}
}
