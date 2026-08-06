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

// ErrNotConnected means a managed node is temporarily disconnected. Write discards input and
// returns this error until reconnect.
var ErrNotConnected = errors.New("ssh console node not connected")

// SSHConsoleManager manages one console for each SSH node.
type SSHConsoleManager struct {
	cfg      SSHConfig
	keyPath  string
	logsPath string // e.g. /var/log/conman/conman

	// consolesMu protects consoles.
	consolesMu sync.RWMutex
	consoles   map[string]*SSHConsole
	cancels map[string]context.CancelFunc

	// running tracks Run goroutines through final cleanup.
	running sync.WaitGroup
}

// NewSSHConsoleManager creates a manager from validated configuration. keyPath may be empty when
// all nodes use password authentication. logsPath stores per-node console logs.
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

// UpdateNodes syncs managed consoles with the provided SSH node set. It starts new SSH nodes and
// stops removed nodes. Connection changes restart a console with its existing
// credentials, while new nodes wait for UpdateCredentials before connecting. ctx controls
// console lifetime.
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

// startConsole creates and starts one console. Call with consolesMu held. Nil credentials delay
// connection.
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

// Wait blocks until all Run goroutines exit but does not stop them. Callers cancel their contexts
// first and then wait for log cleanup.
func (m *SSHConsoleManager) Wait() {
	m.running.Wait()
}

// UpdateCredentials reconnects managed nodes whose credentials changed. UpdateNodes handles
// membership and never changes authentication. Missing entries leave existing credentials
// unchanged because reads may be partial, and entries for unmanaged nodes are ignored. An empty
// password selects key authentication.
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

// Attach connects a client and fails if the node is unmanaged.
func (m *SSHConsoleManager) Attach(nodeID, clientID string) (chan []byte, error) {
	m.consolesMu.RLock()
	console, ok := m.consoles[nodeID]
	m.consolesMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("SSH console node %q not found", nodeID)
	}
	return console.Attach(clientID), nil
}

// Detach disconnects a client and ignores unmanaged nodes.
func (m *SSHConsoleManager) Detach(nodeID, clientID string) {
	m.consolesMu.RLock()
	console, ok := m.consoles[nodeID]
	m.consolesMu.RUnlock()
	if ok {
		console.Detach(clientID)
	}
}

// Write sends data to a node and distinguishes unmanaged nodes from temporary disconnections.
func (m *SSHConsoleManager) Write(nodeID string, p []byte) (int, error) {
	m.consolesMu.RLock()
	console, ok := m.consoles[nodeID]
	m.consolesMu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("SSH console node %q not found", nodeID)
	}
	return console.Write(p)
}
