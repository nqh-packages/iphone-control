package icons

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

func cpuTime(t *testing.T, pid int) time.Duration {
	t.Helper()
	b, err := exec.Command("ps", "-o", "time=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var seconds float64
	for part := range strings.SplitSeq(strings.TrimSpace(string(b)), ":") {
		v, err := strconv.ParseFloat(part, 64)
		if err != nil {
			t.Fatalf("ps time %q: %v", b, err)
		}
		seconds = 60*seconds + v
	}
	return time.Duration(seconds * float64(time.Second))
}

func TestRealModel(t *testing.T) {
	if os.Getenv("PINKY_ICONS_REAL") != "1" {
		t.Skip("set PINKY_ICONS_REAL=1 to install and run the real model")
	}
	ctx := context.Background()
	home := t.TempDir()
	if base := os.Getenv("PINKY_HOME"); base != "" {
		home = filepath.Join(base, "data", "icons")
	}
	t.Cleanup(func() { childCommand = helperCommand("fake") })
	childCommand = helperCommand("ort")
	if !Check(home).Installed {
		var out strings.Builder
		if _, err := Install(ctx, committed, home, &out); err != nil {
			t.Fatalf("install: %v\n%s", err, &out)
		}
		t.Logf("install into %s:\n%s", home, &out)
	}

	m, dir, err := active(home)
	if err != nil {
		t.Fatal(err)
	}
	e, err := openORT(filepath.Join(dir, m.runtime().name), filepath.Join(dir, m.model().name), m)
	if err != nil {
		t.Fatal(err)
	}
	_, shape, err := e.run(make([]float32, 3*m.Input.Width*m.Input.Height))
	if err != nil || len(shape) != 3 || shape[0] != 1 || shape[1] != 103 {
		t.Fatalf("answer shape %v: %v", shape, err)
	}
	t.Logf("answer shape %v", shape)

	t.Cleanup(shutdown)
	log, said := testLog()
	if s := Use(home, log); !s.Installed {
		t.Fatal(s)
	}
	waiting, stop := context.WithTimeout(ctx, time.Minute)
	defer stop()
	blank, _, err := Explain(waiting, picture(t, 375, 667), "")
	if err != nil {
		t.Fatalf("%v\n%s", err, said)
	}
	for _, v := range blank {
		if v.Kept {
			t.Errorf("a blank picture shows %+v", v)
		}
	}

	var frames []struct {
		File  string `json:"file"`
		Icons []struct {
			Label      string  `json:"label"`
			Confidence float64 `json:"confidence"`
			Box        []int   `json:"box"`
		} `json:"icons"`
	}
	sample := picture(t, 375, 667)
	if path := os.Getenv("PINKY_ICONS_PARITY"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &frames); err != nil {
			t.Fatal(err)
		}
		if sample, err = os.ReadFile(frames[0].File); err != nil {
			t.Fatal(err)
		}
	}

	var took []time.Duration
	for range 20 {
		start := time.Now()
		if f := Look(ctx, sample); f.Read != screen.IconsLooked {
			t.Errorf("a look was %v after %v", f.Read, time.Since(start))
		}
		took = append(took, time.Since(start))
		time.Sleep(50 * time.Millisecond)
	}
	slices.Sort(took)
	p50, p95 := took[len(took)/2], took[len(took)*95/100-1]
	t.Logf("20 looks through the child: p50 %v p95 %v max %v", p50.Round(time.Millisecond), p95.Round(time.Millisecond), took[len(took)-1].Round(time.Millisecond))
	if p95 >= 250*time.Millisecond {
		t.Errorf("p95 %v is 250 ms or more", p95)
	}

	s := current.Load()
	s.mu.Lock()
	pid := s.pid
	s.mu.Unlock()
	time.Sleep(time.Second)
	before := cpuTime(t, pid)
	time.Sleep(time.Second)
	idle := cpuTime(t, pid) - before
	t.Logf("idle detector (pid %d): %v of CPU time in 1 s", pid, idle)
	if idle > 50*time.Millisecond {
		t.Errorf("the idle detector used %v of CPU in a second", idle)
	}
	sockets, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-i", "-n", "-P").Output()
	if exit, ok := err.(*exec.ExitError); err != nil && (!ok || exit.ExitCode() != 1) {
		t.Fatalf("lsof: %v", err)
	}
	if opened, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid)).Output(); err != nil || !strings.Contains(string(opened), m.runtime().name) {
		t.Fatalf("lsof does not see the detector: %v", err)
	}
	t.Logf("network sockets of the detector: %q", sockets)
	if len(sockets) != 0 {
		t.Errorf("the detector has sockets open:\n%s", sockets)
	}

	if len(frames) == 0 {
		return
	}
	var python, goFound, matched int
	var worst float64
	var misses []string
	for _, f := range frames {
		pic, err := os.ReadFile(f.File)
		if err != nil {
			t.Fatal(err)
		}
		verdicts, _, err := Explain(ctx, pic, "")
		if err != nil {
			t.Fatalf("%s: %v", f.File, err)
		}
		python, goFound = python+len(f.Icons), goFound+len(verdicts)
		used := make([]bool, len(verdicts))
		for _, p := range f.Icons {
			found := false
			for i, v := range verdicts {
				r := v.Bounds(375, 667)
				near := func(a, b int) bool { return math.Abs(float64(a-b)) <= 3 }
				if !used[i] && v.Class == p.Label && near(r.X, p.Box[0]) && near(r.Y, p.Box[1]) && near(r.X+r.W, p.Box[2]) && near(r.Y+r.H, p.Box[3]) {
					used[i], found = true, true
					matched++
					worst = math.Max(worst, math.Abs(float64(v.Score)-p.Confidence))
					break
				}
			}
			if !found {
				misses = append(misses, filepath.Base(f.File)+" "+p.Label+" "+strconv.FormatFloat(p.Confidence, 'f', 3, 64))
			}
		}
	}
	rate := float64(matched) / float64(python)
	t.Logf("parity: %d frames, python %d, go %d, matched %d (%.1f%%) by class and box within 3 px, max score difference %.3f", len(frames), python, goFound, matched, 100*rate, worst)
	t.Logf("unmatched python detections: %q", misses)
	if rate < 0.95 {
		t.Errorf("only %.1f%% of the detections match", 100*rate)
	}
}
