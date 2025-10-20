// MIT License
// (C) Copyright 2023-2024 Hewlett Packard Enterprise Development LP
//
// This file contains the functions to monitor for changes in keys and certs

package creds

import (
    "log"
    "sync"
    "time"
    "github.com/OpenCHAMI/remote-console/internal/types"
)// Time to wait between checking for credential changes
var MonitorIntervalSecs int = 30

// function to do check for credential changes and restart conman if necessary
func checkForChanges(currNodesMutex *sync.Mutex, currentNodes map[string]*types.NodeConsoleInfo, signalConmanTERM func()) {
	restartConman := false

	var xnames []string = nil
	sshKeyAuth := false

	currNodesMutex.Lock()
	{
		defer currNodesMutex.Unlock()

		for _, nci := range currentNodes {
			if nci.IsIPMI() || nci.IsPassSSH() {
				xnames = append(xnames, nci.BmcName)
			} else if nci.IsKeySSH() {
				sshKeyAuth = true
			}
		}
	}

	if sshKeyAuth && checkIfKeysChanged(currNodesMutex) {
		restartConman = true
	}

	if len(xnames) > 0 && checkIfPasswordsChanged(xnames) {
		restartConman = true
	}

	if restartConman {
		signalConmanTERM()
	}
}

// function to continuously monitor for changes that require conman to restart
func CredMonitor(currNodesMutex *sync.Mutex, currentNodes map[string]*types.NodeConsoleInfo, signalConmanTERM func()) {
	time.Sleep(time.Duration(MonitorIntervalSecs) * time.Second)
	for {
		checkForChanges(currNodesMutex, currentNodes, signalConmanTERM)
		time.Sleep(time.Duration(MonitorIntervalSecs) * time.Second)
	}
}

func checkIfPasswordsChanged(xnames []string) bool {
	prevPasswords := GetPreviousPasswords()
	if prevPasswords == nil {
		return false
	}
	currentPasswords := GetPasswords(xnames)
	for _, xname := range xnames {
		currentCreds, ok := currentPasswords[xname]
		if !ok {
			log.Printf("Missing credentials detected for %s while checking for credential changes", xname)
			continue
		}
		previousCreds, _ := prevPasswords[xname]
		if (currentCreds.Username != previousCreds.Username) || (currentCreds.Password != previousCreds.Password) {
			log.Printf("Change detected in the river passwords.  Conman will be reconfigured.")
			return true
		}
	}
	return false
}

func checkIfKeysChanged(currNodesMutex *sync.Mutex) bool {
	currNodesMutex.Lock()
	defer currNodesMutex.Unlock()
	return EnsureConsoleKeysPresent()
}
