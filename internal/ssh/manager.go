// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/Cray-HPE/hms-compcredentials"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
)

// ErrNotConnected is returned by Write when the node exists but its SSH
// session is currently down — during the initial connect, or between a drop
// and the next retry. The input is discarded. It is deliberately distinct from
// the "node not found" error: not-found is permanent and worth failing on,
// whereas this clears on its own once the node reconnects.
var ErrNotConnected = errors.New("ssh console node not connected")

// SSHConsoleManager manages the set of active SSHConsoles, one per SSH node in
// the cluster inventory and keyed by node ID.
type SSHConsoleManager struct {
	cfg      SSHConfig
	keyPath  string
	logsPath string // e.g. /var/log/conman/conman

	// consolesMu protects the consoles map.
	// Lock ordering: consolesMu → clientsMu, consolesMu → connMu
	consolesMu sync.RWMutex
	consoles   map[string]*SSHConsole
	// cancels maps nodeID to the cancel func for that console's Run goroutine.
	cancels map[string]context.CancelFunc

	// running tracks the live Run goroutines so callers can tell when the last
	// one has released its log file. Cancelling a console's context only asks
	// it to stop; the goroutine may still be opening or writing its log for a
	// short while afterwards. See Wait.
	running sync.WaitGroup
}

// NewSSHConsoleManager creates a new manager. cfg should be built from
// DefaultSSHConfig and checked with cfg.Validate; the manager does not correct
// bad values. keyPath is the SSH private key path (may be empty if all nodes
// use password auth). logsPath is the directory where per-node console log
// files are written.
func NewSSHConsoleManager(cfg SSHConfig, keyPath, logsPath string) *SSHConsoleManager {
	return &SSHConsoleManager{
		cfg:      cfg,
		keyPath:  keyPath,
		logsPath: logsPath,
		consoles: make(map[string]*SSHConsole),
		cancels:  make(map[string]context.CancelFunc),
	}
}

// logPath returns the log file path for a node.
func (m *SSHConsoleManager) logPath(nodeID string) string {
	return filepath.Join(m.logsPath, "console."+nodeID)
}

// nodeChanged reports whether a node's connection parameters have changed.
func nodeChanged(existing *nodes.NodeConsoleInfo, updated *nodes.NodeConsoleInfo) bool {
	return existing.ConnectionHost != updated.ConnectionHost ||
		existing.ConnectionPort != updated.ConnectionPort ||
		existing.ConsoleEntryCommand != updated.ConsoleEntryCommand
}

// credsChanged reports whether credentials have changed.
func credsChanged(a, b compcredentials.CompCredentials) bool {
	return a.Username != b.Username || a.Password != b.Password
}

// UpdateNodes diffs the provided SSH node map against the currently active set:
// new nodes are started, removed nodes are cancelled, and nodes whose
// connection parameters changed are restarted. Every node that survives the
// diff keeps the credentials it is already using.
//
// ctx must be the long-lived service context — console goroutines run until it
// is cancelled or the node leaves inventory.
//
// Membership is the only thing this decides; authentication belongs to
// UpdateCredentials. A node change reports which nodes exist, and that says
// nothing about credentials, so nothing here needs a credential fetch to
// succeed — or to have happened at all.
//
// A node discovered here starts with no credentials. It registers and accepts
// Attach, but does not connect until UpdateCredentials brings it a Vault entry.
func (m *SSHConsoleManager) UpdateNodes(
	ctx context.Context,
	sshNodes map[string]*nodes.NodeConsoleInfo,
) error {
	m.consolesMu.Lock()
	defer m.consolesMu.Unlock()

	// Stop the consoles of nodes that are no longer in the updated set.
	for nodeID, cancel := range m.cancels {
		if _, stillActive := sshNodes[nodeID]; !stillActive {
			slog.Info("Stopping SSH console", "nodeID", nodeID)
			cancel()
			delete(m.consoles, nodeID)
			delete(m.cancels, nodeID)
		}
	}

	for nodeID, info := range sshNodes {
		existing, exists := m.consoles[nodeID]
		if !exists {
			// nil credentials: the console parks until UpdateCredentials reaches it.
			m.startConsole(ctx, nodeID, info, nil)
			continue
		}

		if nodeChanged(existing.info, info) {
			// Connection parameters changed — replace the console, carrying the
			// credentials the old one was using.
			slog.Info("SSH console node parameters changed, restarting", "nodeID", nodeID)
			creds := existing.currentCreds()
			m.cancels[nodeID]()
			m.startConsole(ctx, nodeID, info, creds)
		}
	}

	return nil
}

