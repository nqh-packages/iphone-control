package vault

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	headerMax  = 4096
	valueMax   = 1024
	pathMax    = 103
	ioDeadline = 10 * time.Second
)

type header struct {
	App   string   `json:"app"`
	TTLMS int64    `json:"ttl_ms,omitempty"`
	Names []string `json:"names"`
}

type reply struct {
	Staged
	Error string `json:"error,omitempty"`
	Fix   string `json:"fix,omitempty"`
}

// Staged is what a stager is told after Deposit: the names kept, the app they
// are for and how long they last. It never holds a value.
type Staged struct {
	Names      []string `json:"staged"`
	App        string   `json:"app"`
	ExpiresInS int      `json:"expires_in_s"`
}

// ErrInput is returned by StageSecret when the lines it was given do not fit
// the names: a value missing, empty or too long.
var ErrInput = errors.New("one line of input is needed for each name, and none may be empty or longer than 1024 bytes")

// StageError is a refusal the daemon sent back, with its fix line.
type StageError struct{ Msg, Fix string }

func (e *StageError) Error() string { return e.Msg + ". fix: " + e.Fix }

// Listen listens on a Unix socket at path, readable only by the user. The
// folder it lives in must be the user's own and closed to others: that, and
// the peer check in Serve, are the only guards.
func Listen(path string) (net.Listener, error) {
	if len(path) > pathMax {
		return nil, fmt.Errorf("the vault socket path %s is %d bytes, longer than the %d this system allows: use a shorter PINKY_HOME", path, len(path), pathMax)
	}
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("the folder of the vault socket %s is open to other users; it must be mode 0700", filepath.Dir(path))
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve answers staging requests on ln until ctx ends or ln closes: one
// request per connection, from this user only. It logs nothing.
func (v *Vault) Serve(ctx context.Context, ln net.Listener) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.Close()
			v.handle(c)
		}()
	}
}

func (v *Vault) handle(c net.Conn) {
	if uc, ok := c.(*net.UnixConn); ok {
		if uid, err := peerUID(uc); err != nil || uid != os.Getuid() {
			return
		}
	}
	c.SetDeadline(time.Now().Add(ioDeadline))
	br := bufio.NewReaderSize(c, headerMax)
	out := json.NewEncoder(c)
	fail := func(msg, fix string) { out.Encode(reply{Error: msg, Fix: fix}) }

	line, err := readLine(br, headerMax)
	var h header
	if err != nil || json.Unmarshal(line, &h) != nil || len(h.Names) == 0 || len(h.Names) > maxSlots {
		fail("the request is not a staging request", "use pinkyctl phone secret put")
		return
	}
	values := make(map[string][]byte, len(h.Names))
	defer func() {
		for _, b := range values {
			wipe(b)
		}
	}()
	for _, name := range h.Names {
		val, err := readLine(br, valueMax)
		if err != nil {
			fail("a value is missing or too long", "give one line per name, at most 1024 bytes each")
			return
		}
		values[name] = val
	}
	if len(values) != len(h.Names) {
		fail("a name was given twice", "give each name once")
		return
	}
	ttl := ClampTTL(time.Duration(h.TTLMS) * time.Millisecond)
	if err := v.Deposit(h.App, ttl, values); err != nil {
		fix := "check the name and the value"
		switch {
		case errors.Is(err, ErrAppNotAllowed):
			fix = "the owner adds " + h.App + " to credential_apps in rules.yaml"
		case errors.Is(err, ErrNameInUse):
			fix = "use another name, or wait until the staged secret is used or expires"
		}
		fail(err.Error(), fix)
		return
	}
	out.Encode(reply{Staged: Staged{Names: h.Names, App: h.App, ExpiresInS: int(ttl / time.Second)}})
}

func readLine(br *bufio.Reader, max int) ([]byte, error) {
	line, err := br.ReadSlice('\n')
	switch {
	case errors.Is(err, bufio.ErrBufferFull):
		return nil, errors.New("line too long")
	case err != nil && !(errors.Is(err, io.EOF) && len(line) > 0):
		return nil, err
	}
	line = bytes.TrimRight(line, "\r\n")
	if len(line) > max {
		return nil, errors.New("line too long")
	}
	return append([]byte(nil), line...), nil
}

// StageSecret reads one line from values for each name, with no trailing
// newline kept, and stages them for app through the vault socket at sock.
// The values are wiped after sending. A refusal comes back as *StageError.
func StageSecret(ctx context.Context, sock, app string, ttl time.Duration, names []string, values io.Reader) (Staged, error) {
	br := bufio.NewReaderSize(values, valueMax+2)
	lines := make([][]byte, 0, len(names))
	defer func() {
		for _, l := range lines {
			wipe(l)
		}
	}()
	for range names {
		l, err := readLine(br, valueMax)
		if err != nil {
			return Staged{}, ErrInput
		}
		lines = append(lines, l)
	}
	for _, l := range lines {
		if len(l) == 0 {
			return Staged{}, ErrInput
		}
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return Staged{}, fmt.Errorf("the daemon's vault socket %s is not there: %w", sock, err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(ioDeadline))
	hdr, _ := json.Marshal(header{App: app, TTLMS: ttl.Milliseconds(), Names: names})
	var frame bytes.Buffer
	frame.Write(hdr)
	frame.WriteByte('\n')
	for _, l := range lines {
		frame.Write(l)
		frame.WriteByte('\n')
	}
	_, err = c.Write(frame.Bytes())
	wipe(frame.Bytes())
	if err != nil {
		return Staged{}, err
	}
	var r reply
	if err := json.NewDecoder(c).Decode(&r); err != nil {
		return Staged{}, errors.New("the daemon did not answer")
	}
	if r.Error != "" {
		return Staged{}, &StageError{Msg: r.Error, Fix: r.Fix}
	}
	return r.Staged, nil
}
