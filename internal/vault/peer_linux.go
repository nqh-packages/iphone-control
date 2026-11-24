//go:build linux

package vault

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid := -1
	var serr error
	err = raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if serr == nil {
			uid = int(cred.Uid)
		}
	})
	if err != nil {
		return -1, err
	}
	return uid, serr
}
