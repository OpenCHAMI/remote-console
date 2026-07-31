// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"time"

	compcredentials "github.com/Cray-HPE/hms-compcredentials"
	gossh "golang.org/x/crypto/ssh"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
)

// consoleClient is one attached interactive client.
type consoleClient struct {
	ch chan []byte
	// dropped counts bytes discarded because ch was full since the last drop
	// notice this client was given. A console that silently skips output is
	// worse than one that admits to a gap — an operator reading back a boot log
	// has no other way to tell that something was cut.
	dropped int
}

// SSHConsoleNode manages a single persistent SSH console connection.
// Its Run goroutine is the sole writer for the log file and client channels —
// no lock is needed for those writes.
type SSHConsoleNode struct {
	nodeID  string
	info    *nodes.NodeConsoleInfo
	keyPath string
	cfg     SSHConfig

	credsMu sync.RWMutex
	creds   compcredentials.CompCredentials

	// clientsMu protects the clients map and the consoleClient values in it.
	// broadcast takes it for writing because it updates each client's drop
	// accounting; Attach and Detach are rare enough that the lost read
	// concurrency does not matter.
	// Lock ordering: nodesMu (manager) → clientsMu → connMu
	clientsMu sync.RWMutex
	clients   map[string]*consoleClient // channel closed and entry deleted on Detach or Run exit

	// connMu protects sshClient and stdin.
	connMu    sync.Mutex
	sshClient *gossh.Client
	stdin     io.WriteCloser

	// stdinMu serializes Write calls. golang.org/x/crypto/ssh reuses a per-channel
	// packet buffer (packetPool) across WriteExtended calls, so concurrent writes
	// to the same stdin pipe race. Multiple interactive clients may call Write
	// simultaneously; this mutex ensures they are serialized.
	stdinMu sync.Mutex

	// logFile is opened at the start of Run and written only from the Run goroutine.
	logFile     *os.File
	logPath     string        // logsPath + "/console." + nodeID
	reopenLogCh chan struct{} // buffered depth-1; signals broadcast to reopen after rotation

	// cancel is called by the manager to stop the Run goroutine.
	// ctx is NOT stored in the struct to avoid the go vet context-in-struct antipattern.
	cancel context.CancelFunc
}

// newSSHConsoleNode constructs an SSHConsoleNode. The caller (manager) must set
// node.cancel and call go node.Run(nodeCtx) after construction.
func newSSHConsoleNode(nodeID string, info *nodes.NodeConsoleInfo, creds compcredentials.CompCredentials, keyPath, logPath string, cfg SSHConfig) *SSHConsoleNode {
	return &SSHConsoleNode{
		nodeID:      nodeID,
		info:        info,
		keyPath:     keyPath,
		cfg:         cfg,
		creds:       creds,
		clients:     make(map[string]*consoleClient),
		logPath:     logPath,
		reopenLogCh: make(chan struct{}, 1),
	}
}

// streamStdout reads from the SSH session stdout and broadcasts to all clients
// until the connection drops.
func (n *SSHConsoleNode) streamStdout(stdout io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		nr, err := stdout.Read(buf)
		if nr > 0 {
			n.broadcast(buf[:nr])
		}
		if err != nil {
			break
		}
	}
}

