//
//  MIT License
//
//  (C) Copyright 2020-2022, 2024 Hewlett Packard Enterprise Development LP
//
//  Permission is hereby granted, free of charge, to any person obtaining a
//  copy of this software and associated documentation files (the "Software"),
//  to deal in the Software without restriction, including without limitation
//  the rights to use, copy, modify, merge, publish, distribute, sublicense,
//  and/or sell copies of the Software, and to permit persons to whom the
//  Software is furnished to do so, subject to the following conditions:
//
//  The above copyright notice and this permission notice shall be included
//  in all copies or substantial portions of the Software.
//
//  THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
//  IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
//  FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL
//  THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR
//  OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE,
//  ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR
//  OTHER DEALINGS IN THE SOFTWARE.
//

// This file contains the functions to configure and retrieve credentials

package console

import (
	"log"
	"time"

	compcreds "github.com/Cray-HPE/hms-compcredentials"
	sstorage "github.com/Cray-HPE/hms-securestorage"
)

// Location of the Mountain BMC console ssh key pair files.
// These are obtained or generated by console-operator.
const sshConsoleKey string = "/var/log/console/conman.key"
const sshConsoleKeyPub string = "/var/log/console/conman.key.pub"

// Look up the creds for the input endpoints with retries
func getPasswordsWithRetries(bmcXNames []string, maxTries, waitSecs int) map[string]compcreds.CompCredentials {
	// NOTE: in update config thread

	var passwords map[string]compcreds.CompCredentials = nil
	for numTries := 0; numTries < maxTries; numTries++ {
		log.Printf("Get passwords with retry: %d", numTries)
		// get passwords from vault
		passwords = getPasswords(bmcXNames)

		// make sure we have something for all entries
		foundAll := true
		for _, nn := range bmcXNames {
			_, ok := passwords[nn]
			if !ok {
				log.Printf("Missing credentials for %s", nn)
				foundAll = false
			}
		}

		// if we got all the passwords we are done
		if foundAll {
			log.Printf("Retrieved all passwords")
			return passwords
		}

		// if we did not get all passwords try again until maxAttempts
		log.Printf("Attempt %d - Only retrieved %d of %d River creds from vault, waiting and trying again...",
			numTries, len(passwords), len(bmcXNames))
		time.Sleep(time.Duration(waitSecs) * time.Second)
	}

	// We have reached max attempts, bail with what we have
	log.Printf("Maximum password attempts reached, configuring conman with what we have.")
	return passwords
}

// Look up the creds for the input endpoints
func getPasswords(bmcXNames []string) map[string]compcreds.CompCredentials {
	// NOTE: in update config thread

	// if running in debug mode, skip hsm query
	if DebugOnly {
		log.Print("DEBUGONLY mode - skipping creds query")
		return nil
	}

	// Get the passwords from Hashicorp Vault
	log.Print("Gathering creds from vault")

	// Create the Vault adapter and connect to Vault
	ss, err := sstorage.NewVaultAdapter("secret")
	if err != nil {
		log.Panicf("Error: %#v\n", err)
	}

	// Initialize the CompCredStore struct with the Vault adapter.
	ccs := compcreds.NewCompCredStore("hms-creds", ss)

	// Read the credentials for a list of components from the CompCredStore
	// (backed by Vault).
	ccreds, err := ccs.GetCompCreds(bmcXNames)
	if err != nil {
		log.Panicf("Error: %#v\n", err)
	}

	return ccreds
}
