// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh

import (
	"context"
	"errors"
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

// errNoCredentials means no credential entry has reached this node.
// Run waits instead of dialing when credentials are unavailable.
var errNoCredentials = errors.New("no credentials available for node")

// consoleClient is one attached interactive client.
type consoleClient struct {
	ch chan []byte
	// dropped counts bytes discarded since the last notice.
	dropped int
}

// SSHConsole manages one SSH node and persists across connections.
// Run owns log and client channel writes and reconnects until cancelled.
type SSHConsole struct {
	nodeID  string
	info    *nodes.NodeConsoleInfo
	keyPath string
	cfg     SSHConfig

	// credsMu protects creds. Nil means no credential entry has arrived.
	// An empty password may still select key authentication.
	credsMu sync.RWMutex
	creds   *compcredentials.CompCredentials
	// credsCh wakes Run when credentials arrive.
	credsCh chan struct{}

	// clientsMu protects clients and their drop counts.
	// Locks are acquired in the order consolesMu, clientsMu, then connMu.
	clientsMu sync.Mutex
	clients   map[string]*consoleClient // closed on detach or shutdown

	// connMu protects sshClient and stdin.
	connMu    sync.Mutex
	sshClient *gossh.Client
	stdin     io.WriteCloser

	// writeMu serializes writes because SSH reuses a channel packet buffer.
	// Multiple interactive clients may write concurrently.
	writeMu sync.Mutex

	// logFile is written only by Run.
	logFile      *os.File
	logPath      string
	reopenLogCh  chan struct{} // buffered rotation signal
	logFormatter consoleLogFormatter

	// cancel stops Run without storing its context.
	cancel context.CancelFunc
}

// newSSHConsole constructs an SSHConsole.
// Nil credentials delay connection.
func newSSHConsole(nodeID string, info *nodes.NodeConsoleInfo, creds *compcredentials.CompCredentials, keyPath, logPath string, cfg SSHConfig, cancel context.CancelFunc) *SSHConsole {
	return &SSHConsole{
		nodeID:       nodeID,
		info:         info,
		keyPath:      keyPath,
		cfg:          cfg,
		creds:        cloneCreds(creds),
		clients:      make(map[string]*consoleClient),
		logPath:      logPath,
		reopenLogCh:  make(chan struct{}, 1),
		logFormatter: newConsoleLogFormatter(),
		credsCh:      make(chan struct{}, 1),
		cancel:       cancel,
	}
}

// streamStdout reads from the SSH session stdout and broadcasts to all clients
// until the connection drops.
func (c *SSHConsole) streamStdout(stdout io.Reader) {
	// Match the default buffer size used by io.Copy.
	buf := make([]byte, 32*1024)
	for {
		nr, err := stdout.Read(buf)
		if nr > 0 {
			c.broadcast(buf[:nr])
		}
		if err != nil {
			break
		}
	}
}

// connectAndStream streams one connection and then cleans it up.
// It reports whether the connection was established.
func (c *SSHConsole) connectAndStream(ctx context.Context) bool {
	stdout, err := c.connect(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		slog.Error("SSH console connect failed", "nodeID", c.nodeID, "error", err)
		c.broadcast([]byte(fmt.Sprintf("\n[Console %s connect failed: %v]\n", c.nodeID, err)))
		return false
	}

	// When ctx is cancelled, close the SSH client so streamStdout's Read
	// unblocks immediately instead of waiting for the remote side to hang up.
	ctxDone := make(chan struct{})
	defer close(ctxDone)
	go func() {
		select {
		case <-ctx.Done():
			c.connMu.Lock()
			if c.sshClient != nil {
				_ = c.sshClient.Close()
			}
			c.connMu.Unlock()
		case <-ctxDone:
		}
	}()

	c.streamStdout(stdout)

	// Clear stdin before closing it so Write reports ErrNotConnected.
	// writeMu prevents Close from racing with an active Write.
	c.connMu.Lock()
	if c.sshClient != nil {
		_ = c.sshClient.Close()
		c.sshClient = nil
	}
	stdin := c.stdin
	c.stdin = nil
	c.connMu.Unlock()
	if stdin != nil {
		c.writeMu.Lock()
		_ = stdin.Close()
		c.writeMu.Unlock()
	}

	c.broadcast([]byte(fmt.Sprintf("\n[Console %s disconnected at %s]\n",
		c.nodeID, time.Now().Format(time.RFC3339))))
	return true
}

// stableConnection is how long a connection must last before the reconnect
// backoff is considered recovered and reset to ReconnectMinInterval.
const stableConnection = 30 * time.Second

// jitter spreads retries across the latter half of d.
// This avoids simultaneous reconnects after a network failure.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// Run connects, broadcasts output, and reconnects until cancelled.
// The manager controls its lifetime and must not retain a stopped console.
func (c *SSHConsole) Run(ctx context.Context) {
	var err error
	c.logFile, err = os.OpenFile(c.logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		slog.Error("Failed to open SSH console log file", "nodeID", c.nodeID, "path", c.logPath, "error", err)
		// Continue without logging.
	}
	defer func() {
		if c.logFile != nil {
			_ = c.logFile.Close()
		}
		// Close and delete all client channels so streamOutput goroutines
		// see ok=false and exit cleanly.
		c.clientsMu.Lock()
		for id, client := range c.clients {
			close(client.ch)
			delete(c.clients, id)
		}
		c.clientsMu.Unlock()
	}()

	backoff := c.cfg.ReconnectMinInterval

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Wait for a credential entry before dialing.
		// An empty password may select key authentication after it arrives.
		if c.currentCreds() == nil {
			slog.Info("SSH console node waiting for credentials", "nodeID", c.nodeID)
			c.broadcast([]byte(fmt.Sprintf("\n[Console %s waiting for credentials]\n", c.nodeID)))
			select {
			case <-ctx.Done():
				return
			case <-c.credsCh:
			}
			continue
		}

		start := time.Now()
		connected := c.connectAndStream(ctx)

		// Reset backoff only after a stable connection.
		// Immediate disconnects continue backing off.
		if connected && time.Since(start) >= stableConnection {
			backoff = c.cfg.ReconnectMinInterval
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(backoff)):
		}

		backoff *= 2
		if backoff > c.cfg.ReconnectMaxInterval {
			backoff = c.cfg.ReconnectMaxInterval
		}
	}
}