// connectAndStream attempts one connection, streams output until disconnect,
// then cleans up. Returns true if the connection was established (used by Run
// to reset the backoff on successful connects).
func (n *SSHConsoleNode) connectAndStream(ctx context.Context) bool {
	stdout, err := n.connect(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		slog.Error("SSH console connect failed", "nodeID", n.nodeID, "error", err)
		n.broadcast([]byte(fmt.Sprintf("\n[Console %s connect failed: %v]\n", n.nodeID, err)))
		return false
	}

	// When ctx is cancelled, close the SSH client so streamStdout's Read
	// unblocks immediately instead of waiting for the remote side to hang up.
	ctxDone := make(chan struct{})
	defer close(ctxDone)
	go func() {
		select {
		case <-ctx.Done():
			n.connMu.Lock()
			if n.sshClient != nil {
				_ = n.sshClient.Close()
			}
			n.connMu.Unlock()
		case <-ctxDone:
		}
	}()

	n.streamStdout(stdout)

	// Clean up the connection. Nil stdin under connMu so Write sees nil and
	// reports ErrNotConnected, then close it under stdinMu so an in-flight Write
	// cannot race with Close on the underlying SSH channel buffer.
	n.connMu.Lock()
	if n.sshClient != nil {
		_ = n.sshClient.Close()
		n.sshClient = nil
	}
	stdin := n.stdin
	n.stdin = nil
	n.connMu.Unlock()
	if stdin != nil {
		n.stdinMu.Lock()
		_ = stdin.Close()
		n.stdinMu.Unlock()
	}

	n.broadcast([]byte(fmt.Sprintf("\n[Console %s disconnected at %s]\n",
		n.nodeID, time.Now().Format(time.RFC3339))))
	return true
}

// stableConnection is how long a connection must last before the reconnect
// backoff is considered recovered and reset to ReconnectMinInterval.
const stableConnection = 30 * time.Second

// jitter returns d scaled by a random factor in [0.5, 1.0). Without it, a
// network blip reconnects every node at the same instant — at the scale this
// manager targets that is a thundering herd against the BMC network.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// Run is the main loop for the node. It connects, reads output, fans it out,
// and reconnects on disconnect.
//
// It exits only when ctx is cancelled. The manager owns that ctx and cancels it
// when the node leaves inventory, which makes the manager the single authority
// on node lifetime. Run must not decide to stop on its own: the manager would
// keep the node in its map with no goroutine behind it, so Attach would hand
// back a channel that never delivers and no later UpdateNodes would revive it
// (the node still "exists" and its parameters are unchanged).
func (n *SSHConsoleNode) Run(ctx context.Context) {
	var err error
	n.logFile, err = os.OpenFile(n.logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		slog.Error("Failed to open SSH console log file", "nodeID", n.nodeID, "path", n.logPath, "error", err)
		// Continue with nil logFile; broadcast skips write when nil.
	}
	defer func() {
		if n.logFile != nil {
			_ = n.logFile.Close()
		}
		// Close and delete all client channels so streamOutput goroutines
		// see ok=false and exit cleanly.
		n.clientsMu.Lock()
		for id, c := range n.clients {
			close(c.ch)
			delete(n.clients, id)
		}
		n.clientsMu.Unlock()
	}()

	// Backstop against a config that never went through SSHConfig.Validate —
	// a directly-constructed zero value would otherwise spin this loop with no
	// wait between reconnects. Not a substitute for validation; just a floor so
	// a programming error can't hammer the BMC network.
	minBackoff := n.cfg.ReconnectMinInterval
	if minBackoff < minReconnectInterval {
		slog.Warn("SSH reconnect interval below minimum, clamping",
			"nodeID", n.nodeID, "configured", minBackoff, "using", minReconnectInterval)
		minBackoff = minReconnectInterval
	}
	backoff := minBackoff

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		start := time.Now()
		connected := n.connectAndStream(ctx)

		// Only reset the backoff for a connection that actually held up. A host
		// that accepts, authenticates, then immediately drops (bad entry command,
		// session limit) would otherwise reset every cycle and be hammered at
		// ReconnectMinInterval indefinitely.
		if connected && time.Since(start) >= stableConnection {
			backoff = minBackoff
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(backoff)):
		}

		backoff *= 2
		if backoff > n.cfg.ReconnectMaxInterval {
			backoff = n.cfg.ReconnectMaxInterval
		}
	}
}

// currentCreds returns a snapshot of the node's credentials under credsMu.
// All reads of n.creds — including from the manager — must go through this.
func (n *SSHConsoleNode) currentCreds() compcredentials.CompCredentials {
	n.credsMu.RLock()
	defer n.credsMu.RUnlock()
	return n.creds
}

