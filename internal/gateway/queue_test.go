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

func connectWaiting(s *Session, task string, wait time.Duration) <-chan *Result {
	ch := make(chan *Result, 1)
	go func() { ch <- s.Connect(context.Background(), ConnectRequest{Task: task, Wait: wait}) }()
	return ch
}

func callThenLeave(t *testing.T, g *Gateway, s *Session, task string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *Result, 1)
	go func() { done <- s.Connect(ctx, ConnectRequest{Task: task, Wait: 5 * time.Second}) }()
	for lineLen(g) == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	if res := gets(t, done, task); res.Error != ErrLeaseHeld {
		queueFail(t, "%s: %+v", task, res)
	}
}

func lineLen(g *Gateway) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.devices["pixel"].line.tickets)
}

func waitLine(t *testing.T, g *Gateway, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for lineLen(g) != n {
		if time.Now().After(deadline) {
			queueFail(t, "want %d in line, have %d", n, lineLen(g))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func gets(t *testing.T, ch <-chan *Result, who string) *Result {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(3 * time.Second):
		queueFail(t, "%s was not answered in 3 s", who)
		return nil
	}
}

func notYet(t *testing.T, ch <-chan *Result, who string) {
	t.Helper()
	select {
	case res := <-ch:
		queueFail(t, "%s answered too early: %+v", who, res)
	case <-time.After(60 * time.Millisecond):
	}
}

func TestQueueFIFO(t *testing.T) {
	g, _ := newTestGateway(t)
	a := connectAs(t, g, "worker-a", "a")
	b, c := named(t, g, "worker-b"), named(t, g, "worker-c")
	rb := connectWaiting(b, "b", 5*time.Second)
	waitLine(t, g, 1)
	rc := connectWaiting(c, "c", 5*time.Second)
	waitLine(t, g, 2)
	notYet(t, rb, "b while a holds")
	releaseAll(t, a)
	if res := gets(t, rb, "b"); res.Status != StatusSettled {
		queueFail(t, "b should get the phone first: %+v", res)
	}
	notYet(t, rc, "c while b holds")
	releaseAll(t, b)
	if res := gets(t, rc, "c"); res.Status != StatusSettled {
		queueFail(t, "c should get the phone second: %+v", res)
	}
}

func TestQueueWaitTimeout(t *testing.T) {
	g, _ := newTestGateway(t)
	connectAs(t, g, "worker-a", "a")
	b := named(t, g, "worker-b")
	start := time.Now()
	res := b.Connect(context.Background(), ConnectRequest{Task: "b", Wait: 300 * time.Millisecond})
	if res.Error != ErrLeaseHeld || !strings.Contains(res.Message, "In line") || !strings.Contains(res.Message, "1st, behind worker-a") {
		queueFail(t, "a wait that runs out should say the place in line: %+v", res)
	}
	if time.Since(start) < 250*time.Millisecond {
		queueFail(t, "returned after %s, before the wait ran out", time.Since(start))
	}
}

func TestQueueKeepsPlaceAcrossCalls(t *testing.T) {
	g, _ := newTestGateway(t)
	a := connectAs(t, g, "worker-a", "a")
	b, c := named(t, g, "worker-b"), named(t, g, "worker-c")
	callThenLeave(t, g, b, "b")
	rc := connectWaiting(c, "c", 5*time.Second)
	waitLine(t, g, 2)
	rb := connectWaiting(b, "b", 5*time.Second)
	releaseAll(t, a)
	if res := gets(t, rb, "b"); res.Status != StatusSettled {
		queueFail(t, "b called again within the grace and should still be first: %+v", res)
	}
	notYet(t, rc, "c behind b")
}

func TestQueueAbandonedHeadSkipped(t *testing.T) {
	defer func(d time.Duration) { ticketGrace = d }(ticketGrace)
	ticketGrace = 100 * time.Millisecond
	g, _ := newTestGateway(t)
	a := connectAs(t, g, "worker-a", "a")
	b, c := named(t, g, "worker-b"), named(t, g, "worker-c")
	callThenLeave(t, g, b, "b")
	rc := connectWaiting(c, "c", 5*time.Second)
	waitLine(t, g, 2)
	releaseAll(t, a)
	if res := gets(t, rc, "c"); res.Status != StatusSettled {
		queueFail(t, "b stopped calling, so c should get the phone once b's ticket lapses: %+v", res)
	}
}

func TestQueueDeadSessionSkipped(t *testing.T) {
	g, _ := newTestGateway(t)
	a := connectAs(t, g, "worker-a", "a")
	b, c := named(t, g, "worker-b"), named(t, g, "worker-c")
	connectWaiting(b, "b", 5*time.Second)
	waitLine(t, g, 1)
	rc := connectWaiting(c, "c", 5*time.Second)
	waitLine(t, g, 2)
	b.Close()
	releaseAll(t, a)
	if res := gets(t, rc, "c"); res.Status != StatusSettled {
		queueFail(t, "b's session closed, so c should go first: %+v", res)
	}
}

func TestQueueReservationAheadOfLine(t *testing.T) {
	g, _ := newTestGateway(t, withReserve(300*time.Millisecond))
	a := connectAs(t, g, "worker-a", "a")
	rb := connectWaiting(named(t, g, "worker-b"), "b", 5*time.Second)
	waitLine(t, g, 1)
	releaseAll(t, a)
	released := time.Now()
	a2 := named(t, g, "worker-a")
	if res := a2.Connect(context.Background(), ConnectRequest{Task: "a again"}); res.Status != StatusSettled {
		queueFail(t, "the releasing name should get the phone ahead of the line: %+v", res)
	}
	notYet(t, rb, "b while a is back")
	releaseAll(t, a2)
	res := gets(t, rb, "b")
	if res.Status != StatusSettled {
		queueFail(t, "b should get the phone once the reservation ends: %+v", res)
	}
	if time.Since(released) < 300*time.Millisecond {
		queueFail(t, "b got the phone %s after the release, inside the reservation", time.Since(released))
	}
}

func TestSameNameAfterCrash(t *testing.T) {
	g, _ := newTestGateway(t)
	local := AgentInfo{Name: "worker", Reconnect: true}
	s1 := g.OpenSession(local)
	if res := s1.Connect(context.Background(), ConnectRequest{Task: "a · 1"}); res.Status != StatusSettled {
		t.Fatalf("first run: %+v", res)
	}
	s1.Close()
	s2 := g.OpenSession(local)
	t.Cleanup(s2.Close)
	res := s2.Connect(context.Background(), ConnectRequest{Task: "b · 2", Wait: 2 * time.Second})
	if res.Status != StatusSettled {
		queueFail(t, "a crashed run's kept lease must not block the same name's next run: %+v", res)
	}
	g.mu.Lock()
	shared := s2.shared
	g.mu.Unlock()
	if shared {
		queueFail(t, "the session was marked shared by the crashed run's lease")
	}
}

func TestTakeOverHoldsLine(t *testing.T) {
	g, _ := newTestGateway(t)
	a := connectAs(t, g, "worker-a", "a")
	if err := g.TakeOver("pixel"); err != nil {
		t.Fatal(err)
	}
	rb := connectWaiting(named(t, g, "worker-b"), "b", 5*time.Second)
	waitLine(t, g, 1)
	notYet(t, rb, "b during the takeover")
	releaseAll(t, a)
	if res := gets(t, rb, "b"); res.Status != StatusPaused {
		queueFail(t, "the person still has the phone, so b's lease should start paused: %+v", res)
	}
}

func TestPauseEndsWaits(t *testing.T) {
	g, _ := newTestGateway(t)
	connectAs(t, g, "worker-a", "a")
	rb := connectWaiting(named(t, g, "worker-b"), "b", 5*time.Second)
	waitLine(t, g, 1)
	if err := g.PauseDevice("pixel"); err != nil {
		t.Fatal(err)
	}
	if res := gets(t, rb, "b"); res.Error != ErrDevicePaused {
		queueFail(t, "pausing the phone should answer waiters device_paused: %+v", res)
	}
}

func TestLeaseView(t *testing.T) {
	g, _ := newTestGateway(t, withReserve(time.Minute))
	if v := g.Devices()[0].Lease; v.Holder != "" || v.ReservedFor != "" || len(v.Waiting) != 0 {
		queueFail(t, "a free phone should have an empty view: %+v", v)
	}
	a := connectAs(t, g, "worker-a", "first task")
	connectWaiting(named(t, g, "worker-b"), "b", 5*time.Second)
	waitLine(t, g, 1)
	connectWaiting(named(t, g, "worker-c"), "c", 5*time.Second)
	waitLine(t, g, 2)
	v := g.Devices()[0].Lease
	if v.Holder != "worker-a" || v.Task != "first task" || strings.Join(v.Waiting, ",") != "worker-b,worker-c" {
		queueFail(t, "view while held: %+v", v)
	}
	releaseAll(t, a)
	v = g.Devices()[0].Lease
	if v.Holder != "" || v.ReservedFor != "worker-a" || v.ReservedUntil.IsZero() {
		queueFail(t, "view after release: %+v", v)
	}
}
