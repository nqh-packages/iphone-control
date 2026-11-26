package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/icons"
	"github.com/DIYgod/pinky/internal/screen"
)

const iconsHelp = `Usage:
  pinkyctl phone icons [-as NAME] [-wait S] [-task T] [-device D]
  pinkyctl phone icons <picture> [-app BUNDLE_ID] [-all]
  pinkyctl phone icons install <manifest.json>
  pinkyctl phone icons status

An icon detector, installed as data, guesses the icons the element tree does
not name. A screen read shows each as ` + "`" + `icon? "close" @352,40` + "`" + `: no ref, the
label may be wrong, tap it by its point. "icons unread" in a read's header
says the detector did not answer in time.

Without arguments, icons reads the phone now, as phone call observe does, and
prints the header and the icon lines. With a saved picture (JPEG, PNG or BMP)
it writes nothing and prints the guesses a read of -app would keep, with their
scores; -all adds the dropped detections and why.

install fetches what the manifest names into <data folder>/icons, checks every
hash and tries it on a blank picture; restart the daemon afterwards
(bin/iphone-daemon start). status says whether a detector is installed. To
remove it, delete <data folder>/icons (<data folder> is $PINKY_HOME/data under
PINKY_HOME).

Exit status: 0 done; 1 not installed or it failed; 2 usage; 3 no gateway; 4
the phone stayed busy; 5 the person has the phone or must act.

Friction with this command? Report it:
  ~/.agents/skills/agent-feedback/scripts/agent-feedback submit --tool phone-icons --kind friction --goal "..." --happened "..."
`

const iconsInstallFix = "run `pinkyctl phone icons install <manifest>`"

const iconsExplainWait = 30 * time.Second

func phoneIconsCmd(args []string, pio phoneIO) int {
	if len(args) > 0 {
		switch args[0] {
		case "help":
			fmt.Fprint(pio.out, iconsHelp)
			return phoneExitOK
		case "install":
			return phoneIconsInstall(args[1:], pio)
		case "status":
			return phoneIconsStatus(args[1:], pio)
		case "serve":
			return phoneIconsServe(args[1:], pio)
		}
	}
	fs := phoneFlags("icons", pio)
	fs.Usage = func() { fmt.Fprint(pio.err, iconsHelp) }
	app := fs.String("app", "", "the bundle id of the app in the picture")
	all := fs.Bool("all", false, "also print the dropped detections")
	as := fs.String("as", "", "the agent name")
	wait := fs.Float64("wait", 60, "seconds to queue for the phone")
	task := fs.String("task", "", "the task title shown in Pinky")
	device := fs.String("device", "", "the phone, by id or name")
	var named []string
	for {
		if code, ok := phoneParse(fs, args); !ok {
			return code
		}
		if fs.NArg() == 0 {
			break
		}
		named, args = append(named, fs.Arg(0)), fs.Args()[1:]
	}
	var live, saved bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "app", "all":
			saved = true
		default:
			live = true
		}
	})
	switch {
	case len(named) > 1 || len(named) == 1 && live || len(named) == 0 && saved:
		return phoneFail(pio, phoneExitUsage, "icons takes one picture with -app and -all, or no picture to read the phone", "run `pinkyctl phone icons help`")
	case len(named) == 1:
		return phoneIconsPicture(named[0], *app, *all, pio)
	}
	call := []string{"-as", *as, "-wait", fmt.Sprint(*wait), "-task", *task, "-device", *device, "observe"}
	var out bytes.Buffer
	code := phoneCallCmd(call, phoneIO{in: pio.in, out: &out, err: pio.err})
	if code != phoneExitOK {
		io.Copy(pio.out, &out)
		return code
	}
	fmt.Fprint(pio.out, iconLines(out.String()))
	return code
}

func iconLines(read string) string {
	var b strings.Builder
	header := false
	for line := range strings.Lines(read) {
		switch {
		case !header && strings.HasPrefix(line, "screen "):
			header = true
			b.WriteString(line)
		case header && screen.IsIconLine(line):
			b.WriteString(line)
		}
	}
	if !header {
		return read
	}
	return b.String()
}

