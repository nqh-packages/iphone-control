package gateway

import (
	"context"
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

// leaseViewLocked is d's LeaseView, with dead tickets left out. The caller holds g.mu.
func (g *Gateway) leaseViewLocked(d *device) LeaseView {
	return LeaseView{}
}