// dialClient reads credentials, dials TCP, and completes the SSH handshake.
func (n *SSHConsoleNode) dialClient(ctx context.Context) (*gossh.Client, error) {
	creds := n.currentCreds()

	auth, err := n.buildAuth(creds)
	if err != nil {
		return nil, fmt.Errorf("build SSH auth: %w", err)
	}

	port := n.info.ConnectionPort
	if port == 0 {
		port = 22
	}
	addr := fmt.Sprintf("%s:%d", n.info.ConnectionHost, port)

	dialer := &net.Dialer{Timeout: n.cfg.ConnectTimeout}
	if n.cfg.TCPKeepAlive > 0 {
		dialer.KeepAlive = n.cfg.TCPKeepAlive
	}
	netConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	// gossh.NewClientConn neither takes a context nor honours ClientConfig.Timeout
	// (that field is only consulted by gossh.Dial for the TCP dial), so a host that
	// completes the TCP handshake but stalls the SSH one would block here forever.
	// Guard it with both an absolute deadline and a ctx watchdog that closes the
	// conn, so shutdown is not held up either.
	if err := netConn.SetDeadline(time.Now().Add(n.cfg.ConnectTimeout)); err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("set handshake deadline for %s: %w", addr, err)
	}
	handshakeDone := make(chan struct{})
	defer close(handshakeDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = netConn.Close()
		case <-handshakeDone:
		}
	}()

	sshConn, chans, reqs, err := gossh.NewClientConn(netConn, addr, &gossh.ClientConfig{
		User:            creds.Username,
		Auth:            auth,
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // BMC management network; matches existing StrictHostKeyChecking=no
	})
	if err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("SSH handshake with %s: %w", addr, err)
	}

	// Clear the handshake deadline — the session is long-lived and must not
	// inherit it. Liveness from here on is TCP keepalives.
	if err := netConn.SetDeadline(time.Time{}); err != nil {
		_ = sshConn.Close()
		return nil, fmt.Errorf("clear handshake deadline for %s: %w", addr, err)
	}

	return gossh.NewClient(sshConn, chans, reqs), nil
}

// startSession opens a PTY session on client, wires up stdout/stdin pipes, and
// starts the shell or entry command. Returns stdout and stdin on success.
func (n *SSHConsoleNode) startSession(client *gossh.Client) (stdout io.Reader, stdin io.WriteCloser, err error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, nil, fmt.Errorf("new SSH session: %w", err)
	}

	if err := session.RequestPty(n.cfg.TerminalType, 24, 80, gossh.TerminalModes{
		gossh.ECHO:          1,
		gossh.TTY_OP_ISPEED: 115200, // matches conman seropts="115200,8n1"
		gossh.TTY_OP_OSPEED: 115200,
	}); err != nil {
		_ = session.Close()
		return nil, nil, fmt.Errorf("request PTY: %w", err)
	}

	// StdoutPipe and StdinPipe must be called before Shell/Start.
	stdout, err = session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stdin, err = session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}

	if n.info.ConsoleEntryCommand == "" {
		err = session.Shell()
	} else {
		err = session.Start(n.info.ConsoleEntryCommand)
	}
	if err != nil {
		_ = session.Close()
		return nil, nil, fmt.Errorf("start SSH session: %w", err)
	}

	// Reap the session when it ends to release server-side resources.
	go func() {
		if err := session.Wait(); err != nil {
			slog.Debug("SSH session ended", "nodeID", n.nodeID, "error", err)
		}
	}()

	return stdout, stdin, nil
}

