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

// ErrNotConnected means a managed console is temporarily disconnected. Write discards input and
// returns this error until reconnect.
var ErrNotConnected = errors.New("ssh console console not connected")

// SSHConsoleManager manages the set of active SSHConsoleNodes.
type SSHConsoleManager struct {
	cfg      SSHConfig
	keyPath  string
	logsPath string // e.g. /var/log/conman/conman

	// consolesMu protects the nodes map.
	// Lock ordering: consolesMu → clientsMu, consolesMu → connMu
	consolesMu sync.RWMutex
	consoles   map[string]*SSHConsole
	// cancels maps nodeID to the cancel func for that console's Run goroutine.
	cancels map[string]context.CancelFunc

	// running tracks Run goroutines through final cleanup.
	running sync.WaitGroup
}

// NewSSHConsoleManager creates a manager from validated configuration. keyPath may be empty when
// all nodes use password authentication. logsPath stores per-console console logs.
func NewSSHConsoleManager(cfg SSHConfig, keyPath, logsPath string) *SSHConsoleManager {
	return &SSHConsoleManager{
		cfg:      cfg,
		keyPath:  keyPath,
		logsPath: logsPath,
		consoles: make(map[string]*SSHConsole),
		cancels:  make(map[string]context.CancelFunc),
	}
}

// logPath returns the log file path for a console.
func (m *SSHConsoleManager) logPath(nodeID string) string {
	return filepath.Join(m.logsPath, "console."+nodeID)
}

// nodeChanged reports whether a console's connection parameters have changed.
func nodeChanged(existing *nodes.NodeConsoleInfo, updated *nodes.NodeConsoleInfo) bool {
	return existing.ConnectionHost != updated.ConnectionHost ||
		existing.ConnectionPort != updated.ConnectionPort ||
		existing.ConsoleEntryCommand != updated.ConsoleEntryCommand
}

// credsChanged reports whether credentials have changed.
func credsChanged(a, b compcredentials.CompCredentials) bool {
	return a.Username != b.Username || a.Password != b.Password
}

// UpdateNodes syncs managed consoles with the provided SSH console set. It starts new nodes, stops
// removed nodes, and restarts consoles when connection settings change. Existing nodes retain
// their credentials. ctx controls console lifetime. New nodes use key authentication until
// UpdateCredentials supplies a password.
func (m *SSHConsoleManager) UpdateNodes(
	ctx context.Context,
	sshNodes map[string]*nodes.NodeConsoleInfo,
) error {
	m.consolesMu.Lock()
	defer m.consolesMu.Unlock()

	// Stop nodes that are no longer in the updated set.
	for nodeID, cancel := range m.cancels {
		if _, stillActive := sshNodes[nodeID]; !stillActive {
			slog.Info("Stopping SSH console console", "nodeID", nodeID)
			cancel()
			delete(m.consoles, nodeID)
			delete(m.cancels, nodeID)
		}
	}

	for nodeID, info := range sshNodes {
		existing, exists := m.consoles[nodeID]
		if !exists {
			m.startConsole(ctx, nodeID, info, compcredentials.CompCredentials{})
			continue
		}

		if nodeChanged(existing.info, info) {
			// Connection parameters changed — restart with the credentials the
			// console is already using.
			slog.Info("SSH console console parameters changed, restarting", "nodeID", nodeID)
			creds := existing.currentCreds()
			m.cancels[nodeID]()
			m.startConsole(ctx, nodeID, info, creds)
		}
	}

	return nil
}

// startConsole creates and starts a new SSHConsole. Must be called with consolesMu held.
func (m *SSHConsoleManager) startConsole(ctx context.Context, nodeID string, info *nodes.NodeConsoleInfo, creds compcredentials.CompCredentials) {
	nodeCtx, nodeCancel := context.WithCancel(ctx)
	console := newSSHConsole(nodeID, info, creds, m.keyPath, m.logPath(nodeID), m.cfg)
	console.cancel = nodeCancel
	m.consoles[nodeID] = console
	m.cancels[nodeID] = nodeCancel
	slog.Info("Starting SSH console console", "nodeID", nodeID, "host", info.ConnectionHost)
	m.running.Add(1)
	go func() {
		defer m.running.Done()
		console.Run(nodeCtx)
	}()
}

// Wait blocks until all Run goroutines exit but does not stop them. Callers cancel their contexts
// first and then wait for log cleanup.
func (m *SSHConsoleManager) Wait() {
	m.running.Wait()
}

// UpdateCredentials updates credentials for managed nodes present in passwords.
// Nodes absent from passwords retain their current credentials.
func (m *SSHConsoleManager) UpdateCredentials(passwords map[string]compcredentials.CompCredentials) {
	m.consolesMu.RLock()
	type pending struct {
		console *SSHConsole
		creds   compcredentials.CompCredentials
	}
	var updates []pending
	for nodeID, console := range m.consoles {
		if creds, ok := passwords[nodeID]; ok {
			if credsChanged(console.currentCreds(), creds) {
				updates = append(updates, pending{console, creds})
			}
		}
	}
	m.consolesMu.RUnlock()

	for _, u := range updates {
		u.console.UpdateCreds(u.creds)
	}
}

// ReopenLogs signals all active nodes to reopen their log files after rotation.
func (m *SSHConsoleManager) ReopenLogs() {
	m.consolesMu.RLock()
	defer m.consolesMu.RUnlock()
	for _, console := range m.consoles {
		console.ReopenLog()
	}
}

// Attach connects a client and fails if the console is unmanaged.
func (m *SSHConsoleManager) Attach(nodeID, clientID string) (chan []byte, error) {
	m.consolesMu.RLock()
	console, ok := m.consoles[nodeID]
	m.consolesMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("SSH console console %q not found", nodeID)
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

// Write sends data to a console and distinguishes unmanaged nodes from temporary disconnections.
func (m *SSHConsoleManager) Write(nodeID string, p []byte) (int, error) {
	m.consolesMu.RLock()
	console, ok := m.consoles[nodeID]
	m.consolesMu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("SSH console console %q not found", nodeID)
	}
	return console.Write(p)
}
