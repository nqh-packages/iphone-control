//go:build !darwin && !linux

package main

import "errors"

// echoOff has no terminal control on this system: set reads stdin as is.
func echoOff(int) (func(), error) { return nil, errors.New("no terminal control") }