// connect dials the remote host, opens a PTY session, and stores connection
// state. Returns the stdout reader on success; caller must drain it until
// error/EOF.
func (n *SSHConsoleNode) connect(ctx context.Context) (io.Reader, error) {
	client, err := n.dialClient(ctx)
	if err != nil {
		return nil, err
	}

	// Track whether connect succeeds so the defer can clean up on failure.
	success := false
	defer func() {
		if !success {
			n.connMu.Lock()
			n.sshClient = nil
			n.stdin = nil
			n.connMu.Unlock()
			_ = client.Close()
		}
	}()

	n.connMu.Lock()
	n.sshClient = client
	n.connMu.Unlock()

	stdout, stdin, err := n.startSession(client)
	if err != nil {
		return nil, err
	}

	n.connMu.Lock()
	n.stdin = stdin
	n.connMu.Unlock()

	n.broadcast([]byte(fmt.Sprintf("\n[Console %s connected at %s]\n",
		n.nodeID, time.Now().Format(time.RFC3339))))

	success = true
	return stdout, nil
}

// buildAuth selects the SSH auth method based on credentials and keyPath.
// Priority matches the original ssh-pwd-console / ssh-key-console scripts:
//   - Password set → password auth (even when a key file is also present)
//   - No password   → key auth: cert+key if cert exists, key-only otherwise
func (n *SSHConsoleNode) buildAuth(creds compcredentials.CompCredentials) ([]gossh.AuthMethod, error) {
	// Password takes priority — matches ssh-pwd-console behaviour.
	if creds.Password != "" {
		return []gossh.AuthMethod{gossh.Password(creds.Password)}, nil
	}

	// No password: attempt key-based auth — matches ssh-key-console behaviour.
	if n.keyPath == "" {
		return nil, fmt.Errorf("no SSH auth available for %s: no password and no key path configured", n.nodeID)
	}

	keyData, err := os.ReadFile(n.keyPath)
	if err != nil {
		return nil, fmt.Errorf("read SSH key %s: %w", n.keyPath, err)
	}
	signer, err := gossh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parse SSH private key: %w", err)
	}

	// Check for a certificate alongside the key.
	certPath := n.keyPath + "-cert.pub"
	certData, err := os.ReadFile(certPath)
	if err == nil {
		pubKey, _, _, _, err := gossh.ParseAuthorizedKey(certData)
		if err != nil {
			return nil, fmt.Errorf("parse SSH certificate: %w", err)
		}
		cert, ok := pubKey.(*gossh.Certificate)
		if !ok {
			return nil, fmt.Errorf("file %s is not an SSH certificate", certPath)
		}
		certSigner, err := gossh.NewCertSigner(cert, signer)
		if err != nil {
			return nil, fmt.Errorf("create cert signer: %w", err)
		}
		return []gossh.AuthMethod{gossh.PublicKeys(certSigner)}, nil
	}

	// Key only (no cert).
	return []gossh.AuthMethod{gossh.PublicKeys(signer)}, nil
}

// broadcast sends data to the log file and all attached client channels.
// Called only from the Run goroutine — no lock needed for logFile writes.
func (n *SSHConsoleNode) broadcast(data []byte) {
	// Handle log rotation signal (non-blocking, depth-1 channel debounces).
	select {
	case <-n.reopenLogCh:
		if n.logFile != nil {
			_ = n.logFile.Close()
		}
		var err error
		n.logFile, err = os.OpenFile(n.logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			slog.Error("Failed to reopen SSH console log after rotation", "nodeID", n.nodeID, "error", err)
			n.logFile = nil
		}
	default:
	}

	if n.logFile != nil {
		if _, err := n.logFile.Write(data); err != nil {
			slog.Warn("Failed to write to SSH console log", "nodeID", n.nodeID, "error", err)
		}
	}

	n.clientsMu.Lock()
	defer n.clientsMu.Unlock()
	for id, c := range n.clients {
		n.sendToClient(id, c, data)
	}
}

