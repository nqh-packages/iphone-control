//go:build !darwin && !linux

package vault

import (
	"net"
	"os"
)

func peerUID(*net.UnixConn) (int, error) { return os.Getuid(), nil }