// currentCreds returns a copy of the credentials under credsMu.
// Nil means no credential entry has arrived.
func (c *SSHConsole) currentCreds() *compcredentials.CompCredentials {
	c.credsMu.RLock()
	defer c.credsMu.RUnlock()
	return cloneCreds(c.creds)
}

// cloneCreds copies a nil-able credential so the original cannot be aliased.
func cloneCreds(creds *compcredentials.CompCredentials) *compcredentials.CompCredentials {
	if creds == nil {
		return nil
	}
	c := *creds
	return &c
}

// dialClient reads credentials, dials TCP, and completes the SSH handshake.
func (c *SSHConsole) dialClient(ctx context.Context) (*gossh.Client, error) {
	creds := c.currentCreds()
	if creds == nil {
		return nil, errNoCredentials
	}

	auth, err := c.buildAuth(*creds)
	if err != nil {
		return nil, fmt.Errorf("build SSH auth: %w", err)
	}

	port := c.info.ConnectionPort
	if port == 0 {
		port = 22
	}
	addr := fmt.Sprintf("%s:%d", c.info.ConnectionHost, port)

	dialer := &net.Dialer{Timeout: c.cfg.ConnectTimeout}
	if c.cfg.TCPKeepAlive > 0 {
		dialer.KeepAlive = c.cfg.TCPKeepAlive
	}
	netConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	// NewClientConn has no context and ignores ClientConfig.Timeout.
	// Bound the handshake with a deadline and close it on cancellation.
	if err := netConn.SetDeadline(time.Now().Add(c.cfg.ConnectTimeout)); err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("set handshake deadline for %s: %w", addr, err)
	}
	handshakeDone := make(chan struct{})
	defer close(handshakeDone)
	// Cancel the handshake if the context is cancelled
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
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // Matches existing BMC host key behavior
	})
	if err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("SSH handshake with %s: %w", addr, err)
	}

	// Clear the handshake deadline before starting the long-lived session.
	if err := netConn.SetDeadline(time.Time{}); err != nil {
		_ = sshConn.Close()
		return nil, fmt.Errorf("clear handshake deadline for %s: %w", addr, err)
	}

	return gossh.NewClient(sshConn, chans, reqs), nil
}

