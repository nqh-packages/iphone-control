package ios

import (
	"context"
	"fmt"
	"net"

	goios "github.com/danielpaulus/go-ios/ios"
)

type wdaDialFunc func(ctx context.Context, port int) (net.Conn, error)

func usbmuxDial(e goios.DeviceEntry) wdaDialFunc {
	return func(ctx context.Context, port int) (net.Conn, error) {
		return dialDevicePort(ctx, e.DeviceID, port)
	}
}

func otherTransport(e goios.DeviceEntry) (wdaDialFunc, bool) {
	list, err := goios.ListDevices()
	if err != nil {
		return nil, false
	}
	for _, o := range list.DeviceList {
		if o.Properties.SerialNumber == e.Properties.SerialNumber &&
			o.Properties.ConnectionType != e.Properties.ConnectionType {
			return usbmuxDial(o), true
		}
	}
	return nil, false
}

func (c *wdaClient) dialed() {
	c.mu.Lock()
	c.fails.Store(0)
	c.swaps = 0
	c.mu.Unlock()
}

func (c *wdaClient) dropSession() {
	c.mu.Lock()
	c.sid, c.depth = "", 0
	c.mu.Unlock()
}

func (c *wdaClient) recoverDial(ctx context.Context) {
	c.dropSession()
	c.mu.Lock()
	swap := c.fails.Add(1) >= 2 && c.swaps == 0 && c.other != nil
	c.mu.Unlock()
	if !swap {
		return
	}
	if dial, ok := c.other(ctx); ok {
		c.dialer.Store(&dial)
		c.mu.Lock()
		c.swaps++
		c.mu.Unlock()
		c.hc.CloseIdleConnections()
	}
}

func (c *wdaClient) dialFailed(err error, retried bool) error {
	c.dropSession()
	c.mu.Lock()
	c.fails.Add(1)
	swapped := c.swaps > 0
	c.mu.Unlock()
	tried := "the helper's session was not restarted, because the request may already have been sent"
	if retried {
		tried = "the helper's session was restarted and the request retried once"
		if swapped {
			tried += ", and the other connection (USB or Wi-Fi) was tried"
		}
	}
	return fmt.Errorf("%w; fix: %s; if it stays, restart the daemon or reinstall the helper (pinky doctor)", err, tried)
}

const dialFailsGone = 3

func (c *wdaClient) dialFails() int { return int(c.fails.Load()) }