// sendToClient delivers data to one client, accounting for anything discarded
// because its channel was full. Must be called with clientsMu held for writing.
//
// A drop notice cannot be delivered at the moment of the drop — the channel is
// full, which is the whole problem. It is held until the client has drained
// enough to accept it, and every byte lost in the meantime is folded into the
// same notice, so a client stalled for megabytes gets one accurate line rather
// than a flood. The notice is emitted before the data that follows it, which
// puts the gap marker in the right place in the client's stream.
func (n *SSHConsoleNode) sendToClient(id string, c *consoleClient, data []byte) {
	if c.dropped > 0 {
		select {
		case c.ch <- dropNotice(n.nodeID, c.dropped):
			slog.Info("SSH console client caught up after dropping output",
				"nodeID", n.nodeID, "clientID", id, "bytesDropped", c.dropped)
			c.dropped = 0
		default:
			// Still backed up. Keep counting; do not try to send the data.
			c.dropped += len(data)
			return
		}
	}

	select {
	case c.ch <- append([]byte{}, data...):
	default:
		if c.dropped == 0 {
			// Log the transition only. Logging every dropped chunk would put
			// one line per 32KB read into the service log for as long as the
			// client stays behind.
			slog.Warn("SSH console client not keeping up, dropping output",
				"nodeID", n.nodeID, "clientID", id)
		}
		c.dropped += len(data)
	}
}

// dropNotice renders the marker injected into a client's stream to account for
// output it never received.
func dropNotice(nodeID string, bytesDropped int) []byte {
	return fmt.Appendf(nil, "\n[Console %s: %d bytes of output dropped, client not keeping up]\n",
		nodeID, bytesDropped)
}

// clientBufferDepth is how many output chunks a client may fall behind before
// the node starts discarding its output. It matches the conman backend so both
// consoles behave the same under a slow client.
//
// The unit is chunks, not bytes: one chunk is whatever a single read of the SSH
// session returned, up to the 32KB streamStdout buffer. Depth buys a client
// time to recover from a stall, but only up to a point — an interactive console
// that is megabytes behind is not much use, and past there an acknowledged gap
// beats a long lag. Raise this only with evidence from the "not keeping up"
// warning in the service log.
const clientBufferDepth = 256

// Attach registers a client receive channel. Returns a buffered channel that
// receives console output. The manager ensures the node exists before calling.
func (n *SSHConsoleNode) Attach(clientID string) chan []byte {
	ch := make(chan []byte, clientBufferDepth)
	n.clientsMu.Lock()
	n.clients[clientID] = &consoleClient{ch: ch}
	n.clientsMu.Unlock()
	return ch
}

// Detach deregisters a client channel. Idempotent.
func (n *SSHConsoleNode) Detach(clientID string) {
	n.clientsMu.Lock()
	defer n.clientsMu.Unlock()
	if c, ok := n.clients[clientID]; ok {
		close(c.ch)
		delete(n.clients, clientID)
	}
}

// Write sends data to the SSH session's stdin. It reports ErrNotConnected
// rather than claiming a successful write when the node is between
// connections, so callers can decide what to do with the discarded input; a
// transient disconnect is not a reason to tear the caller down.
func (n *SSHConsoleNode) Write(p []byte) (int, error) {
	n.stdinMu.Lock()
	defer n.stdinMu.Unlock()

	n.connMu.Lock()
	stdin := n.stdin
	n.connMu.Unlock()

	if stdin == nil {
		return 0, ErrNotConnected
	}
	return stdin.Write(p)
}

// UpdateCreds updates stored credentials and forces a reconnect so the new
// credentials take effect.
func (n *SSHConsoleNode) UpdateCreds(creds compcredentials.CompCredentials) {
	n.credsMu.Lock()
	n.creds = creds
	n.credsMu.Unlock()

	// Close the current connection to trigger reconnect with new creds.
	n.connMu.Lock()
	client := n.sshClient
	n.connMu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

// ReopenLog signals the Run goroutine to reopen the log file (after rotation).
func (n *SSHConsoleNode) ReopenLog() {
	select {
	case n.reopenLogCh <- struct{}{}:
	default: // already signalled; no-op
	}
}
