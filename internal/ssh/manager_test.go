// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	compcredentials "github.com/Cray-HPE/hms-compcredentials"
	gossh "golang.org/x/crypto/ssh"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
	"github.com/OpenCHAMI/remote-console/internal/ssh"
)

// deadAddr returns an unused local address so connections fail immediately.
func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// TestWriteWhileDisconnectedReportsNotConnected verifies disconnected writes report no bytes.
// This prevents discarded input from appearing delivered. ErrNotConnected remains distinct from
// unmanaged node errors because reconnects are temporary.
func TestWriteWhileDisconnectedReportsNotConnected(t *testing.T) {
	id, nodeMap, passwords := singleNode(t, deadAddr(t))

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startNodes(t, manager, ctx, nodeMap, passwords)

	n, err := manager.Write(id, []byte("into the void\n"))
	if !errors.Is(err, ssh.ErrNotConnected) {
		t.Fatalf("Write to a disconnected node = (%d, %v), want ErrNotConnected", n, err)
	}
	if n != 0 {
		t.Errorf("Write to a disconnected node reported %d bytes written, want 0", n)
	}

	if _, err := manager.Write("no-such-node", []byte("x")); err == nil {
		t.Fatal("Write to an unknown node succeeded")
	} else if errors.Is(err, ssh.ErrNotConnected) {
		t.Error("Write to an unknown node returned ErrNotConnected; a missing node is permanent, not a reconnect")
	}
}

// waitForBytes reads until the expected session input arrives or the timeout expires.
func waitForBytes(t *testing.T, sess *sshSession, want string, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var got strings.Builder
	for {
		select {
		case data := <-sess.recv:
			got.Write(data)
			if strings.Contains(got.String(), want) {
				return
			}
		case <-timer.C:
			t.Fatalf("timeout waiting for %q on session; got %q", want, got.String())
		}
	}
}

// TestUpdateNodesPreservesCredentials verifies membership updates retain active credentials.
// Clearing credentials would drop a healthy session and incorrectly select key authentication.
func TestUpdateNodesPreservesCredentials(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startNodes(t, manager, ctx, nodeMap, passwords)

	// The connected marker confirms stdin is ready before the first write.
	clientCh, err := manager.Attach(id, "creds-client")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Detach(id, "creds-client")

	sess := <-sessionCh
	waitForMarker(t, clientCh, "[Console "+id+" connected at", 15*time.Second)

	if _, err := manager.Write(id, []byte("before\n")); err != nil {
		t.Fatalf("write before update: %v", err)
	}
	waitForBytes(t, sess, "before", 15*time.Second)

	if err := manager.UpdateNodes(ctx, nodeMap); err != nil {
		t.Fatal(err)
	}

	// The original session must still be live. If the credentials had been
	// cleared, UpdateCreds would have torn this connection down.
	select {
	case <-sessionCh:
		t.Fatal("node reconnected after a membership-only update; UpdateNodes touched its credentials")
	case <-time.After(2 * time.Second):
	}

	if _, err := manager.Write(id, []byte("after\n")); err != nil {
		t.Fatalf("write after update: %v", err)
	}
	waitForBytes(t, sess, "after", 15*time.Second)
}

// TestUpdateNodesAddsAndRemovesNodes verifies membership changes do not depend on credential
// delivery. Inventory updates must proceed even when credentials are unavailable.
func TestUpdateNodesAddsAndRemovesNodes(t *testing.T) {
	srv := newSSHServer(t, func(ch gossh.Channel) { _ = ch.Close() })
	id, nodeMap, _ := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register the node before credentials arrive.
	if err := manager.UpdateNodes(ctx, nodeMap); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Attach(id, "probe"); err != nil {
		t.Fatalf("node was not registered by UpdateNodes: %v", err)
	}
	manager.Detach(id, "probe")

	if err := manager.UpdateNodes(ctx, map[string]*nodes.NodeConsoleInfo{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Attach(id, "probe"); err == nil {
		t.Fatal("node still registered after UpdateNodes dropped it")
	}
}

// TestUpdateCredentialsIgnoresAbsentNodes pins the property that lets callers
// pass a credential map they cannot vouch for. GetPasswordsWithRetries returns
// whatever it managed to fetch with a nil error once it exhausts its retries,
// so an absent nodeID says nothing about the node — only that this fetch did
// not bring it back. UpdateCredentials must therefore leave absent nodes alone:
// treating absence as a change would tear down a working console every time the
// credential store had a bad minute.
func TestUpdateCredentialsIgnoresAbsentNodes(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startNodes(t, manager, ctx, nodeMap, passwords)

	clientCh, err := manager.Attach(id, "absent-client")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Detach(id, "absent-client")

	sess := <-sessionCh
	waitForMarker(t, clientCh, "[Console "+id+" connected at", 15*time.Second)

	// An empty map — every node absent. Nothing may happen to this connection.
	manager.UpdateCredentials(map[string]compcredentials.CompCredentials{})

	select {
	case <-sessionCh:
		t.Fatal("node reconnected after an update that did not mention it; absence was read as a credential change")
	case <-time.After(2 * time.Second):
	}

	// Still the same live session, not a silently re-established one.
	if _, err := manager.Write(id, []byte("still-here\n")); err != nil {
		t.Fatalf("write after the empty credential update: %v", err)
	}
	waitForBytes(t, sess, "still-here", 15*time.Second)
}

// waitForLogMarker polls a node log until it contains marker. Logs retain markers emitted before
// clients can attach, which avoids attachment timing races.
func waitForLogMarker(t *testing.T, logsDir, nodeID, marker string, timeout time.Duration) {
	t.Helper()
	path := filepath.Join(logsDir, "console."+nodeID)
	deadline := time.Now().Add(timeout)
	var last []byte
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			last = data
			if strings.Contains(string(data), marker) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %q in %s; log contains: %q", marker, path, string(last))
}

// TestNodeWithoutCredentialsWaitsInsteadOfDialling verifies provisioning nodes wait for a
// credential entry. Every node requires a username, even for key authentication, so dialing
// before the entry arrives would report a misleading authentication failure.
func TestNodeWithoutCredentialsWaitsInsteadOfDialling(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	logsDir := t.TempDir()
	manager := newManager(t, logsDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register membership without credentials.
	if err := manager.UpdateNodes(ctx, nodeMap); err != nil {
		t.Fatal(err)
	}

	waitForLogMarker(t, logsDir, id, "waiting for credentials", 10*time.Second)

	// Wait beyond several reconnect intervals to detect an unintended dial.
	time.Sleep(3 * time.Second)

	if got := srv.accepts.Load(); got != 0 {
		t.Errorf("node made %d connection attempt(s) with no credentials, want 0", got)
	}
	select {
	case <-sessionCh:
		t.Fatal("node established a session before it had any credentials")
	default:
	}

	// Credential delivery must wake the node without waiting for a timer.
	manager.UpdateCredentials(passwords)

	select {
	case <-sessionCh:
	case <-time.After(15 * time.Second):
		t.Fatal("node did not connect after its credentials arrived")
	}
}
