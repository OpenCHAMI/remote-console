// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package creds

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Cray-HPE/hms-securestorage"
)

// storeCred writes a credential entry for one xname, as the credential store
// would hold it.
func storeCred(t *testing.T, ss securestorage.SecureStorage, xname, password string) {
	t.Helper()
	value := map[string]string{
		"Username": "admin",
		"Password": password,
		"Xname":    xname,
		"URL":      "https://" + xname + "/redfish/v1/Managers/BMC",
	}
	require.NoError(t, ss.Store(fmt.Sprintf("hms-creds/%s", xname), value))
}

// newTestCredsService returns a CredsService backed by a local secret store
// holding one entry per xname in passwords, along with the store so the test can
// change entries afterwards.
func newTestCredsService(t *testing.T, passwords map[string]string) (*CredsService, securestorage.SecureStorage) {
	t.Helper()

	localStoreFilePath := filepath.Join(t.TempDir(), "secure_store")
	localStoreKey, err := securestorage.GenerateMasterKey()
	require.NoError(t, err)
	ss, err := securestorage.NewLocalSecretStore(localStoreKey, localStoreFilePath, true)
	require.NoError(t, err)

	for xname, password := range passwords {
		storeCred(t, ss, xname, password)
	}

	config := DefaultCredsConfig()
	config.SecureStorageAdapter = StorageAdapterLocal
	config.LocalStoreFilePath = localStoreFilePath
	config.LocalStoreKey = localStoreKey

	return NewCredsService(config), ss
}

func TestCheckIfPasswordsChanged(t *testing.T) {
	xnames := []string{"x0c0s1b0", "x0c0s1b1"}
	service, ss := newTestCredsService(t, map[string]string{
		"x0c0s1b0": "password1",
		"x0c0s1b1": "password2",
	})

	changed, err := service.checkIfPasswordsChanged(xnames)
	require.NoError(t, err)
	require.True(t, changed, "the first check learns every credential, so it reports a change")

	changed, err = service.checkIfPasswordsChanged(xnames)
	require.NoError(t, err)
	require.False(t, changed, "unchanged passwords should not report a change")

	storeCred(t, ss, "x0c0s1b0", "newpassword")

	changed, err = service.checkIfPasswordsChanged(xnames)
	require.NoError(t, err)
	require.True(t, changed, "a changed password should report a change")
}

// GetPasswords answers from the snapshot the check records, and reading it does
// not disturb the snapshot. That is what keeps subset reads honest: runConman
// asks about the IPMI nodes and the credential watcher about the SSH ones, and
// while each of those was a fetch that recorded the baseline, each erased the
// other's half and every tick afterwards reported a change.
func TestGetPasswordsServesTheRecordedSnapshot(t *testing.T) {
	ipmiNode, sshNode := "x0c0s1b0", "x0c0s2b0"
	all := []string{ipmiNode, sshNode}

	service, ss := newTestCredsService(t, map[string]string{
		ipmiNode: "password1",
		sshNode:  "password2",
	})

	require.Empty(t, service.GetPasswords(all), "nothing is known before the first check")

	changed, err := service.checkIfPasswordsChanged(all)
	require.NoError(t, err)
	require.True(t, changed)

	ipmiOnly := service.GetPasswords([]string{ipmiNode})
	require.Len(t, ipmiOnly, 1)
	require.Equal(t, "password1", ipmiOnly[ipmiNode].Password)
	require.Empty(t, service.GetPasswords([]string{"x0c0s9b0"}), "an unknown node is absent, not empty")

	changed, err = service.checkIfPasswordsChanged(all)
	require.NoError(t, err)
	require.False(t, changed, "reading one node's entry is not a change to another's")

	storeCred(t, ss, sshNode, "newpassword")
	require.Equal(t, "password2", service.GetPasswords(all)[sshNode].Password,
		"the snapshot is what the last check read, not what the store holds now")

	changed, err = service.checkIfPasswordsChanged(all)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "newpassword", service.GetPasswords(all)[sshNode].Password,
		"a check that reports a change should leave the new credentials behind")
}

// A first observation that found nothing is not news, and must not restart
// conman for it. Recording the empty read is still right: the entry arriving
// afterwards is what the change is.
func TestFirstObservationWithNoEntriesIsNotAChange(t *testing.T) {
	xnames := []string{"x0c0s1b0"}
	// Another node's entry, so the store exists but holds nothing for xnames.
	service, ss := newTestCredsService(t, map[string]string{"x0c0s9b0": "unrelated"})

	changed, err := service.checkIfPasswordsChanged(xnames)
	require.NoError(t, err)
	require.False(t, changed, "a first read that found no entries is not a change")
	require.NotNil(t, service.passwords, "the empty read should still be recorded")

	storeCred(t, ss, "x0c0s1b0", "password1")

	changed, err = service.checkIfPasswordsChanged(xnames)
	require.NoError(t, err)
	require.True(t, changed, "the entry arriving afterwards is the change")
}

