// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// Copyright © 2023-2024 Hewlett Packard Enterprise Development LP
//
// SPDX-License-Identifier: MIT

// This file contains the functions to monitor for changes in keys and certs

package creds

import (
	"log/slog"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
)

type SignalConmanTERM func()

// function to do check for credential changes
func (cs *CredsService) CheckForUpdates() (bool, error) {
	currentNodes := nodes.CurrentNodes()
	ids := make([]string, 0, len(currentNodes))
	for _, nci := range currentNodes {
		ids = append(ids, nci.ID)
	}

	keysChanged := false
	// Only check keys if SecureStorageSshKeysPath is configured
	if cs.config.SecureStorageSshKeysPath != "" {
		var err error
		keysChanged, err = cs.checkIfKeysChanged()
		if err != nil {
			return false, err
		}
	}

	passwordsChanged, err := cs.checkIfPasswordsChanged(ids)
	if err != nil {
		return false, err
	}

	return (len(ids) > 0 && passwordsChanged) || keysChanged, nil
}

// checkIfPasswordsChanged refreshes the service's snapshot of the credential
// store and reports whether any of the given nodes' credentials differ from what
// the previous refresh saw.
//
// Refreshing and comparing are one operation on purpose. This is the only place
// that reads the whole node set, so the snapshot it leaves behind always covers
// exactly what the next call will compare — and everything GetPasswords is asked
// for. Were a caller fetching some subset to record its result instead, the nodes
// it left out would go missing from the snapshot and read as changed forever
// after, which is precisely the failure this replaced.
func (cs *CredsService) checkIfPasswordsChanged(xnames []string) (bool, error) {
	if len(xnames) == 0 {
		// Nothing to compare, and nothing worth recording: an empty snapshot
		// would make every node look new once inventory arrives.
		return false, nil
	}

	currentPasswords, err := getPasswords(cs.config, xnames)
	if err != nil {
		// Leave the snapshot alone. A failed fetch says nothing about what is in
		// the store, so overwriting it would either hide a real change or invent
		// one on the next call — and would strand every GetPasswords caller.
		slog.Error("Error retrieving passwords while checking for credential changes", "error", err)
		return false, err
	}

	cs.passwordsMu.Lock()
	defer cs.passwordsMu.Unlock()

	if cs.passwords == nil {
		// First observation: everything read here is news. Reporting nothing
		// would be a hole rather than an economy — conman is rebuilt only when
		// this reports a change, and it starts before this snapshot exists, so
		// staying quiet leaves it running on the credentials it did not have.
		//
		// An empty first read is not news, and claiming otherwise would restart
		// conman for nothing. Recording it is still right: an entry provisioned
		// later is then reported by the loop below.
		cs.passwords = currentPasswords
		return len(currentPasswords) > 0, nil
	}

	changed := false
	for _, xname := range xnames {
		currentCreds, ok := currentPasswords[xname]
		if !ok {
			slog.Warn("Missing credentials detected while checking for credential changes", "xname", xname)
			continue
		}

		previousCreds, seen := cs.passwords[xname]
		if !seen {
			// An entry that has just been provisioned. Absent is not the same as
			// empty: comparing against the zero value here would report every
			// unobserved node as a password change.
			slog.Info("New credentials detected. Conman will be reconfigured.", "xname", xname)
			changed = true
			break
		}

		if (currentCreds.Username != previousCreds.Username) || (currentCreds.Password != previousCreds.Password) {
			slog.Info("Change detected in the passwords. Conman will be reconfigured.")
			changed = true
			break
		}
	}

	// Replace rather than merge, so entries for departed nodes fall away.
	cs.passwords = currentPasswords

	return changed, nil
}

func (cs *CredsService) checkIfKeysChanged() (bool, error) {
	return cs.EnsureConsoleKeysPresent()
}
