package icons

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type timing struct {
	start, hang, firstRetry, lastRetry, healthy time.Duration
}

var timings = timing{start: 30 * time.Second, hang: 3 * time.Second, firstRetry: time.Second, lastRetry: time.Minute, healthy: time.Minute}

var childCommand = func(dir string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, "phone", "icons", "serve", dir), nil
}

type process struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	frames chan []byte
}

func startProcess(dir string, stderr io.Writer) (*process, error) {
	cmd, err := childCommand(dir)
	if err != nil {
		return nil, err
	}
	cmd.Stderr, cmd.WaitDelay = stderr, time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the detector: %w", err)
	}
	p := &process{cmd: cmd, in: in, frames: make(chan []byte)}
	go func() {
		defer close(p.frames)
		for {
			b, err := readFrame(out, maxReply)
			if err != nil {
				return
			}
			p.frames <- b
		}
	}()
	return p, nil
}

func (p *process) end() error {
	p.in.Close()
	p.cmd.Process.Kill()
	for range p.frames {
	}
	return p.cmd.Wait()
}

func (p *process) answer(within time.Duration, send func() error, into any) error {
	var hung atomic.Bool
	timer := time.AfterFunc(within, func() {
		hung.Store(true)
		p.cmd.Process.Kill()
	})
	defer timer.Stop()
	err := send()
	var b []byte
	if err == nil {
		var open bool
		if b, open = <-p.frames; !open {
			err = errors.New("the detector ended")
		}
	}
	switch {
	case hung.Load():
		return fmt.Errorf("the detector gave no answer in %v and was killed", within)
	case err != nil:
		return err
	}
	return json.Unmarshal(b, into)
}

func (p *process) ready(within time.Duration, m *manifest) error {
	var h hello
	if err := p.answer(within, func() error { return nil }, &h); err != nil {
		return err
	}
	switch {
	case !h.Ready:
		return errors.New(h.Reason)
	case h.Classes != len(m.Classes):
		return fmt.Errorf("the detector has %d classes, the manifest %d", h.Classes, len(m.Classes))
	}
	return nil
}

func (p *process) ask(seq uint32, pic []byte, within time.Duration) (reply, error) {
	var r reply
	err := p.answer(within, func() error {
		return writeFrame(p.in, binary.BigEndian.AppendUint32(nil, seq), pic)
	}, &r)
	if err == nil && r.Seq != seq {
		err = fmt.Errorf("the detector answered request %d to request %d", r.Seq, seq)
	}
	return r, err
}

type request struct {
	pic  []byte
	done chan outcome
}

type outcome struct {
	found []detection
	err   error
}

type supervisor struct {
	home, dir string
	m         *manifest
	log       *slog.Logger
	timing    timing
	requests  chan *request
	cancel    context.CancelFunc
	stopped   chan struct{}

	mu      sync.Mutex
	free    bool
	failure error
	changed chan struct{}
	pid     int
}

func startSupervisor(home, dir string, m *manifest, log *slog.Logger) *supervisor {
	ctx, cancel := context.WithCancel(context.Background())
	s := &supervisor{home: home, dir: dir, m: m, log: log, timing: timings, requests: make(chan *request, 1),
		cancel: cancel, stopped: make(chan struct{}), changed: make(chan struct{})}
	go s.run(ctx)
	return s
}

func (s *supervisor) set(free bool, failure error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.free, s.failure = free, failure
	if !free {
		select {
		case r := <-s.requests:
			r.done <- outcome{err: cmp.Or(failure, errors.New("the detector is not ready"))}
		default:
		}
	}
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *supervisor) offer(r *request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.free {
		return false
	}
	s.free = false
	s.requests <- r
	return true
}

func (s *supervisor) offerWhenFree(ctx context.Context, r *request) error {
	for {
		s.mu.Lock()
		changed, failure := s.changed, s.failure
		s.mu.Unlock()
		if s.offer(r) {
			return nil
		}
		if failure != nil {
			return failure
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return fmt.Errorf("the detector did not get ready: %w", ctx.Err())
		}
	}
}

func (s *supervisor) run(ctx context.Context) {
	defer close(s.stopped)
	retry := s.timing.firstRetry
	for ctx.Err() == nil {
		healthy, err := s.session(ctx)
		if ctx.Err() != nil {
			break
		}
		if healthy >= s.timing.healthy {
			retry = s.timing.firstRetry
		}
		s.set(false, err)
		s.log.Warn("icons: the detector stopped", "err", err, "retry_in", retry)
		select {
		case <-time.After(retry):
		case <-ctx.Done():
		}
		retry = min(2*retry, s.timing.lastRetry)
	}
	s.set(false, errors.New("the detector was stopped"))
}

func (s *supervisor) session(ctx context.Context) (healthy time.Duration, err error) {
	p, err := startProcess(s.dir, &limitedLog{log: s.log})
	if err != nil {
		return 0, err
	}
	var readyAt time.Time
	unwatch := context.AfterFunc(ctx, func() { p.cmd.Process.Kill() })
	defer func() {
		unwatch()
		if exit := p.end(); exit != nil && err != nil {
			err = fmt.Errorf("%w (%v)", err, exit)
		}
		if !readyAt.IsZero() {
			healthy = time.Since(readyAt)
		}
	}()
	if err := p.ready(s.timing.start, s.m); err != nil {
		return 0, err
	}
	readyAt = time.Now()
	s.mu.Lock()
	s.pid = p.cmd.Process.Pid
	s.mu.Unlock()
	s.set(true, nil)
	for seq := uint32(1); ; seq++ {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-p.frames:
			return 0, errors.New("the detector ended or spoke unasked")
		case r := <-s.requests:
			a, err := p.ask(seq, r.pic, s.timing.hang)
			r.pic = nil
			if err != nil {
				r.done <- outcome{err: err}
				return 0, err
			}
			s.set(true, nil)
			if a.Error != "" {
				s.log.Warn("icons: the detector could not read a picture", "err", a.Error)
				r.done <- outcome{err: errors.New(a.Error)}
				continue
			}
			r.done <- outcome{found: a.Found}
		}
	}
}

func (s *supervisor) stop() {
	s.cancel()
	<-s.stopped
}

type limitedLog struct {
	log   *slog.Logger
	since time.Time
	lines int
}

const linesPerMinute = 20

func (l *limitedLog) Write(b []byte) (int, error) {
	for line := range bytes.Lines(b) {
		text := strings.TrimSpace(string(line))
		if text == "" {
			continue
		}
		if time.Since(l.since) >= time.Minute {
			l.since, l.lines = time.Now(), 0
		}
		if l.lines++; l.lines <= linesPerMinute {
			l.log.Info("icons: detector", "says", text)
		}
	}
	return len(b), nil
}

var (
	useMu   sync.Mutex
	current atomic.Pointer[supervisor]
)

// Use makes home's detector the process's: it starts the detector process
// at once and restarts it when it ends. A second Use with the same home
// changes nothing. Without a sound install nothing starts.
func Use(home string, log *slog.Logger) Status {
	useMu.Lock()
	defer useMu.Unlock()
	if s := current.Load(); s != nil && s.home == home {
		return Status{Installed: true, ID: s.m.ID}
	}
	m, dir, err := active(home)
	if err != nil {
		return Status{Reason: err.Error()}
	}
	if old := current.Swap(startSupervisor(home, dir, m, log)); old != nil {
		old.stop()
	}
	return Status{Installed: true, ID: m.ID}
}
