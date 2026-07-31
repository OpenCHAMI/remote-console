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

// TestSSHConsoleReconnect verifies automatic reconnection and status markers.
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

	sess.drop()

	waitForMarker(t, clientCh, fmt.Sprintf("[Console %s disconnected at", id), 15*time.Second)

	<-sessionCh
	waitForMarker(t, clientCh, fmt.Sprintf("[Console %s connected at", id), 15*time.Second)
}

// TestSSHConsoleFanOut verifies output reaches all clients independently.
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

	sess.send <- []byte("broadcast-test-payload\r\n")
	for i, ch := range clients {
		waitForMarker(t, ch, "broadcast-test-payload", 5*time.Second)
		t.Logf("client %d received payload", i)
	}

	// A full client buffer must not block another client.
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
	time.Sleep(200 * time.Millisecond) // allow the slow client buffer to fill

	sess.send <- []byte("after-slow-client-fill\r\n")
	waitForMarker(t, fastCh, "after-slow-client-fill", 5*time.Second)
	_ = slowCh // slow client may have dropped data
}

// TestSSHConsoleDataFlow verifies input and output forwarding.
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

	sess.send <- []byte("hello-from-server\r\n")
	waitForMarker(t, clientCh, "hello-from-server", 5*time.Second)

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

// TestSSHConsoleLifecycle verifies node removal stops its goroutines.
func TestSSHConsoleLifecycle(t *testing.T) {
	sessionCh := make(chan *sshSession)
	srv := newSSHServer(t, func(ch gossh.Channel) { runSession(ch, sessionCh) })
	id, nodeMap, passwords := singleNode(t, srv.addr())

	manager := newManager(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	goroutinesBefore := runtime.NumGoroutine()

	startNodes(t, manager, ctx, nodeMap, passwords)

	<-sessionCh
	t.Logf("goroutines with node: %d (added %d)", runtime.NumGoroutine(), runtime.NumGoroutine()-goroutinesBefore)

	// Removing the node must stop Run.
	if err := manager.UpdateNodes(ctx, map[string]*nodes.NodeConsoleInfo{}); err != nil {
		t.Fatal(err)
	}

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

// TestSlowClientGetsDropNotice verifies dropped output is reported in place.
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

	// Send enough data to overflow the client buffer even with 32 KB reads.
	// The SSH window confirms accepted data was consumed by the console.
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

	// The notice may arrive while draining or after output resumes.
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
	// The notice must precede resumed output.
	if at > strings.Index(seen.String(), "back-in-sync") {
		t.Error("drop notice arrived after the resumed output; it marks the wrong point in the stream")
	}
}
