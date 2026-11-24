package gateway

import (
	"context"
	"strings"
	"testing"
	"time"
)

const queueFix = "\nfix: the lease line's contract is in docs/mcp.md phone_connect and the vendored control-local-iphone skill; change them together"

func queueFail(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf(format+queueFix, args...)
}

func named(t *testing.T, g *Gateway, name string) *Session {
	t.Helper()
	s := g.OpenSession(AgentInfo{Name: name})
	t.Cleanup(s.Close)
	return s
}

func connectAs(t *testing.T, g *Gateway, name, task string) *Session {
	t.Helper()
	s := named(t, g, name)
	if res := s.Connect(context.Background(), ConnectRequest{Task: task}); res.IsError() || res.Status != StatusSettled {
		queueFail(t, "%s connect %q: %+v", name, task, res)
	}
	return s
}

func releaseAll(t *testing.T, s *Session) {
	t.Helper()
	if res := s.Release(context.Background(), ReleaseRequest{}); res.IsError() {
		queueFail(t, "release: %+v", res)
	}
}

func withReserve(d time.Duration) func(*Options) {
	return func(o *Options) { o.LeaseReserve = d }
}

func TestReservationSameName(t *testing.T) {
	g, _ := newTestGateway(t, withReserve(300*time.Millisecond))
	a := connectAs(t, g, "worker-a", "first")
	releaseAll(t, a)
	d := named(t, g, "worker-d")
	res := d.Connect(context.Background(), ConnectRequest{Task: "other"})
	if res.Error != ErrLeaseHeld || !strings.Contains(res.Message, "kept for worker-a") {
		queueFail(t, "another name right after the release should get lease_held kept for worker-a: %+v", res)
	}
	connectAs(t, g, "worker-a", "second")
}

func TestReservationExpires(t *testing.T) {
	g, _ := newTestGateway(t, withReserve(50*time.Millisecond))
	releaseAll(t, connectAs(t, g, "worker-a", "first"))
	time.Sleep(80 * time.Millisecond)
	connectAs(t, g, "worker-d", "other")
}

func TestNoReservationAfterStopOrCrash(t *testing.T) {
	g, _ := newTestGateway(t, withReserve(time.Minute))
	connectAs(t, g, "worker-a", "first")
	if err := g.Stop("pixel"); err != nil {
		t.Fatal(err)
	}
	b := connectAs(t, g, "worker-b", "after stop")
	b.CloseNow()
	connectAs(t, g, "worker-c", "after crash")
}

func TestUpstreamLeaseHeldUnchanged(t *testing.T) {
	g, _ := newTestGateway(t)
	connect(t, g, "Reorder lunch")
	other := named(t, g, "Codex")
	res := other.Connect(context.Background(), ConnectRequest{Task: "Something else"})
	want := "Pixel 9 is in use by Claude Code · Reorder lunch."
	if res.Error != ErrLeaseHeld || res.Message != want {
		queueFail(t, "with no line and no reservation lease_held must read %q, got %+v", want, res)
	}
}
