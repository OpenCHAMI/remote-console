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

	// running tracks the live Run goroutines so callers can tell when the last
	// one has released its log file. Cancelling a node's context only asks it
	// to stop; the goroutine may still be opening or writing its log for a
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

// UpdateNodes diffs the provided SSH node map against the currently active set:
// new nodes are started, removed nodes are cancelled, and nodes whose
// connection parameters changed are restarted. Every node that survives the
// diff keeps the credentials it is already using.
//
// ctx must be the long-lived service context — node goroutines run until it is
// cancelled or the node is explicitly stopped.
//
// This is the operation for a caller that could not obtain a complete set of
// credentials, because an absent entry in a passwords map is ambiguous: it
// means "use key auth" for a node with no Vault entry, but it also means
// "Vault did not answer for this node this time", since
// GetPasswordsWithRetries returns a partial map with a nil error once it
// exhausts its retries. Leaving auth alone keeps working nodes working while
// the membership change still takes effect.
//
// Nodes discovered here start with no password and therefore attempt key auth.
// If they are really password nodes they will fail and back off until a later
// successful fetch reaches them through UpdateCredentials.
func (m *SSHConsoleManager) UpdateNodes(
	ctx context.Context,
	sshNodes map[string]*nodes.NodeConsoleInfo,
) error {
	return m.update(ctx, sshNodes, nil, false)
}

// UpdateNodesAndCredentials does everything UpdateNodes does, and additionally
// applies passwords: a node whose credentials changed gets UpdateCreds called,
// which reconnects it under the new ones.
//
// passwords is taken as authoritative — a nodeID absent from it means that node
// has no stored password and should authenticate with the configured SSH key
// (see buildAuth). Only call this when the credential fetch succeeded. A
// partial map from a failed fetch would strip working password nodes down to
// key auth; use UpdateNodes for that case.
func (m *SSHConsoleManager) UpdateNodesAndCredentials(
	ctx context.Context,
	sshNodes map[string]*nodes.NodeConsoleInfo,
	passwords map[string]compcredentials.CompCredentials,
) error {
	return m.update(ctx, sshNodes, passwords, true)
}

// update is the shared implementation. When applyCreds is false, passwords is
// ignored for nodes that already exist and their current credentials are kept.
func (m *SSHConsoleManager) update(
	ctx context.Context,
	sshNodes map[string]*nodes.NodeConsoleInfo,
	passwords map[string]compcredentials.CompCredentials,
	applyCreds bool,
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
		// An absent entry is an empty password, which buildAuth resolves to key
		// auth. That is only a safe reading when the caller vouched for the map.
		creds := passwords[nodeID]

		existing, exists := m.nodes[nodeID]
		if !exists {
			m.startNode(ctx, nodeID, info, creds)
			continue
		}

		if nodeChanged(existing.info, info) {
			// Connection parameters changed — restart. Keep the credentials the
			// node already has when this update is not allowed to set them.
			slog.Info("SSH console node parameters changed, restarting", "nodeID", nodeID)
			if !applyCreds {
				creds = existing.currentCreds()
			}
			m.cancels[nodeID]()
			m.startNode(ctx, nodeID, info, creds)
			continue
		}

		if applyCreds && credsChanged(existing.currentCreds(), creds) {
			// Credentials only — reconnect in place without full restart.
			slog.Info("SSH console node credentials changed, reconnecting", "nodeID", nodeID)
			existing.UpdateCreds(creds)
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

// Wait blocks until all console goroutines have stopped. The caller must first
// cancel the context passed to UpdateNodes or remove every managed node.
func (m *SSHConsoleManager) Wait() {
	m.running.Wait()
}

// UpdateCredentials updates credentials for nodes whose passwords have changed.
// Nodes that changed connection info should use UpdateNodesAndCredentials instead.
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

// Attach connects an interactive client to an SSH console node.
// Returns an error if the node is not managed by this manager.
func (m *SSHConsoleManager) Attach(nodeID, clientID string) (chan []byte, error) {
	m.nodesMu.RLock()
	node, ok := m.nodes[nodeID]
	m.nodesMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("SSH console node %q not found", nodeID)
	}
	return node.Attach(clientID), nil
}

// Detach disconnects a client from an SSH console node. No-op if not found.
func (m *SSHConsoleManager) Detach(nodeID, clientID string) {
	m.nodesMu.RLock()
	node, ok := m.nodes[nodeID]
	m.nodesMu.RUnlock()
	if ok {
		node.Detach(clientID)
	}
}

// Write sends data to the SSH session stdin for a node. It returns an error if
// the node is not managed by this manager, and ErrNotConnected if it is managed
// but currently disconnected.
func (m *SSHConsoleManager) Write(nodeID string, p []byte) (int, error) {
	m.nodesMu.RLock()
	node, ok := m.nodes[nodeID]
	m.nodesMu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("SSH console node %q not found", nodeID)
	}
	return node.Write(p)
}
