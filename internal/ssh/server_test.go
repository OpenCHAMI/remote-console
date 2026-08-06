// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh_test

// In-process SSH test server and shared test helpers used across scale,
// concurrent, and node-behaviour tests.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cray-HPE/hms-compcredentials"
	gossh "golang.org/x/crypto/ssh"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
	"github.com/OpenCHAMI/remote-console/internal/ssh"
)

// ---------------------------------------------------------------------------
// Test credentials (shared across all tests)
// ---------------------------------------------------------------------------

const (
	testSSHUser = "testuser"
	testSSHPass = "testpass"
	// testSSHPassAlt is a second valid password. Tests that need credentials to
	// actually change (so credsChanged() flips and UpdateCreds() is reached) can
	// alternate between the two without breaking authentication.
	testSSHPassAlt = "testpass-alt"
)

// ---------------------------------------------------------------------------
// sshServer — in-process SSH server
// ---------------------------------------------------------------------------

// sshServer is a minimal in-process SSH server. When a client establishes a
// shell or exec session, onShell is called in its own goroutine with the
// accepted channel. onShell is responsible for draining the channel and closing
// it when done. All connections share a single listener on a random loopback port.
type sshServer struct {
	listener net.Listener
	cfg      *gossh.ServerConfig
	onShell  func(ch gossh.Channel)
}

// newSSHServer starts an in-process SSH server that accepts password auth with
// testSSHUser/testSSHPass and calls onShell for every established shell session.
func newSSHServer(t *testing.T, onShell func(gossh.Channel)) *sshServer {
	t.Helper()

	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate SSH host key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatalf("create SSH host signer: %v", err)
	}

	cfg := &gossh.ServerConfig{
		PasswordCallback: func(c gossh.ConnMetadata, pass []byte) (*gossh.Permissions, error) {
			if c.User() == testSSHUser && (string(pass) == testSSHPass || string(pass) == testSSHPassAlt) {
				return nil, nil
			}
			return nil, fmt.Errorf("access denied")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	srv := &sshServer{listener: ln, cfg: cfg, onShell: onShell}
	go srv.serve()
	return srv
}

func (s *sshServer) addr() string { return s.listener.Addr().String() }

func (s *sshServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *sshServer) handleConn(conn net.Conn) {
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, s.cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer func() { _ = sshConn.Close() }()
	go gossh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(gossh.UnknownChannelType, "unsupported")
			continue
		}
		ch, reqs, err := newChan.Accept()
		if err != nil {
			return
		}
		go s.handleSession(ch, reqs)
	}
}

func (s *sshServer) handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			_ = req.Reply(true, nil)
		case "shell", "exec":
			_ = req.Reply(true, nil)
			s.onShell(ch)
			return
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// sshSession — controllable session handed to tests
// ---------------------------------------------------------------------------

// sshSession gives a test direct control over one established shell session.
type sshSession struct {
	send chan<- []byte // write here to push bytes to the client
	recv <-chan []byte // read here to see bytes the client wrote
	drop func()        // close the server-side channel immediately
}

