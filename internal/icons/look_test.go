package icons

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

func TestLookBudgetAndFailures(t *testing.T) {
	shorten(t, timing{start: 20 * time.Second, hang: 1500 * time.Millisecond, firstRetry: 100 * time.Millisecond, lastRetry: 200 * time.Millisecond, healthy: time.Minute})
	ctx := context.Background()
	pic := picture(t, 64, 128)
	t.Cleanup(shutdown)
	log, said := testLog()

	if f := Look(ctx, pic); f.Read != screen.IconsNotLooked {
		t.Fatalf("with no detector in use: %+v", f)
	}
	home := t.TempDir()
	if s := Use(home, log); s.Installed {
		t.Fatalf("an empty home: %v", s)
	}
	if f := Look(ctx, pic); f.Read != screen.IconsNotLooked {
		t.Fatalf("after a failed Use: %+v", f)
	}

	f := newFixture(t, "https://example.com")
	dir := f.place(t, home)
	behave := func(how string) { write(t, filepath.Join(dir, "behaviour"), []byte(how)) }
	if s := Use(home, log); !s.Installed || s.ID != "fake-v1" {
		t.Fatalf("use: %v", s)
	}
	first := current.Load()
	if Use(home, log); current.Load() != first {
		t.Fatal("a second Use started another detector")
	}
	looks := func(what string) Found {
		t.Helper()
		for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if got := Look(ctx, pic); got.Read == screen.IconsLooked {
				return got
			}
		}
		t.Fatalf("%s: the detector never answered\n%s", what, said)
		return Found{}
	}
	got := looks("start")
	if len(got.Boxes) != 2 || got.Boxes[0].Label != "close" || got.Boxes[1].Label != "scoped" ||
		got.Orientation != "portrait" || got.Overlap != 0.45 {
		t.Fatalf("found %+v", got)
	}
	if l := Look(ctx, picture(t, 128, 64)); l.Read != screen.IconsNotLooked {
		t.Fatalf("a landscape picture: %+v", l)
	}
	late := func(what string) time.Duration {
		t.Helper()
		start := time.Now()
		got := Look(ctx, pic)
		took := time.Since(start)
		if got.Read != screen.IconsLate || len(got.Boxes) != 0 {
			t.Fatalf("%s: %+v", what, got)
		}
		if took > lookBudget+30*time.Millisecond {
			t.Fatalf("%s: a look took %v", what, took)
		}
		return took
	}

	behave("sleep 800ms")
	if took := late("slow"); took < lookBudget {
		t.Fatalf("a slow look gave up after %v", took)
	}
	if took := late("busy"); took > 100*time.Millisecond {
		t.Fatalf("a look at a busy detector waited %v", took)
	}
	behave("ok")
	pid := func() int { first.mu.Lock(); defer first.mu.Unlock(); return first.pid }
	before := pid()
	looks("after slow")
	if pid() != before {
		t.Fatal("a slow answer restarted the detector")
	}

	behave("error")
	late("error")
	behave("ok")
	looks("after an error")

	behave("crash")
	late("crash")
	behave("ok")
	looks("after a crash")
	if pid() == before || !strings.Contains(said.String(), "exit status 3") {
		t.Fatalf("after a crash:\n%s", said)
	}

	before = pid()
	behave("hang")
	late("hang")
	for end := time.Now().Add(10 * time.Second); !strings.Contains(said.String(), "no answer in"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("a hung detector was not killed:\n%s", said)
		}
	}
	behave("ok")
	looks("after a hang")
	if pid() == before {
		t.Fatal("the hung detector still runs")
	}

	done, cancel := context.WithCancel(ctx)
	cancel()
	behave("sleep 200ms")
	start := time.Now()
	if l := Look(done, pic); l.Read != screen.IconsLate || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("an ended context: %+v after %v", l, time.Since(start))
	}
	behave("ok")
	looks("after a cancelled look")

	verdicts, took, err := Explain(ctx, pic, "")
	if err != nil || took <= 0 || len(verdicts) != 3 {
		t.Fatalf("explain: %v %v %+v", err, took, verdicts)
	}
	var lines []string
	for _, v := range verdicts {
		lines = append(lines, v.Class+" "+v.Why)
		if v.Kept != (v.Why == "") {
			t.Fatalf("kept and why disagree: %+v", v)
		}
	}
	if want := "one_close |one_scoped not for this app (com.example.app)|one_faint below 0.60"; strings.Join(lines, "|") != want {
		t.Fatalf("verdicts %q, want %q", strings.Join(lines, "|"), want)
	}
	verdicts, _, err = Explain(ctx, picture(t, 128, 64), "com.example.app")
	if err != nil || len(verdicts) == 0 || verdicts[0].Kept || !strings.Contains(verdicts[0].Why, "landscape") {
		t.Fatalf("explain landscape: %v %+v", err, verdicts)
	}
	if _, _, err := Explain(ctx, []byte("no picture"), ""); err == nil {
		t.Fatal("explain read a text file")
	}

	shutdown()
	if l := Look(ctx, pic); l.Read != screen.IconsNotLooked {
		t.Fatalf("after a stop: %+v", l)
	}
	write(t, filepath.Join(dir, "model.onnx"), []byte(strings.ToUpper(string(f.model))))
	log, said = testLog()
	if s := Use(home, log); !s.Installed {
		t.Fatalf("sizes match: %v", s)
	}
	for end := time.Now().Add(20 * time.Second); !strings.Contains(said.String(), "model.onnx: sha256 differs"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the log does not name the changed file:\n%s", said)
		}
	}
	late("a changed file")
	waiting, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	if _, _, err := Explain(waiting, pic, ""); err == nil || !strings.Contains(err.Error(), "model.onnx") {
		t.Fatalf("explain with a changed file: %v", err)
	}
}

func TestLookDetectorEndsWithItsInput(t *testing.T) {
	dir := newFixture(t, "https://example.com").place(t, t.TempDir())
	p, err := startProcess(dir, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	m, err := load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ready(20*time.Second, m); err != nil {
		t.Fatal(err)
	}
	p.in.Close()
	select {
	case _, open := <-p.frames:
		if open {
			t.Fatal("it spoke instead of ending")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the detector outlived its input")
	}
	if err := p.cmd.Wait(); err != nil {
		t.Fatalf("it ended with %v", err)
	}
}
