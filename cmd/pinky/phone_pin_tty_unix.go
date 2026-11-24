//go:build darwin || linux

package main

import "golang.org/x/sys/unix"

// pinFileOpenFlags keep set from following a symlink or waiting on a pipe.
const pinFileOpenFlags = unix.O_NOFOLLOW | unix.O_NONBLOCK

// echoOff turns the terminal's echo off and returns the call that puts it
// back; it fails when fd is not a terminal.
func echoOff(fd int) (restore func(), err error) {
	old, err := unix.IoctlGetTermios(fd, termiosGet)
	if err != nil {
		return nil, err
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, termiosSet, &quiet); err != nil {
		return nil, err
	}
	return func() { unix.IoctlSetTermios(fd, termiosSet, old) }, nil
}
