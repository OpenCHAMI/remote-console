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

// TestUpdateNodesPreservesCredentials pins the split between the two update
// operations. UpdateNodes decides membership and nothing else; a healthy node
// that survives the diff must keep running on the credentials it already has.
// Clearing them would trigger UpdateCreds, drop the connection, and fall
// through to key auth, which is not what this node uses.
func TestUpdateNodesPreservesCredentials(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startNodes(t, manager, ctx, nodeMap, passwords)

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

	// Same node set — the node survives the diff untouched.
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

// TestUpdateNodesAddsAndRemovesNodes verifies that UpdateNodes adds and removes
// nodes on its own, with no credentials in sight. Before the split, a failed
// credential fetch skipped the SSH update entirely, so a node removed from
// inventory kept its console running and a newly added node never started.
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
		t.Fatalf("node was not registered by UpdateNodes: %v", err)
	}
	manager.Detach(id, "probe")

	// Remove.
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

// waitForLogMarker polls a node's console log file until it contains marker.
//
// Markers are broadcast to the log as well as to attached clients, which is
// what makes this usable where an Attach would race: a node that says something
// once, before any client could have attached, still leaves it on disk.
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

// TestNodeWithoutCredentialsWaitsInsteadOfDialling covers the provisioning
// window. Every node in the cluster is expected to have a Vault entry — a
// password node carries a username and password, a key node a username alone —
// so an entry that has not arrived means it has not been written yet, not that
// the node uses key auth.
//
// Dialling anyway would send an empty username at a real BMC and burn reconnect
// attempts reporting an authentication problem, which is a misleading account
// of a node that is merely early. The node must stay put, say so, and connect
// once the entry reaches it.
func TestNodeWithoutCredentialsWaitsInsteadOfDialling(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	logsDir := t.TempDir()
	manager := newManager(t, logsDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Membership only — no credentials follow.
	if err := manager.UpdateNodes(ctx, nodeMap); err != nil {
		t.Fatal(err)
	}

	waitForLogMarker(t, logsDir, id, "waiting for credentials", 10*time.Second)

	// Well past several reconnect intervals (250ms min, 1s max in tests), so a
	// node that was going to dial would have done it by now.
	time.Sleep(3 * time.Second)

	if got := srv.accepts.Load(); got != 0 {
		t.Errorf("node made %d connection attempt(s) with no credentials, want 0", got)
	}
	select {
	case <-sessionCh:
		t.Fatal("node established a session before it had any credentials")
	default:
	}

	// The entry arrives. The node must pick it up promptly rather than after
	// some later poll — the wait is on a signal, not a timer.
	manager.UpdateCredentials(passwords)

	select {
	case <-sessionCh:
	case <-time.After(15 * time.Second):
		t.Fatal("node did not connect after its credentials arrived")
	}
}

// TestNodesAwaitingCredentials covers the signal the credential watcher runs
// on. A node added after startup needs its Vault entry fetched even though
// nothing in Vault changed, so change detection alone would leave it parked;
// the watcher asks the manager who is still waiting instead.
func TestNodesAwaitingCredentials(t *testing.T) {
	id, nodeMap, passwords := singleNode(t, deadAddr(t))

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if got := manager.NodesAwaitingCredentials(); len(got) != 0 {
		t.Fatalf("NodesAwaitingCredentials on an empty manager = %v, want none", got)
	}

	if err := manager.UpdateNodes(ctx, nodeMap); err != nil {
		t.Fatal(err)
	}
	got := manager.NodesAwaitingCredentials()
	if len(got) != 1 || got[0] != id {
		t.Fatalf("NodesAwaitingCredentials after adding %s = %v, want [%s]", id, got, id)
	}

	// UpdateCredentials applies synchronously, so the node has stopped waiting
	// by the time it returns — no polling window here.
	manager.UpdateCredentials(passwords)
	if got := manager.NodesAwaitingCredentials(); len(got) != 0 {
		t.Fatalf("NodesAwaitingCredentials after delivery = %v, want none", got)
	}

	// A node dropped from inventory is not waiting for anything.
	if err := manager.UpdateNodes(ctx, map[string]*nodes.NodeConsoleInfo{}); err != nil {
		t.Fatal(err)
	}
	if got := manager.NodesAwaitingCredentials(); len(got) != 0 {
		t.Fatalf("NodesAwaitingCredentials after removing the node = %v, want none", got)
	}
}
