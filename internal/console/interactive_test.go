// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package console

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
	"github.com/OpenCHAMI/remote-console/internal/ssh"
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

// fakeConsoleIO records attempted and accepted writes separately.
type fakeConsoleIO struct {
	out chan []byte

	mu        sync.Mutex
	err       error
	attempted []string
	accepted  []string
	closed    bool
}

func newFakeConsoleIO() *fakeConsoleIO {
	return &fakeConsoleIO{out: make(chan []byte, 16)}
}

func (f *fakeConsoleIO) Output() <-chan []byte { return f.out }

func (f *fakeConsoleIO) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempted = append(f.attempted, string(p))
	if f.err != nil {
		return 0, f.err
	}
	f.accepted = append(f.accepted, string(p))
	return len(p), nil
}

func (f *fakeConsoleIO) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeConsoleIO) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeConsoleIO) snapshot() (attempted, accepted []string, closed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.attempted...), append([]string(nil), f.accepted...), f.closed
}

// startSession mirrors the production handler shutdown behavior.
func startSession(t *testing.T, cio consoleIO) (client *websocket.Conn, done chan struct{}) {
	t.Helper()

	done = make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		session := newInteractiveConsoleSession("test-node", conn, cio)
		defer close(done)
		defer session.close()
		session.Start(r.Context())
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial console websocket: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client, done
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func sendText(t *testing.T, c *websocket.Conn, s string) {
	t.Helper()
	if err := c.WriteMessage(websocket.TextMessage, []byte(s)); err != nil {
		t.Fatalf("client write %q: %v", s, err)
	}
}

func TestInteractiveSessionForwardsInput(t *testing.T) {
	fake := newFakeConsoleIO()
	client, _ := startSession(t, fake)

	sendText(t, client, "uname -a\r")

	waitFor(t, 5*time.Second, "input to reach the backend", func() bool {
		_, accepted, _ := fake.snapshot()
		return len(accepted) == 1 && accepted[0] == "uname -a\r"
	})
}

func TestInteractiveSessionRejectsOversizedInput(t *testing.T) {
	fake := newFakeConsoleIO()
	client, done := startSession(t, fake)

	input := make([]byte, maxWebSocketMessageSize+1)
	if err := client.WriteMessage(websocket.BinaryMessage, input); err != nil {
		t.Fatalf("client write: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not close after oversized input")
	}

	attempted, _, _ := fake.snapshot()
	if len(attempted) != 0 {
		t.Fatalf("oversized input reached the backend in %d writes", len(attempted))
	}
}

// A temporary disconnect drops input without ending the session.
func TestInteractiveSessionSurvivesNotConnected(t *testing.T) {
	fake := newFakeConsoleIO()
	fake.setErr(errNotConnected)
	client, done := startSession(t, fake)

	sendText(t, client, "lost\r")
	waitFor(t, 5*time.Second, "the dropped input to be attempted", func() bool {
		attempted, _, _ := fake.snapshot()
		return len(attempted) == 1
	})

	select {
	case <-done:
		t.Fatal("session ended when the backend was merely disconnected")
	case <-time.After(250 * time.Millisecond):
	}

	fake.setErr(nil)
	sendText(t, client, "recovered\r")

	waitFor(t, 5*time.Second, "input after the backend reconnected", func() bool {
		_, accepted, _ := fake.snapshot()
		return len(accepted) == 1 && accepted[0] == "recovered\r"
	})

	attempted, accepted, _ := fake.snapshot()
	if len(attempted) != 2 {
		t.Errorf("backend saw %d write attempts, want 2: %q", len(attempted), attempted)
	}
	for _, got := range accepted {
		if got == "lost\r" {
			t.Error("input written while disconnected was reported as accepted")
		}
	}
}

// A permanent write error closes the client with the original reason.
func TestInteractiveSessionClosesOnWriteError(t *testing.T) {
	fake := newFakeConsoleIO()
	fake.setErr(errors.New("console backend exploded"))
	client, done := startSession(t, fake)

	sendText(t, client, "anything\r")

	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := client.ReadMessage()
	if err == nil {
		t.Fatal("client read succeeded; the session did not close on a backend write error")
	}
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("client saw %v, want a websocket close frame", err)
	}
	if closeErr.Code != websocket.CloseInternalServerErr {
		t.Errorf("close code = %d (%q), want %d because the failure was relabelled",
			closeErr.Code, closeErr.Text, websocket.CloseInternalServerErr)
	}
	if closeErr.Text != "failed to write to console" {
		t.Errorf("close reason = %q, want %q", closeErr.Text, "failed to write to console")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session goroutines did not exit after the backend write error")
	}

	if _, _, closed := fake.snapshot(); !closed {
		t.Error("backend was not closed when the session ended")
	}
}

func deadTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// Wait for node goroutines before temporary log cleanup.
func newTestSSHManager(t *testing.T, logsDir string) (*ssh.SSHConsoleManager, context.Context) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	mgr := ssh.NewSSHConsoleManager(ssh.DefaultSSHConfig(), "", logsDir)
	t.Cleanup(func() {
		cancel()
		mgr.Wait()
	})
	return mgr, ctx
}

// Temporary SSH disconnects use the package sentinel and retain the SSH cause.
func TestSSHBackendTranslatesNotConnected(t *testing.T) {
	const nodeID = "x0c0s1b0n0"

	mgr, ctx := newTestSSHManager(t, t.TempDir())
	node := &nodes.NodeConsoleInfo{
		ID:             nodeID,
		ConnectionType: nodes.SSH,
		ConnectionHost: "127.0.0.1",
		ConnectionPort: deadTCPPort(t),
	}
	if err := mgr.UpdateNodes(ctx, map[string]*nodes.NodeConsoleInfo{nodeID: node}); err != nil {
		t.Fatal(err)
	}

	cio, err := newSSHConsoleIO(nodeID, mgr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cio.Close() })

	// A registered node without a connection models a reconnect in progress.
	_, err = cio.Write([]byte("keystroke"))
	if !errors.Is(err, errNotConnected) {
		t.Fatalf("Write to a disconnected SSH node returned %v, want errNotConnected", err)
	}
	// The wrapped SSH cause remains available to callers.
	if !errors.Is(err, ssh.ErrNotConnected) {
		t.Errorf("Write returned %v; the ssh cause was dropped instead of wrapped", err)
	}

	// An unmanaged node is a permanent failure.
	absent := &sshConsoleIO{nodeID: "no-such-node", manager: mgr}
	if _, err := absent.Write([]byte("keystroke")); err == nil {
		t.Error("Write to an unmanaged node succeeded")
	} else if errors.Is(err, errNotConnected) {
		t.Error("Write to an unmanaged node reported errNotConnected; the session would keep the client attached to a console that does not exist")
	}
}

// Deferred cleanup must not replace the original close reason.
func TestWebSocketCloseReasonFirstWins(t *testing.T) {
	ws := newWebSocketSession(nil, "test")

	ws.close(sessionCloseError, "failed to write to console")
	ws.close(sessionCloseNormal, "")

	ws.closeMutex.Lock()
	code, reason := ws.closeCode, ws.closeReason
	ws.closeMutex.Unlock()

	if code != websocket.CloseInternalServerErr {
		t.Errorf("close code = %d, want %d: deferred cleanup relabelled a failure as a clean exit",
			code, websocket.CloseInternalServerErr)
	}
	if reason != "failed to write to console" {
		t.Errorf("close reason = %q, want the original failure text", reason)
	}
}

// Connection types select the expected backend.
func TestNewConsoleIODispatch(t *testing.T) {
	const nodeID = "x0c0s0b0n0"

	mgr, ctx := newTestSSHManager(t, t.TempDir())
	sshNode := &nodes.NodeConsoleInfo{
		ID:             nodeID,
		ConnectionType: nodes.SSH,
		ConnectionHost: "127.0.0.1",
		ConnectionPort: 1,
	}
	if err := mgr.UpdateNodes(ctx, map[string]*nodes.NodeConsoleInfo{nodeID: sshNode}); err != nil {
		t.Fatal(err)
	}

	t.Run("ssh node uses the ssh manager", func(t *testing.T) {
		cio, err := newConsoleIO(sshNode, mgr)
		if err != nil {
			t.Fatalf("newConsoleIO for an SSH node: %v", err)
		}
		t.Cleanup(func() { _ = cio.Close() })
		if _, ok := cio.(*sshConsoleIO); !ok {
			t.Fatalf("SSH node got a %T backend, want *sshConsoleIO", cio)
		}
	})

	t.Run("ipmi node uses conman", func(t *testing.T) {
		ipmiNode := &nodes.NodeConsoleInfo{
			ID:             nodeID,
			ConnectionType: nodes.IPMI,
			ConnectionHost: "127.0.0.1",
		}
		// This node exists only in the SSH manager, so ConMan must reject it.
		cio, err := newConsoleIO(ipmiNode, mgr)
		if err == nil {
			_ = cio.Close()
			t.Fatalf("IPMI node got a %T backend; it was routed to the SSH manager", cio)
		}
		if cio != nil {
			t.Errorf("newConsoleIO returned both a backend (%T) and an error", cio)
		}
	})

	t.Run("unsupported node is rejected", func(t *testing.T) {
		node := &nodes.NodeConsoleInfo{
			ID:             nodeID,
			ConnectionType: "telnet",
		}
		cio, err := newConsoleIO(node, mgr)
		if err == nil {
			t.Fatalf("unsupported node got a %T backend", cio)
		}
		if cio != nil {
			t.Errorf("newConsoleIO returned both a backend (%T) and an error", cio)
		}
	})
}