const (
	terminalRows     = 24
	terminalColumns  = 80
	terminalBaudRate = 115200
)

// startSession opens a PTY session on client, wires up stdout/stdin pipes, and
// starts the shell or entry command. Returns stdout and stdin on success.
func (c *SSHConsole) startSession(client *gossh.Client) (stdout io.Reader, stdin io.WriteCloser, err error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, nil, fmt.Errorf("new SSH session: %w", err)
	}

	if err := session.RequestPty(c.cfg.TerminalType, terminalRows, terminalColumns, gossh.TerminalModes{
		gossh.ECHO:          1,
		gossh.TTY_OP_ISPEED: terminalBaudRate,
		gossh.TTY_OP_OSPEED: terminalBaudRate,
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

	if c.info.ConsoleEntryCommand == "" {
		err = session.Shell()
	} else {
		err = session.Start(c.info.ConsoleEntryCommand)
	}
	if err != nil {
		_ = session.Close()
		return nil, nil, fmt.Errorf("start SSH session: %w", err)
	}

	// Reap the session when it ends to release server-side resources.
	go func() {
		if err := session.Wait(); err != nil {
			slog.Debug("SSH session ended", "nodeID", c.nodeID, "error", err)
		}
	}()

	return stdout, stdin, nil
}

// connect opens a PTY session and stores its connection state.
// The caller drains stdout until EOF or error.
func (c *SSHConsole) connect(ctx context.Context) (io.Reader, error) {
	client, err := c.dialClient(ctx)
	if err != nil {
		return nil, err
	}

	success := false
	defer func() {
		if !success {
			c.connMu.Lock()
			c.sshClient = nil
			c.stdin = nil
			c.connMu.Unlock()
			_ = client.Close()
		}
	}()

	c.connMu.Lock()
	c.sshClient = client
	c.connMu.Unlock()

	stdout, stdin, err := c.startSession(client)
	if err != nil {
		return nil, err
	}

	c.connMu.Lock()
	c.stdin = stdin
	c.connMu.Unlock()

	c.broadcast([]byte(fmt.Sprintf("\n[Console %s connected at %s]\n",
		c.nodeID, time.Now().Format(time.RFC3339))))

	success = true
	return stdout, nil
}

// buildAuth selects authentication for a credential entry.
// Passwords take priority. Empty passwords use the key and optional certificate.
func (c *SSHConsole) buildAuth(creds compcredentials.CompCredentials) ([]gossh.AuthMethod, error) {
	// Password authentication takes priority.
	if creds.Password != "" {
		return []gossh.AuthMethod{gossh.Password(creds.Password)}, nil
	}

	// Use key authentication when the password is empty.
	if c.keyPath == "" {
		return nil, fmt.Errorf("no SSH auth available for %s: no password and no key path configured", c.nodeID)
	}

	keyData, err := os.ReadFile(c.keyPath)
	if err != nil {
		return nil, fmt.Errorf("read SSH key %s: %w", c.keyPath, err)
	}
	signer, err := gossh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parse SSH private key: %w", err)
	}

	certPath := c.keyPath + "-cert.pub"
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

	return []gossh.AuthMethod{gossh.PublicKeys(signer)}, nil
}