// startConsole creates and starts the console for one node. Must be called with
// consolesMu held. A nil creds means none have been fetched for this node yet.
func (m *SSHConsoleManager) startConsole(ctx context.Context, nodeID string, info *nodes.NodeConsoleInfo, creds *compcredentials.CompCredentials) {
	consoleCtx, consoleCancel := context.WithCancel(ctx)
	console := newSSHConsole(nodeID, info, creds, m.keyPath, m.logPath(nodeID), m.cfg, consoleCancel)
	m.consoles[nodeID] = console
	m.cancels[nodeID] = consoleCancel
	slog.Info("Starting SSH console", "nodeID", nodeID, "host", info.ConnectionHost)
	m.running.Add(1)
	go func() {
		defer m.running.Done()
		console.Run(consoleCtx)
	}()
}

// Wait blocks until all console goroutines have stopped. The caller must first
// cancel the context passed to UpdateNodes or remove every managed node.
func (m *SSHConsoleManager) Wait() {
	m.running.Wait()
}

// UpdateCredentials delivers Vault entries to managed nodes, reconnecting any
// node whose entry differs from the one it is using. It is the only way
// credentials enter the manager; UpdateNodes never touches them.
//
// Only node IDs present in passwords are considered. An absent node is left
// alone rather than cleared: a credential that has not arrived is not evidence
// that a node has none, and a fetch can come back short for reasons that have
// nothing to do with the node. A caller may therefore pass whatever it managed
// to fetch without first deciding whether the result is complete.
//
// Moving a node from password to key auth is done by clearing the password on
// its Vault entry, not by deleting the entry — the username is still needed
// either way. That arrives here as a changed entry and is applied normally.
func (m *SSHConsoleManager) UpdateCredentials(passwords map[string]compcredentials.CompCredentials) {
	m.consolesMu.RLock()
	type pending struct {
		console *SSHConsole
		creds   compcredentials.CompCredentials
	}
	var updates []pending
	for nodeID, console := range m.consoles {
		if creds, ok := passwords[nodeID]; ok {
			// A node still waiting for its first entry always counts as changed.
			if current := console.currentCreds(); current == nil || credsChanged(*current, creds) {
				updates = append(updates, pending{console, creds})
			}
		}
	}
	m.consolesMu.RUnlock()

	for _, u := range updates {
		u.console.UpdateCreds(u.creds)
	}
}

// ReopenLogs signals all active consoles to reopen their log files after rotation.
func (m *SSHConsoleManager) ReopenLogs() {
	m.consolesMu.RLock()
	defer m.consolesMu.RUnlock()
	for _, console := range m.consoles {
		console.ReopenLog()
	}
}

// Attach connects an interactive client to an SSH console node.
// Returns an error if the node is not managed by this manager.
func (m *SSHConsoleManager) Attach(nodeID, clientID string) (chan []byte, error) {
	m.consolesMu.RLock()
	console, ok := m.consoles[nodeID]
	m.consolesMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("SSH console node %q not found", nodeID)
	}
	return console.Attach(clientID), nil
}

// Detach disconnects a client from an SSH console node. No-op if not found.
func (m *SSHConsoleManager) Detach(nodeID, clientID string) {
	m.consolesMu.RLock()
	console, ok := m.consoles[nodeID]
	m.consolesMu.RUnlock()
	if ok {
		console.Detach(clientID)
	}
}

// Write sends data to the SSH session stdin for a node. It returns an error if
// the node is not managed by this manager, and ErrNotConnected if it is managed
// but currently disconnected.
func (m *SSHConsoleManager) Write(nodeID string, p []byte) (int, error) {
	m.consolesMu.RLock()
	console, ok := m.consoles[nodeID]
	m.consolesMu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("SSH console node %q not found", nodeID)
	}
	return console.Write(p)
}
