// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh_test

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
)

// ---------------------------------------------------------------------------
// 1. Reconnect on disconnect
// ---------------------------------------------------------------------------

// TestSSHConsoleReconnect verifies that when the server drops a connection
// the node broadcasts a disconnect marker, then reconnects automatically and
// broadcasts a connected marker.
func TestSSHConsoleReconnect(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startNodes(t, manager, ctx, nodeMap, passwords)

	// Attach before the first connect so we capture the initial connected marker.
	clientCh, err := manager.Attach(id, "reconnect-client")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Detach(id, "reconnect-client")

	sess := <-sessionCh
	waitForMarker(t, clientCh, fmt.Sprintf("[Console %s connected at", id), 15*time.Second)

	// Drop the connection server-side.
	sess.drop()

	// Node should broadcast a disconnect marker.
	waitForMarker(t, clientCh, fmt.Sprintf("[Console %s disconnected at", id), 15*time.Second)

	// Server accepts the reconnect; node should broadcast another connected marker.
	<-sessionCh
	waitForMarker(t, clientCh, fmt.Sprintf("[Console %s connected at", id), 15*time.Second)
}

// ---------------------------------------------------------------------------
// 2. Fan-out broadcast to multiple clients
// ---------------------------------------------------------------------------

// TestSSHConsoleFanOut verifies that output from the SSH session is
// delivered to every attached client and that a slow client (full buffer)
// only loses its own data — it does not block other clients.
func TestSSHConsoleFanOut(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startNodes(t, manager, ctx, nodeMap, passwords)

	const numClients = 5
	clients := make([]<-chan []byte, numClients)
	for i := 0; i < numClients; i++ {
		ch, err := manager.Attach(id, fmt.Sprintf("fan-client-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		i := i
		t.Cleanup(func() { manager.Detach(id, fmt.Sprintf("fan-client-%d", i)) })
		clients[i] = ch
	}

	// Wait for connection, then drain the connected markers from all clients.
	sess := <-sessionCh
	for _, ch := range clients {
		waitForMarker(t, ch, fmt.Sprintf("[Console %s connected at", id), 15*time.Second)
	}

	// Server sends a known payload; every client must receive it.
	sess.send <- []byte("broadcast-test-payload\r\n")
	for i, ch := range clients {
		waitForMarker(t, ch, "broadcast-test-payload", 5*time.Second)
		t.Logf("client %d received payload", i)
	}

	// Slow-client test: fill one client's buffer so it is full, then verify
	// the fast client still receives new data unimpeded.
	slowCh, err := manager.Attach(id, "slow-client")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Detach(id, "slow-client")

	fastCh, err := manager.Attach(id, "fast-client")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Detach(id, "fast-client")

	filler := []byte(strings.Repeat("x", 1024))
	for i := 0; i < 64; i++ {
		sess.send <- filler
	}
	time.Sleep(200 * time.Millisecond) // let broadcast goroutine saturate the slow client

	sess.send <- []byte("after-slow-client-fill\r\n")
	waitForMarker(t, fastCh, "after-slow-client-fill", 5*time.Second)
	_ = slowCh // slow client may have dropped data — expected behaviour
}

// ---------------------------------------------------------------------------
// 3. Data flow: output reaches clients, input reaches server
// ---------------------------------------------------------------------------

// TestSSHConsoleDataFlow verifies that bytes written by the server arrive
// in the client channel (output path) and bytes written by the client arrive
// at the server's stdin (input path).
func TestSSHConsoleDataFlow(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startNodes(t, manager, ctx, nodeMap, passwords)

	clientCh, err := manager.Attach(id, "data-client")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Detach(id, "data-client")

	sess := <-sessionCh
	waitForMarker(t, clientCh, fmt.Sprintf("[Console %s connected at", id), 15*time.Second)

	// Output path: server → client.
	sess.send <- []byte("hello-from-server\r\n")
	waitForMarker(t, clientCh, "hello-from-server", 5*time.Second)

	// Input path: client → server.
	if _, err := manager.Write(id, []byte("hello-from-client\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var recvBuf strings.Builder
	for {
		select {
		case data := <-sess.recv:
			recvBuf.Write(data)
			if strings.Contains(recvBuf.String(), "hello-from-client") {
				t.Logf("server received: %q", recvBuf.String())
				return
			}
		case <-timer.C:
			t.Fatalf("server did not receive client input; got: %q", recvBuf.String())
		}
	}
}

// ---------------------------------------------------------------------------
// 4. Node lifecycle via UpdateNodes
// ---------------------------------------------------------------------------

// TestSSHConsoleLifecycle verifies that removing a node via UpdateNodes
// cancels its Run goroutine and that all associated goroutines exit cleanly.
func TestSSHConsoleLifecycle(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	goroutinesBefore := runtime.NumGoroutine()

	startNodes(t, manager, ctx, nodeMap, passwords)

	// Wait for the node to connect.
	<-sessionCh
	t.Logf("goroutines with node: %d (added %d)", runtime.NumGoroutine(), runtime.NumGoroutine()-goroutinesBefore)

	// Remove the node. The manager is the sole authority on node lifetime, so
	// cancelling through UpdateNodes must be enough to stop Run.
	if err := manager.UpdateNodes(ctx, map[string]*nodes.NodeConsoleInfo{}); err != nil {
		t.Fatal(err)
	}

	// Attach should now fail.
	if _, err := manager.Attach(id, "post-remove"); err == nil {
		t.Error("Attach succeeded after node removal")
	}

	// Goroutines should drain within a few seconds.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= goroutinesBefore+5 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	leaked := runtime.NumGoroutine() - goroutinesBefore
	t.Logf("goroutines after removal: delta=%d", leaked)
	if leaked > 10 {
		t.Errorf("goroutine leak after node removal: %d above baseline", leaked)
	}
}

// ---------------------------------------------------------------------------
// 6. Slow client accounting
// ---------------------------------------------------------------------------

// TestSlowClientGetsDropNotice covers the case where a client cannot keep up
// with console output. The node has to discard the overflow — it will not stall
// every other client and the log file to wait for one slow websocket — but
// discarding it silently hands the operator a console stream with an
// unmarked hole in it. The dropped bytes must be accounted for in the client's
// own stream once it drains enough to receive the notice.
func TestSlowClientGetsDropNotice(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startNodes(t, manager, ctx, nodeMap, passwords)

	clientCh, err := manager.Attach(id, "slow-client")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Detach(id, "slow-client")

	sess := <-sessionCh
	waitForMarker(t, clientCh, fmt.Sprintf("[Console %s connected at", id), 15*time.Second)

	// Flood the node while reading nothing. The server's writes are
	// flow-controlled by the SSH window, which only advances as the node reads,
	// so once every send has been accepted the node has consumed the lot — and
	// with the client reading none of it, everything past the channel's depth
	// must have been dropped.
	//
	// The burst has to beat that depth measured in broadcasts, not bytes.
	// streamStdout reads up to 32KB at a time, so the worst case for this test
	// is the sends coalescing into 32KB chunks: 24MB still yields ~750
	// broadcasts against a channel that holds clientBufferDepth (256). Keep the
	// margin if that constant grows.
	const (
		bursts    = 1500
		chunkSize = 16 * 1024
	)
	chunk := make([]byte, chunkSize)
	for i := range chunk {
		chunk[i] = 'x'
	}
	for i := 0; i < bursts; i++ {
		select {
		case sess.send <- chunk:
		case <-time.After(30 * time.Second):
			t.Fatalf("server blocked sending burst %d/%d", i, bursts)
		}
	}

	// Drain what the client did manage to buffer, keeping everything it saw.
	// The notice may land here — as soon as the channel has room, the next
	// broadcast carries it — or on the resume write below, so the assertions
	// have to look at the whole stream rather than at one read.
	var seen strings.Builder
	drained := 0
	draining := true
	for draining {
		select {
		case data := <-clientCh:
			seen.Write(data)
			drained++
		case <-time.After(2 * time.Second):
			draining = false
		}
	}
	t.Logf("client drained %d chunks of the %d sent", drained, bursts)

	sess.send <- []byte("back-in-sync\r\n")

	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for !strings.Contains(seen.String(), "back-in-sync") {
		select {
		case data, ok := <-clientCh:
			if !ok {
				t.Fatal("client channel closed before the console resumed")
			}
			seen.Write(data)
		case <-deadline.C:
			t.Fatal("console did not resume after the drop")
		}
	}

	notice := fmt.Sprintf("[Console %s: ", id)
	at := strings.Index(seen.String(), notice)
	if at < 0 {
		t.Fatalf("no drop notice in the client stream after %d of %d chunks arrived", drained, bursts)
	}
	if !strings.Contains(seen.String()[at:], "bytes of output dropped") {
		t.Errorf("drop notice is not the expected marker: %q", seen.String()[at:min(at+120, seen.Len())])
	}
	// The notice has to sit at the gap. Behind the resumed output it would tell
	// the operator the wrong thing about where the console skipped.
	if at > strings.Index(seen.String(), "back-in-sync") {
		t.Error("drop notice arrived after the resumed output; it marks the wrong point in the stream")
	}
}
