// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package console

import "errors"

// errNotConnected means the backend is temporarily disconnected.
// Callers should drop the input and keep the session open while it reconnects.
var errNotConnected = errors.New("console backend not connected")

// consoleIO provides I/O for PTY, conman, and SSH backends.
type consoleIO interface {
	// Output returns console output and closes on permanent shutdown.
	Output() <-chan []byte
	// Write sends input to the console. It returns errNotConnected without
	// writing while reconnecting. Check wrapped errors with errors.Is.
	Write(p []byte) (n int, err error)
	// Close shuts down the backend. Safe to call multiple times.
	Close() error
}
