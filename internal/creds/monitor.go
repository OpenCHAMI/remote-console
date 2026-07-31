// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// Copyright © 2023-2024 Hewlett Packard Enterprise Development LP
//
// SPDX-License-Identifier: MIT

// This file contains the functions to monitor for changes in keys and certs

package creds

import (
	"log/slog"

	compcreds "github.com/Cray-HPE/hms-compcredentials"
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

// checkIfPasswordsChanged refreshes the credential snapshot and reports changes.
func (cs *CredsService) checkIfPasswordsChanged(xnames []string) (bool, error) {
	if len(xnames) == 0 {
		// Preserve the snapshot until inventory is available.
		return false, nil
	}

	currentPasswords, err := getPasswords(cs.config, xnames)
	if err != nil {
		// A failed read does not invalidate the previous snapshot.
		slog.Error("Error retrieving passwords while checking for credential changes", "error", err)
		return false, err
	}

	cs.passwordsMu.Lock()
	defer cs.passwordsMu.Unlock()

	// Retain previous credentials when a read omits an inventoried node.
	refreshed := make(map[string]compcreds.CompCredentials, len(xnames))
	for _, xname := range xnames {
		if creds, ok := currentPasswords[xname]; ok {
			refreshed[xname] = creds
			continue
		}
		if previous, seen := cs.passwords[xname]; seen {
			refreshed[xname] = previous
		}
	}

	if cs.passwords == nil {
		// Report a non-empty first read so ConMan receives initial credentials.
		cs.passwords = refreshed
		return len(refreshed) > 0, nil
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

	cs.passwords = refreshed

	return changed, nil
}

func (cs *CredsService) checkIfKeysChanged() (bool, error) {
	return cs.EnsureConsoleKeysPresent()
}
