// MIT License
// (C) Copyright 2023-2024 Hewlett Packard Enterprise Development LP
//
// This file contains the functions to monitor for changes in keys and certs

package creds

import (
	"log"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
)

type SignalConmanTERM func ()

// Time to wait between checking for credential changes
var MonitorIntervalSecs int = 30

// function to do check for credential changes and restart conman if necessary
func CheckForUpdates() bool{
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

	log.Printf("sshKeyAuth: %v", sshKeyAuth) 

	if sshKeyAuth && checkIfKeysChanged() {
		restartConman = true
	}

	if len(xnames) > 0 && checkIfPasswordsChanged(xnames) {
		restartConman = true
	}

	return restartConman
}


func checkIfPasswordsChanged(xnames []string) bool {
	if previousPasswords == nil {
		return false
	}
	currentPasswords := GetPasswords(xnames)
	for _, xname := range xnames {
		currentCreds, ok := currentPasswords[xname]
		if !ok {
			log.Printf("Missing credentials detected for %s while checking for credential changes", xname)
			continue
		}
		previousCreds, _ := previousPasswords[xname]
		if (currentCreds.Username != previousCreds.Username) || (currentCreds.Password != previousCreds.Password) {
			log.Printf("Change detected in the river passwords.  Conman will be reconfigured.")
			return true
		}
	}
	return false
}

func checkIfKeysChanged() bool {
	return EnsureConsoleKeysPresent()
}
