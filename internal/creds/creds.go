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

// This file contains the functions to configure and retrieve credentials

package creds

import (
	"bytes"
	"crypto/sha256"
	"log"
	"os"
	"time"

	compcreds "github.com/Cray-HPE/hms-compcredentials"
	sstorage "github.com/Cray-HPE/hms-securestorage"
)
// TODO can these be private?
// Also where should the point to!
const SshConsoleKeyPath string = "/var/log/console/conman.key"
const SshConsoleKeyPubCertPath string = "/var/log/console/conman.key-cert.pub"

var PreviousPrivateKeyHash []byte = nil
var PreviousCertHash []byte = nil

// TODO temp for refactor
var DebugOnly bool = false

// Internal state for password tracking
var previousPasswords map[string]compcreds.CompCredentials = nil

type sshKeys struct {
	PrivateKey     string `json:"privateKey"`
	Certificate *string `json:"certificate"`
}

// Look up the creds for the input endpoints with retries
// TODO: I don't think we need this retry logic here any more?
func GetPasswordsWithRetries(bmcXNames []string, maxTries, waitSecs int) map[string]compcreds.CompCredentials {
	var passwords map[string]compcreds.CompCredentials = nil
	for numTries := 0; numTries < maxTries; numTries++ {
		log.Printf("Get passwords with retry: %d", numTries)
		passwords = GetPasswords(bmcXNames)
		log.Printf("Retrieved %v", passwords)

		foundAll := true
		for _, nn := range bmcXNames {
			_, ok := passwords[nn]
			if !ok {
				log.Printf("Missing credentials for %s", nn)
				foundAll = false
			}
		}
		if foundAll {
			log.Printf("Retrieved all passwords")
			return passwords
		}
		log.Printf("Attempt %d - Only retrieved %d of %d River creds from vault, waiting and trying again...",
			numTries, len(passwords), len(bmcXNames))
		time.Sleep(time.Duration(waitSecs) * time.Second)
	}
	log.Printf("Maximum password attempts reached, configuring conman with what we have.")
	return passwords
}

// Look up the creds for the input endpoints
func GetPasswords(bmcXNames []string) map[string]compcreds.CompCredentials {
	// NOTE: in update config thread
	// if running in debug mode, skip hsm query
	// TODO: DebugOnly should be passed in or imported from config
	if DebugOnly {
		log.Print("DEBUGONLY mode - skipping creds query")
		return nil
	}

	log.Print("Gathering creds from vault")
	ss, err := sstorage.NewVaultAdapter("")
	if err != nil {
		log.Panicf("Error: %#v\n", err)
	}

	ccs := compcreds.NewCompCredStore("hms-creds", ss)
	ccreds, err := ccs.GetCompCreds(bmcXNames)
	if err != nil {
		log.Panicf("Error: %#v\n", err)
	}

	previousPasswords = ccreds

	return ccreds
}

func HashString(s string) ([]byte, error) {
	hasher := sha256.New()
	if _, err := hasher.Write([]byte(s)); err != nil {
		return nil, err
	}
	return hasher.Sum(nil), nil
}



func EnsureConsoleKeysPresent() bool {
	retVal := false
	// TODO: DebugOnly should be passed in or imported from config
	// if DebugOnly {
	// 	log.Print("Running in debug mode - skipping mountain cred generation")
	// 	return retVal
	// }

	vaultBasePath := os.Getenv("VAULT_BASE_PATH")
	if len(vaultBasePath) == 0 {
		log.Printf("Warning: VAULT_BASE_PATH environment variable is not set, defaulting to 'secret'")
		vaultBasePath = "secret"
	}
	vaultRole := os.Getenv("VAULT_ROLE")
	if len(vaultRole) == 0 {
		log.Printf("Warning: VAULT_ROLE environment variable is not set, defaulting to ''")
		vaultRole = ""
	}
	consolePrivateKeyName := os.Getenv("CONSOLE_KEYS_NAME")
	if len(consolePrivateKeyName) == 0 {
		log.Printf("Warning: CONSOLE_KEYS_NAME environment variable is not set, defaulting to 'bmc-console-keys'")
		consolePrivateKeyName = "bmc-console-keys"
	}
	ss, err := sstorage.NewVaultAdapterAs(vaultBasePath, vaultRole)
	if err != nil {
		log.Panicf("Error: Unable to create secure storage adapter: %#v\n", err)
	}
	var consoleKeys sshKeys
	err = ss.Lookup(consolePrivateKeyName, &consoleKeys)
	if err != nil {
		log.Panicf("Error: Unable to lookup private key: %#v\n", err)
	}

	log.Printf("consolePrivateKey: %v", consoleKeys.PrivateKey)

	newHash, err := HashString(consoleKeys.PrivateKey)
	if err != nil {
		log.Printf("Error: Failed to hash the private ssh key received from Vault. Err: %s", err)
	} else if PreviousPrivateKeyHash == nil || !(bytes.Equal(newHash, PreviousPrivateKeyHash)) {
		retVal = true
		PreviousPrivateKeyHash = newHash
		err = os.WriteFile(SshConsoleKeyPath, []byte(consoleKeys.PrivateKey), 0600)
		if err != nil {
			log.Printf("Error: Failed to write our the private ssh key received from Vault. Err: %s", err)
			return retVal
		}
		log.Printf("Console ssh key file created")
		return retVal
	} else {
		log.Printf("Console ssh key file already exists")
		return retVal
	}

	if consoleKeys.Certificate != nil {
		newHash, err = HashString(*consoleKeys.Certificate)
		if err != nil {
			log.Printf("Error: Failed to hash the public ssh cert received from Vault. Err: %s", err)
		} else if PreviousCertHash == nil || !(bytes.Equal(newHash, PreviousCertHash)) {
			retVal = true
			PreviousCertHash = newHash
			err = os.WriteFile(SshConsoleKeyPubCertPath, []byte(*consoleKeys.Certificate), 0644)
			if err != nil {
				log.Printf("Error: Failed to write our the public ssh cert received from Vault. Err: %s", err)
				return retVal
			}
			log.Printf("Console ssh cert file created")
			return retVal
		} else {
			log.Printf("Console ssh cert file already exists")
			return retVal
		}
	}

	return retVal
}