// broadcast writes data to the log and attached clients.
// Run is its only caller.
func (c *SSHConsole) broadcast(data []byte) {
	// Apply one pending log rotation signal.
	select {
	case <-c.reopenLogCh:
		if c.logFile != nil {
			_ = c.logFile.Close()
		}
		c.logFormatter.reset()
		var err error
		c.logFile, err = os.OpenFile(c.logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			slog.Error("Failed to reopen SSH console log after rotation", "nodeID", c.nodeID, "error", err)
			c.logFile = nil
		}
	default:
	}

	if c.logFile != nil {
		logData := c.logFormatter.format(data, time.Now())
		if _, err := c.logFile.Write(logData); err != nil {
			slog.Warn("Failed to write to SSH console log", "nodeID", c.nodeID, "error", err)
		}
	}

	c.clientsMu.Lock()
	defer c.clientsMu.Unlock()
	for id, client := range c.clients {
		c.sendToClient(id, client, data)
	}
}

// sendToClient delivers data while tracking dropped bytes.
// Drop notices wait for capacity and precede new data.
// Call with clientsMu held for writing.
func (c *SSHConsole) sendToClient(id string, client *consoleClient, data []byte) {
	if client.dropped > 0 {
		select {
		case client.ch <- dropNotice(c.nodeID, client.dropped):
			slog.Info("SSH console client caught up after dropping output",
				"nodeID", c.nodeID, "clientID", id, "bytesDropped", client.dropped)
			client.dropped = 0
		default:
			// Keep counting while the client is backed up.
			client.dropped += len(data)
			return
		}
	}

	select {
	case client.ch <- append([]byte{}, data...):
	default:
		if client.dropped == 0 {
			// Log only when dropping begins.
			slog.Warn("SSH console client not keeping up, dropping output",
				"nodeID", c.nodeID, "clientID", id)
		}
		client.dropped += len(data)
	}
}

// dropNotice renders the marker injected into a client's stream to account for
// output it never received.
func dropNotice(nodeID string, bytesDropped int) []byte {
	return fmt.Appendf(nil, "\n[Console %s: %d bytes of output dropped, client not keeping up]\n",
		nodeID, bytesDropped)
}

// clientBufferDepth limits queued output for each client.
// It matches ConMan buffering. Each item is up to 32 KB.
const clientBufferDepth = 256

// Attach registers a buffered client channel.
func (c *SSHConsole) Attach(clientID string) chan []byte {
	ch := make(chan []byte, clientBufferDepth)
	c.clientsMu.Lock()
	c.clients[clientID] = &consoleClient{ch: ch}
	c.clientsMu.Unlock()
	return ch
}

// Detach deregisters a client channel. Idempotent.
func (c *SSHConsole) Detach(clientID string) {
	c.clientsMu.Lock()
	defer c.clientsMu.Unlock()
	if client, ok := c.clients[clientID]; ok {
		close(client.ch)
		delete(c.clients, clientID)
	}
}

// Write sends data to the SSH session.
// ErrNotConnected means no input was written during a reconnect.
func (c *SSHConsole) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.connMu.Lock()
	stdin := c.stdin
	c.connMu.Unlock()

	if stdin == nil {
		return 0, ErrNotConnected
	}
	return stdin.Write(p)
}

// UpdateCreds updates stored credentials and forces a reconnect so the new
// credentials take effect.
func (c *SSHConsole) UpdateCreds(creds compcredentials.CompCredentials) {
	c.credsMu.Lock()
	c.creds = &creds
	c.credsMu.Unlock()

	// Wake Run when the first credentials arrive.
	// A buffered signal avoids missed wakeups.
	select {
	case c.credsCh <- struct{}{}:
	default:
	}

	// Close the current connection to trigger reconnect with new creds.
	c.connMu.Lock()
	client := c.sshClient
	c.connMu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

// ReopenLog signals the Run goroutine to reopen the log file (after rotation).
func (c *SSHConsole) ReopenLog() {
	select {
	case c.reopenLogCh <- struct{}{}:
	default: // already signalled
	}
}