// runSession is an onShell implementation for tests that need per-session
// control. It pumps stdin/stdout and sends the session to out, then keeps the
// channel alive until drop is called.
func runSession(ch gossh.Channel, out chan<- *sshSession) {
	sendCh := make(chan []byte, 16)
	recvCh := make(chan []byte, 16)
	dropped := make(chan struct{})

	sess := &sshSession{
		send: sendCh,
		recv: recvCh,
		drop: sync.OnceFunc(func() {
			close(dropped)
			_ = ch.Close()
		}),
	}

	// stdin → recvCh
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ch.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				select {
				case recvCh <- cp:
				case <-dropped:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// sendCh → stdout
	go func() {
		for {
			select {
			case data := <-sendCh:
				if _, err := ch.Write(data); err != nil {
					return
				}
			case <-dropped:
				return
			}
		}
	}()

	select {
	case out <- sess:
	case <-dropped:
	}
	<-dropped
}

// ---------------------------------------------------------------------------
// Shared test helpers
// ---------------------------------------------------------------------------

// nodeAddr parses "host:port" and returns the components.
func nodeAddr(t *testing.T, addr string) (host string, port int) {
	t.Helper()
	colon := strings.LastIndex(addr, ":")
	if colon < 0 {
		t.Fatalf("bad addr %q", addr)
	}
	if _, err := fmt.Sscanf(addr[colon+1:], "%d", &port); err != nil {
		t.Fatalf("parse port from %q: %v", addr, err)
	}
	return addr[:colon], port
}

// singleNode builds a one-node map and matching credentials map for tests
// that only need a single SSH node.
func singleNode(t *testing.T, addr string) (id string, nodeMap map[string]*nodes.NodeConsoleInfo, passwords map[string]compcredentials.CompCredentials) {
	t.Helper()
	host, port := nodeAddr(t, addr)
	id = "test-node-0"
	nodeMap = map[string]*nodes.NodeConsoleInfo{
		id: {
			ID:             id,
			ConnectionType: nodes.SSH,
			ConnectionHost: host,
			ConnectionPort: port,
		},
	}
	passwords = map[string]compcredentials.CompCredentials{
		id: {Username: testSSHUser, Password: testSSHPass},
	}
	return
}

// startNodes follows service ordering by registering membership before delivering credentials.
func startNodes(
	t *testing.T,
	manager *ssh.SSHConsoleManager,
	ctx context.Context,
	nodeMap map[string]*nodes.NodeConsoleInfo,
	passwords map[string]compcredentials.CompCredentials,
) {
	t.Helper()
	if err := manager.UpdateNodes(ctx, nodeMap); err != nil {
		t.Fatalf("UpdateNodes: %v", err)
	}
	manager.UpdateCredentials(passwords)
}

// makePasswordsWith builds a credential map assigning the same password to
// every id.
func makePasswordsWith(ids []string, password string) map[string]compcredentials.CompCredentials {
	m := make(map[string]compcredentials.CompCredentials, len(ids))
	for _, id := range ids {
		m[id] = compcredentials.CompCredentials{Username: testSSHUser, Password: password}
	}
	return m
}

// newManager creates an SSHConsoleManager with fast reconnect intervals
// suitable for tests.
//
// Pass logsDir as t.TempDir() at the call site — see waitOnCleanup for why the
// order matters. Callers must cancel the context they hand to
// UpdateNodes with defer, which runs before this cleanup.
func newManager(t *testing.T, logsDir string) *ssh.SSHConsoleManager {
	t.Helper()
	cfg := ssh.DefaultSSHConfig()
	cfg.TCPKeepAlive = 0
	cfg.ReconnectMinInterval = 250 * time.Millisecond
	cfg.ReconnectMaxInterval = 1 * time.Second
	manager := ssh.NewSSHConsoleManager(cfg, "", logsDir)
	waitOnCleanup(t, manager)
	return manager
}

// waitOnCleanup makes a test wait for the manager's node goroutines before it
// finishes.
//
// A node opens its log file as the first thing Run does, and the manager only
// ever asks nodes to stop. A test that hands t.TempDir() to a manager and
// returns without waiting races its own cleanup: RemoveAll empties the
// directory, a node goroutine gets scheduled and recreates the log file, and
// the final rmdir fails with "directory not empty".
//
// Cleanups run last-in-first-out, so as long as the caller obtained logsDir
// from t.TempDir() before reaching here, the wait happens first and the
// directory is empty when it is removed.
func waitOnCleanup(t *testing.T, manager *ssh.SSHConsoleManager) {
	t.Helper()
	t.Cleanup(manager.Wait)
}

// waitForMarker reads from ch until the accumulated output contains marker or
// the timeout expires. Returns the full output seen.
func waitForMarker(t *testing.T, ch <-chan []byte, marker string, timeout time.Duration) string {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var buf strings.Builder
	for {
		select {
		case data, ok := <-ch:
			if !ok {
				t.Fatalf("client channel closed before marker %q; received: %q", marker, buf.String())
			}
			buf.Write(data)
			if strings.Contains(buf.String(), marker) {
				return buf.String()
			}
		case <-timer.C:
			t.Fatalf("timeout waiting for marker %q; received: %q", marker, buf.String())
			return ""
		}
	}
}
