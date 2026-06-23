// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package console

import (
	"testing"
)

func TestReservationExclusive(t *testing.T) {
	sessions := newInteractiveSessions()
	nodeID := "x0c0s1b0n0"

	if ok := sessions.reserve(nodeID); !ok {
		t.Fatal("expected first reservation to succeed")
	}

	if ok := sessions.reserve(nodeID); ok {
		t.Fatal("expected second reservation for same node to fail")
	}

	if ok := sessions.reserve("x0c0s2b0n0"); !ok {
		t.Fatal("expected reservation for a different node to succeed")
	}

	sessions.release(nodeID)
	if ok := sessions.reserve(nodeID); !ok {
		t.Fatal("expected reservation after release to succeed")
	}
}
