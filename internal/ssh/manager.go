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

// SSHConsoleManager manages the set of active SSHConsoleNodes.
type SSHConsoleManager struct {
	cfg      SSHConfig
	keyPath  string
	logsPath string // e.g. /var/log/conman/conman

	// nodesMu protects the nodes map.
	// Lock ordering: nodesMu → clientsMu, nodesMu → connMu
	nodesMu sync.RWMutex
	nodes   map[string]*SSHConsoleNode
	// cancels maps nodeID to the cancel func for that node's Run goroutine.
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
		nodes:    make(map[string]*SSHConsoleNode),
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

// UpdateNodes syncs managed consoles with the provided SSH node set. It starts new nodes, stops
// removed nodes, and restarts consoles when connection settings change. Existing nodes retain
// their credentials. ctx controls console lifetime. New nodes use key authentication until
// UpdateCredentials supplies a password.
func (m *SSHConsoleManager) UpdateNodes(
	ctx context.Context,
	sshNodes map[string]*nodes.NodeConsoleInfo,
) error {
	m.nodesMu.Lock()
	defer m.nodesMu.Unlock()

	// Stop nodes that are no longer in the updated set.
	for nodeID, cancel := range m.cancels {
		if _, stillActive := sshNodes[nodeID]; !stillActive {
			slog.Info("Stopping SSH console node", "nodeID", nodeID)
			cancel()
			delete(m.nodes, nodeID)
			delete(m.cancels, nodeID)
		}
	}

	for nodeID, info := range sshNodes {
		existing, exists := m.nodes[nodeID]
		if !exists {
			m.startNode(ctx, nodeID, info, compcredentials.CompCredentials{})
			continue
		}

		if nodeChanged(existing.info, info) {
			// Connection parameters changed — restart with the credentials the
			// node is already using.
			slog.Info("SSH console node parameters changed, restarting", "nodeID", nodeID)
			creds := existing.currentCreds()
			m.cancels[nodeID]()
			m.startNode(ctx, nodeID, info, creds)
		}
	}

	return nil
}

// startNode creates and starts a new SSHConsoleNode. Must be called with nodesMu held.
func (m *SSHConsoleManager) startNode(ctx context.Context, nodeID string, info *nodes.NodeConsoleInfo, creds compcredentials.CompCredentials) {
	nodeCtx, nodeCancel := context.WithCancel(ctx)
	node := newSSHConsoleNode(nodeID, info, creds, m.keyPath, m.logPath(nodeID), m.cfg)
	node.cancel = nodeCancel
	m.nodes[nodeID] = node
	m.cancels[nodeID] = nodeCancel
	slog.Info("Starting SSH console node", "nodeID", nodeID, "host", info.ConnectionHost)
	m.running.Add(1)
	go func() {
		defer m.running.Done()
		node.Run(nodeCtx)
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
	m.nodesMu.RLock()
	type pending struct {
		node  *SSHConsoleNode
		creds compcredentials.CompCredentials
	}
	var updates []pending
	for nodeID, node := range m.nodes {
		if creds, ok := passwords[nodeID]; ok {
			if credsChanged(node.currentCreds(), creds) {
				updates = append(updates, pending{node, creds})
			}
		}
	}
	m.nodesMu.RUnlock()

	for _, u := range updates {
		u.node.UpdateCreds(u.creds)
	}
}

// ReopenLogs signals all active nodes to reopen their log files after rotation.
func (m *SSHConsoleManager) ReopenLogs() {
	m.nodesMu.RLock()
	defer m.nodesMu.RUnlock()
	for _, node := range m.nodes {
		node.ReopenLog()
	}
}

// Attach connects a client and fails if the node is unmanaged.
func (m *SSHConsoleManager) Attach(nodeID, clientID string) (chan []byte, error) {
	m.nodesMu.RLock()
	node, ok := m.nodes[nodeID]
	m.nodesMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("SSH console node %q not found", nodeID)
	}
	return node.Attach(clientID), nil
}

// Detach disconnects a client and ignores unmanaged nodes.
func (m *SSHConsoleManager) Detach(nodeID, clientID string) {
	m.nodesMu.RLock()
	node, ok := m.nodes[nodeID]
	m.nodesMu.RUnlock()
	if ok {
		node.Detach(clientID)
	}
}

// Write sends data to a node and distinguishes unmanaged nodes from temporary disconnections.
func (m *SSHConsoleManager) Write(nodeID string, p []byte) (int, error) {
	m.nodesMu.RLock()
	node, ok := m.nodes[nodeID]
	m.nodesMu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("SSH console node %q not found", nodeID)
	}
	return node.Write(p)
}