// A failed fetch must leave the baseline alone. Recording its empty result would
// switch detection off until some later fetch happened to succeed.
func TestFailedCheckLeavesBaselineIntact(t *testing.T) {
	xnames := []string{"x0c0s1b0"}
	service, ss := newTestCredsService(t, map[string]string{"x0c0s1b0": "password1"})

	changed, err := service.checkIfPasswordsChanged(xnames)
	require.NoError(t, err)
	require.True(t, changed)

	workingAdapter := service.config.SecureStorageAdapter
	service.config.SecureStorageAdapter = StorageAdapter("bogus")
	changed, err = service.checkIfPasswordsChanged(xnames)
	require.Error(t, err)
	require.False(t, changed)
	service.config.SecureStorageAdapter = workingAdapter

	changed, err = service.checkIfPasswordsChanged(xnames)
	require.NoError(t, err)
	require.False(t, changed, "recovering from a failed fetch is not a credential change")

	storeCred(t, ss, "x0c0s1b0", "newpassword")

	changed, err = service.checkIfPasswordsChanged(xnames)
	require.NoError(t, err)
	require.True(t, changed, "detection should still work after a failed fetch")
}

// An entry appearing after the baseline was recorded is a real change: the node
// was already in inventory but had no credentials yet.
func TestNewlyProvisionedCredentialIsAChange(t *testing.T) {
	existing, provisioned := "x0c0s1b0", "x0c0s1b1"
	xnames := []string{existing, provisioned}

	service, ss := newTestCredsService(t, map[string]string{existing: "password1"})

	changed, err := service.checkIfPasswordsChanged(xnames)
	require.NoError(t, err)
	require.True(t, changed)

	storeCred(t, ss, provisioned, "password2")

	changed, err = service.checkIfPasswordsChanged(xnames)
	require.NoError(t, err)
	require.True(t, changed, "a newly provisioned entry should report a change")
}

// Before inventory arrives there is nothing worth recording. Seeding an empty
// baseline would make every node read as newly provisioned on the next tick.
func TestCheckWithNoNodesDoesNotRecordBaseline(t *testing.T) {
	service, _ := newTestCredsService(t, map[string]string{"x0c0s1b0": "password1"})

	changed, err := service.checkIfPasswordsChanged(nil)
	require.NoError(t, err)
	require.False(t, changed)
	require.Nil(t, service.passwords, "an empty check should not record a snapshot")

	changed, err = service.checkIfPasswordsChanged([]string{"x0c0s1b0"})
	require.NoError(t, err)
	require.True(t, changed, "the empty check should not have counted as the first observation")
}

func TestCheckIfKeysChanged(t *testing.T) {
	tempDir := t.TempDir()

	// Setup a local secure storage file
	localStoreFilePath := filepath.Join(tempDir, "secure_store")
	localStoreKey, err := securestorage.GenerateMasterKey()
	require.NoError(t, err)
	ss, err := securestorage.NewLocalSecretStore(localStoreKey, localStoreFilePath, true)
	require.NoError(t, err)

	config := DefaultCredsConfig()
	config.SecureStorageAdapter = StorageAdapterLocal
	config.LocalStoreFilePath = localStoreFilePath
	config.LocalStoreKey = localStoreKey
	config.SshConsoleKeyPath = filepath.Join(tempDir, "conman.key")
	config.SecureStorageSshKeysPath = "bmc-console-keys"

	// Save test key
	testKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC7..."
	value := map[string]string{
		"PrivateKey": testKey,
	}
	err = ss.Store(config.SecureStorageSshKeysPath, value)
	require.NoError(t, err)

	service := NewCredsService(config)

	changed, err := service.checkIfKeysChanged()
	require.NoError(t, err)
	require.True(t, changed, "Keys should be considered changed on first check")

	// Check again without changing keys
	changed, err = service.checkIfKeysChanged()
	require.NoError(t, err)
	require.False(t, changed, "Keys should not have changed")

	// Now change the key
	newTestKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQD8..."
	value = map[string]string{
		"PrivateKey": newTestKey,
	}
	err = ss.Store(config.SecureStorageSshKeysPath, value)
	require.NoError(t, err)

	changed, err = service.checkIfKeysChanged()
	require.NoError(t, err)
	require.True(t, changed, "Keys should have changed after update")
}
