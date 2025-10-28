// MIT License
// (C) Copyright 2023-2024 Hewlett Packard Enterprise Development LP
//
// This file contains the functions to monitor for changes in keys and certs

package creds

import (
	"fmt"
	"log"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
)

type SignalConmanTERM func()

// Time to wait between checking for credential changes
var MonitorIntervalSecs int = 30

// function to do check for credential changes and restart conman if necessary
func CheckForUpdates(config CredsConfig) (bool, error) {
	restartConman := false

	var xnames []string = nil
	sshKeyAuth := false

	currentNodes := nodes.CurrentNodes()
	for _, nci := range currentNodes {
		if nci.IsIPMI() || nci.IsPassSSH() {
			xnames = append(xnames, nci.BmcName)
		} else if nci.IsKeySSH() {
			sshKeyAuth = true
		}
	}

	changed, err := checkIfKeysChanged(config)
	if err != nil {
		return false, err
	}

	restartConman = sshKeyAuth && changed

	changed, err = checkIfPasswordsChanged(config, xnames)
	if err != nil {
		return false, err
	}

	restartConman = len(xnames) > 0 && changed || restartConman

	return restartConman, nil
}

func checkIfPasswordsChanged(config CredsConfig, xnames []string) (bool, error) {
	if previousPasswords == nil {
		fmt.Printf("No previous passwords stored, cannot check for changes\n")
		return false, nil
	}
	currentPasswords, err := getPasswords(config, xnames)

	if err != nil {
		log.Printf("Error retrieving passwords while checking for credential changes: %v", err)
		return false, err
	}
	for _, xname := range xnames {
		currentCreds, ok := currentPasswords[xname]
		if !ok {
			log.Printf("Missing credentials detected for %s while checking for credential changes", xname)
			continue
		}
		previousCreds, _ := previousPasswords[xname]

		if (currentCreds.Username != previousCreds.Username) || (currentCreds.Password != previousCreds.Password) {
			log.Printf("Change detected in the passwords.  Conman will be reconfigured.")
			return true, nil
		}
	}

	return false, nil
}

func checkIfKeysChanged(config CredsConfig) (bool, error) {
	return EnsureConsoleKeysPresent(config)
}
