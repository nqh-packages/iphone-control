package ios

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

func recoverServer(t *testing.T, sessions *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		n := sessions.Add(1)
		w.Write([]byte(`{"value":{"sessionId":"s` + string(rune('0'+n)) + `"},"sessionId":"s` + string(rune('0'+n)) + `"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"value":{"scale":3}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func dialAfter(srv *httptest.Server, failures *atomic.Int32, left int32) wdaDialFunc {
	failures.Store(left)
	return func(ctx context.Context, port int) (net.Conn, error) {
		if failures.Add(-1) >= 0 {
			return nil, errors.New("Failed connecting to service, error code:3")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}
}

func TestWDAReadAfterDialErrorRestartsHelperSessionAndSucceeds_connectToPort8100Failed(t *testing.T) {
	var sessions, failures atomic.Int32
	srv := recoverServer(t, &sessions)
	c := newWDADial(dialAfter(srv, &failures, 1))
	if _, err := c.screenInfo(context.Background()); err != nil {
		t.Fatalf("read after one dial error: %v", err)
	}
	if sessions.Load() != 1 {
		t.Fatalf("sessions created = %d, want 1 fresh one", sessions.Load())
	}
}

func TestWDAActionAfterDialErrorIsNotRetried(t *testing.T) {
	var sessions, failures atomic.Int32
	srv := recoverServer(t, &sessions)
	c := newWDADial(dialAfter(srv, &failures, 0))
	if err := c.do(context.Background(), c.hc, http.MethodPost, "/wda/tap", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	c.hc.CloseIdleConnections()
	failures.Store(1)
	err := c.do(context.Background(), c.hc, http.MethodPost, "/wda/tap", map[string]any{}, nil)
	var de *wdaDialError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want the dial error", err)
	}
	if failures.Load() != 0 {
		t.Fatalf("dial attempts after the failure = %d, an action must not be retried", 1-failures.Load())
	}
	if !strings.Contains(err.Error(), "fix:") || !strings.Contains(err.Error(), "may already have been sent") {
		t.Fatalf("err lacks the fix line: %v", err)
	}
}

func TestWDASecondDialFailureCarriesFixLine(t *testing.T) {
	var sessions, failures atomic.Int32
	srv := recoverServer(t, &sessions)
	c := newWDADial(dialAfter(srv, &failures, 100))
	_, err := c.screenInfo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "fix:") || !strings.Contains(err.Error(), "restarted and the request retried once") {
		t.Fatalf("err = %v", err)
	}
	if got := 100 - failures.Load(); got != 2 {
		t.Fatalf("dials = %d, want 2 (one try and one retry)", got)
	}
}

func TestWDARepeatedFailureTriesOtherTransportOnce(t *testing.T) {
	var sessions, failures, other atomic.Int32
	srv := recoverServer(t, &sessions)
	c := newWDADial(dialAfter(srv, &failures, 100))
	c.other = func(ctx context.Context) (wdaDialFunc, bool) {
		other.Add(1)
		return func(ctx context.Context, port int) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
		}, true
	}
	if _, err := c.screenInfo(context.Background()); err == nil {
		t.Fatal("first call should fail on the dead transport")
	}
	if other.Load() != 0 {
		t.Fatal("other transport tried after one failed call")
	}
	if _, err := c.screenInfo(context.Background()); err != nil {
		t.Fatalf("second call on the other transport: %v", err)
	}
	if other.Load() != 1 {
		t.Fatalf("other transport lookups = %d, want 1", other.Load())
	}
}

func TestGoneAfterRepeatedDialFailsWhileTheRunnerLives(t *testing.T) {
	d := &WDADevice{c: newWDAURL("http://127.0.0.1:1", ""), run: &wdaRun{done: make(chan struct{})}}
	err := &wdaDialError{errors.New("connect to port 8100 on the phone")}
	if got := d.gone(err); errors.Is(got, driver.ErrOffline) {
		t.Fatalf("offline after no failures: %v", got)
	}
	for i := 0; i < dialFailsGone; i++ {
		d.c.recoverDial(context.Background())
	}
	if got := d.gone(err); !errors.Is(got, driver.ErrOffline) {
		t.Fatalf("not offline after %d dial failures: %v", dialFailsGone, got)
	}
}

func TestSessionDialFailureAnswersWhileTheRunnerLives(t *testing.T) {
	var sessions, failures atomic.Int32
	srv := recoverServer(t, &sessions)
	c := newWDADial(dialAfter(srv, &failures, 100))
	d := &WDADevice{c: c, run: &wdaRun{done: make(chan struct{})}}
	c.gone = d.gone
	answered := make(chan error, 1)
	go func() {
		var err error
		for range dialFailsGone + 1 {
			_, err = c.screenshot(context.Background())
		}
		answered <- err
	}()
	select {
	case err := <-answered:
		if !errors.Is(err, driver.ErrOffline) {
			t.Fatalf("not offline after repeated dial failures: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a request that could not open the helper's session never returned")
	}
}