func iconsDirs(pio phoneIO) (string, int) {
	dirs, err := api.DefaultDirs()
	if err != nil {
		return "", phoneFail(pio, phoneExitToolError, err.Error(), "set PINKY_HOME or HOME")
	}
	return iconsHome(dirs), phoneExitOK
}

func phoneIconsPicture(path, app string, all bool, pio phoneIO) int {
	pic, err := os.ReadFile(path)
	if err != nil {
		return phoneFail(pio, phoneExitUsage, err.Error(), "give the path of a saved screen picture")
	}
	size, _, err := image.DecodeConfig(bytes.NewReader(pic))
	if err != nil {
		return phoneFail(pio, phoneExitUsage, path+" is not a JPEG, PNG or BMP picture", "give the path of a saved screen picture")
	}
	home, code := iconsDirs(pio)
	if code != phoneExitOK {
		return code
	}
	log := slog.New(slog.NewTextHandler(pio.err, &slog.HandlerOptions{Level: slog.LevelError}))
	if s := icons.Use(home, log); !s.Installed {
		return phoneFail(pio, phoneExitToolError, "no icon detector: "+s.Reason, iconsInstallFix)
	}
	ctx, cancel := context.WithTimeout(context.Background(), iconsExplainWait)
	defer cancel()
	verdicts, took, err := icons.Explain(ctx, pic, app)
	if err != nil {
		return phoneFail(pio, phoneExitToolError, "the icon detector failed: "+err.Error(), iconsInstallFix)
	}
	printVerdicts(pio.out, verdicts, size.Width, size.Height, took, all)
	return phoneExitOK
}

func printVerdicts(w io.Writer, verdicts []icons.Verdict, width, height int, took time.Duration, all bool) {
	var kept, dropped []string
	for _, v := range verdicts {
		c := v.Bounds(width, height).Center()
		guess := fmt.Sprintf("%q %.2f @%d,%d", v.Label, v.Score, c.X, c.Y)
		if v.Kept {
			kept = append(kept, fmt.Sprintf("%-34s %-9s (class %s)", "icon? "+guess, "kept", v.Class))
		} else {
			dropped = append(dropped, fmt.Sprintf("%-34s %-9s %s (class %s)", "-     "+guess, "dropped", v.Why, v.Class))
		}
	}
	lines := kept
	if all {
		lines = append(lines, dropped...)
	}
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "icons: %d kept, %d dropped · %d ms\n", len(kept), len(dropped), took.Milliseconds())
}

func phoneIconsInstall(args []string, pio phoneIO) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return phoneFail(pio, phoneExitUsage, "install takes one manifest file", iconsInstallFix)
	}
	home, code := iconsDirs(pio)
	if code != phoneExitOK {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if _, err := icons.Install(ctx, args[0], home, pio.out); err != nil {
		return phoneFail(pio, phoneExitToolError, err.Error(), "check the manifest and the network, then "+iconsInstallFix+" again")
	}
	fmt.Fprintln(pio.out, "restart the daemon to use it (`bin/iphone-daemon start`)")
	return phoneExitOK
}

func phoneIconsStatus(args []string, pio phoneIO) int {
	if len(args) != 0 {
		return phoneFail(pio, phoneExitUsage, "status takes no argument", "run `pinkyctl phone icons status`")
	}
	home, code := iconsDirs(pio)
	if code != phoneExitOK {
		return code
	}
	s := icons.Check(home)
	line, _ := json.Marshal(struct {
		Icons string `json:"icons"`
		ID    string `json:"id,omitempty"`
	}{s.String(), s.ID})
	fmt.Fprintf(pio.out, "%s\n", line)
	return phoneExitOK
}

func phoneIconsServe(args []string, pio phoneIO) int {
	if len(args) != 1 {
		return phoneFail(pio, phoneExitUsage, "serve takes the install's folder; the daemon runs it", "run `pinkyctl phone icons help`")
	}
	if err := icons.Serve(args[0], pio.in, pio.out); err != nil {
		fmt.Fprintln(pio.err, err)
		return phoneExitToolError
	}
	return phoneExitOK
}
