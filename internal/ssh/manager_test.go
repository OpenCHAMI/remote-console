// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	compcredentials "github.com/Cray-HPE/hms-compcredentials"
	gossh "golang.org/x/crypto/ssh"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
	"github.com/OpenCHAMI/remote-console/internal/ssh"
)

// deadAddr returns a host:port that nothing is listening on, so connects to it
// are refused immediately.
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

// TestWriteWhileDisconnectedReportsNotConnected pins the Write contract. A
// managed node that has no live session must say so instead of returning
// len(p), nil — that claimed the input had been delivered when it had been
// thrown away, and the interactive session had no way to tell the difference.
// The error is distinct from the not-found error because a caller should keep
// going through a reconnect but not through a node that no longer exists.
func TestWriteWhileDisconnectedReportsNotConnected(t *testing.T) {
	id, nodeMap, passwords := singleNode(t, deadAddr(t))

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := manager.UpdateNodesAndCredentials(ctx, nodeMap, passwords); err != nil {
		t.Fatal(err)
	}

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

// waitForBytes reads from a session's recv channel until want appears or the
// timeout expires.
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

// TestUpdateNodesPreservesCredentials covers the case where the service's
// credential fetch failed and it falls back to a credential-preserving
// update. A
// healthy node must keep running on the credentials it already has. Applying
// the empty password would trigger UpdateCreds, drop the connection, and then
// fall through to key auth, which is not what this node uses.
func TestUpdateNodesPreservesCredentials(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := manager.UpdateNodesAndCredentials(ctx, nodeMap, passwords); err != nil {
		t.Fatal(err)
	}

	// Attach before the first connect completes. node.Write silently drops and
	// reports success while stdin is nil, so writing before the node is fully
	// wired up would race; the connected marker is broadcast after stdin is
	// set, which makes it a reliable sync point.
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

	// Same node set, credentials incomplete — what the service does when the
	// fetch fails.
	if err := manager.UpdateNodes(ctx, nodeMap); err != nil {
		t.Fatal(err)
	}

	// The original session must still be live. If the credentials had been
	// cleared, UpdateCreds would have torn this connection down.
	select {
	case <-sessionCh:
		t.Fatal("node reconnected after a credential-preserving update; existing credentials were not preserved")
	case <-time.After(2 * time.Second):
	}

	if _, err := manager.Write(id, []byte("after\n")); err != nil {
		t.Fatalf("write after update: %v", err)
	}
	waitForBytes(t, sess, "after", 15*time.Second)
}

// TestUpdateNodesAddsAndRemovesNodes verifies that a credential-preserving update
// still adds and removes nodes. Before this, a failed credential fetch skipped
// the SSH update entirely, so a node removed from inventory kept its console
// running and a newly added node never started.
func TestUpdateNodesAddsAndRemovesNodes(t *testing.T) {
	srv := newSSHServer(t, func(ch gossh.Channel) { _ = ch.Close() })
	id, nodeMap, _ := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Add. The node must be registered so a later UpdateCredentials can reach it.
	if err := manager.UpdateNodes(ctx, nodeMap); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Attach(id, "probe"); err != nil {
		t.Fatalf("node was not registered by a credential-preserving update: %v", err)
	}
	manager.Detach(id, "probe")

	// Remove.
	if err := manager.UpdateNodes(ctx, map[string]*nodes.NodeConsoleInfo{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Attach(id, "probe"); err == nil {
		t.Fatal("node still registered after removal via a credential-less update")
	}
}

// TestUpdateNodesAndCredentialsAbsentPassword pins the other half of the
// contract. When the caller vouches for the passwords map, a nodeID absent from
// it means the node has no stored password and should fall through to key auth
// (buildAuth). UpdateNodesAndCredentials must therefore apply the empty credentials and
// reconnect, rather than assuming the entry was merely missing.
func TestUpdateNodesAndCredentialsAbsentPassword(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := manager.UpdateNodesAndCredentials(ctx, nodeMap, passwords); err != nil {
		t.Fatal(err)
	}

	clientCh, err := manager.Attach(id, "keyauth-client")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Detach(id, "keyauth-client")

	<-sessionCh
	waitForMarker(t, clientCh, "[Console "+id+" connected at", 15*time.Second)

	// Authoritative update that no longer carries a password for this node.
	if err := manager.UpdateNodesAndCredentials(ctx, nodeMap, map[string]compcredentials.CompCredentials{}); err != nil {
		t.Fatal(err)
	}

	// The connection must be torn down so the node can re-authenticate. This
	// manager has no key path, so the retry then fails and backs off — the
	// disconnect itself is what proves the credentials were applied.
	waitForMarker(t, clientCh, "[Console "+id+" disconnected at", 15*time.Second)
}
