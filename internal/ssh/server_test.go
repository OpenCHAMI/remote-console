// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cray-HPE/hms-compcredentials"
	gossh "golang.org/x/crypto/ssh"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
	"github.com/OpenCHAMI/remote-console/internal/ssh"
)

const (
	testSSHUser = "testuser"
	testSSHPass = "testpass"
	// testSSHPassAlt allows credential changes without breaking authentication.
	testSSHPassAlt = "testpass-alt"
)

// sshServer is a minimal in-process server on a random loopback port. It calls onShell in a new
// goroutine for each shell or exec session. onShell drains and closes the accepted channel.
type sshServer struct {
	listener net.Listener
	cfg      *gossh.ServerConfig
	onShell  func(ch gossh.Channel)

	// accepts counts TCP connections before authentication. It is the earliest observable
	// connection attempt.
	accepts atomic.Int64
}

// newSSHServer starts a password-authenticated test server.
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
		s.accepts.Add(1)
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

// sshSession gives a test direct control over one established shell session.
type sshSession struct {
	send chan<- []byte // write here to push bytes to the client
	recv <-chan []byte // read here to see bytes the client wrote
	drop func()        // close the server-side channel immediately
}

// runSession gives tests control of session input, output, and disconnection. It keeps the channel
// open until drop is called.
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

	// Forward client input to recvCh.
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

	// Forward sendCh data to client output.
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

// nodeAddr separates a network address into its host and port.
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

// singleNode builds matching inventory and credentials for one SSH node.
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

// startNodes follows service ordering by registering membership before credentials. The first
// connection attempt therefore occurs after UpdateCredentials.
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

// makePasswordsWith assigns one password to every node.
func makePasswordsWith(ids []string, password string) map[string]compcredentials.CompCredentials {
	m := make(map[string]compcredentials.CompCredentials, len(ids))
	for _, id := range ids {
		m[id] = compcredentials.CompCredentials{Username: testSSHUser, Password: password}
	}
	return m
}

// newManager creates a manager with fast reconnect intervals. logsDir must come from t.TempDir
// before this call so manager cleanup runs first. Callers must defer cancellation of the context
// passed to UpdateNodes.
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

// waitOnCleanup waits for node goroutines before temporary directory cleanup. Without it, Run can
// recreate a log after directory removal begins and cause cleanup to fail. Calling t.TempDir
// before registering this cleanup ensures the wait runs first.
func waitOnCleanup(t *testing.T, manager *ssh.SSHConsoleManager) {
	t.Helper()
	t.Cleanup(manager.Wait)
}

// waitForMarker reads until the accumulated output contains marker or the timeout expires. It
// returns all output received.
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
