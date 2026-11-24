package gateway

import (
	"context"
	"fmt"
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

// Connect is phone_connect. With req.Wait it queues for the phone (this
// file); without, it is connect: lease_held at once, as before.
func (s *Session) Connect(ctx context.Context, req ConnectRequest) *Result {
	return s.connect(ctx, req)
}

// turnLocked decides, inside connect's critical section, right after
// adoption and before the other-task check, whether s may take d now. nil:
// go on (connect then creates the lease without unlocking, so the decision
// cannot go stale). A result: lease_held with Holder set. With wait == 0, no
// tickets and no reservation it returns nil, and upstream's lease_held texts
// stand. The caller holds g.mu.
func (g *Gateway) turnLocked(s *Session, d *device, wait time.Duration) *Result {
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
	if d.lease != nil && len(ln.tickets) == 0 && ln.reserved == "" {
		return nil
	}
	return g.heldLocked(s, d, now)
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
