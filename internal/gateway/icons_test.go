package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

type iconDev struct {
	*readerDev
	icons []screen.Icon
}

func (d *iconDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, err := d.readerDev.Observe(ctx, opt)
	if err == nil {
		s.SetGuessedIcons(screen.IconsLooked, d.icons)
	}
	return s, err
}

func TestIconsDoNotRelaxPolicy(t *testing.T) {
	ctx := context.Background()
	read := func(icons []screen.Icon) (*Session, *Result, *readerDev, *Gateway) {
		base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
		base.Show("settings")
		reader := &readerDev{Device: base, reads: true}
		reader.text("Pay ¥5,527.07", screen.Rect{X: 100, Y: 900, W: 400, H: 60})
		g := newDevGateway(t, base, &iconDev{readerDev: reader, icons: icons})
		s, res := connect(t, g, "Look at the bill")
		return s, res, reader, g
	}
	_, plain, _, _ := read(nil)
	s, res, reader, g := read([]screen.Icon{{Label: "close", Score: 0.9, Bounds: screen.Rect{X: 280, Y: 910, W: 40, H: 40}}})

	const line = `icon? "close" @300,930`
	shown := res.Screen.Render()
	if !strings.HasSuffix(shown, "\n"+line) {
		t.Fatalf("the agent does not see the guess:\n%s", shown)
	}
	if strings.TrimSuffix(shown, "\n"+line) != plain.Screen.Render() {
		t.Fatalf("a guess changed the elements or their refs:\n%s\n%s", shown, plain.Screen.Render())
	}
	if reader.allReads() != 0 {
		t.Fatalf("%d reads of all the text before the tap", reader.allReads())
	}

	asked := make(chan NeedView, 1)
	go func() {
		n := waitNeed(t, g)
		asked <- n
		g.Decide(n.ID, Deny, "")
	}()
	res = s.Act(ctx, Call{}, Action{Tool: "tap", Point: &screen.Point{X: 300, Y: 930}})
	mustStatus(t, res, StatusDenied)
	if n := <-asked; n.Rule != "Payments" {
		t.Fatalf("asked under rule %q", n.Rule)
	}
	if reader.allReads() == 0 {
		t.Fatal("the text under the finger was not read")
	}
}

func TestALateIconReadIsNotAChangedScreen(t *testing.T) {
	read := &screen.Screen{Width: 375, Height: 667}
	late := *read
	late.SetGuessedIcons(screen.IconsLate, nil)
	if late.Header() == read.Header() {
		t.Fatal("the agent is not told the icons are unread")
	}
	if body(&late) != body(read) {
		t.Fatalf("a wait would end on an unchanged screen:\n%s\n%s", body(&late), body(read))
	}
}
