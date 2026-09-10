// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package conman

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cray-HPE/hms-compcredentials"
	"github.com/stretchr/testify/require"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
)

func TestGenerateBaseConfig(t *testing.T) {
	baseDir := "/tmp/conman_test"

	config := DefaultConmanConfig()
	config.BaseConfFilePath = "../../scripts/conman.conf.tmpl"
	config.ConfFilePath = filepath.Join(baseDir, "conman.conf")
	config.LogsPath = filepath.Join(baseDir, "logs")
	config.PidFilePath = filepath.Join(baseDir, "conman.pid")

	baseConfig, err := generateBaseConfig(config)
	require.NoError(t, err)
	require.NotEmpty(t, baseConfig)

	expected := `# UPDATE_CONFIG=TRUE
SERVER keepalive=ON
SERVER logdir="/tmp/conman_test/logs"
SERVER logfile="conman.log"
SERVER loopback=ON
SERVER pidfile="/tmp/conman_test/conman.pid"
SERVER resetcmd="powerman -0 %N; sleep 3; powerman -1 %N"
SERVER timestamp=1h
GLOBAL seropts="115200,8n1"
GLOBAL log="conman/console.%N"
GLOBAL logopts="sanitize,timestamp"
`

	require.Equal(t, []byte(expected), baseConfig)
}

func TestConfigureConman(t *testing.T) {
	tempDir := t.TempDir()

	config := DefaultConmanConfig()
	config.BaseConfFilePath = "../../scripts/conman.conf.tmpl"
	config.ConfFilePath = filepath.Join(tempDir, "conman.conf")
	config.LogsPath = filepath.Join(tempDir, "logs")
	config.PidFilePath = filepath.Join(tempDir, "conman.pid")

	nodes := map[string]*nodes.NodeConsoleInfo{
		"x0c0s1b0": {
			ID:             "x0c0s1b0",
			ConnectionType: nodes.IPMI,
			ConnectionHost: "x0c0s1b0",
		},
		"x0c0s2b0": {
			ID:             "x0c0s2b0",
			ConnectionType: nodes.SSH,
			ConnectionHost: "x0c0s2b0",
			ConnectionPort: 2222,
		},
		"x0c0s3b0": {
			ID:             "x0c0s3b0",
			ConnectionType: nodes.SSH,
			ConnectionHost: "x0c0s3b0",
		},
	}

	passwords := map[string]compcredentials.CompCredentials{
		"x0c0s1b0": {
			Username: "admin",
			Password: "password1",
		},
		"x0c0s2b0": {
			Username: "admin",
			Password: "",
		},
		"x0c0s3b0": {
			Username: "admin",
			Password: "password3",
		},
	}
	service := NewConmanService(config)

	// First call should create the config file
	updated, err := service.ConfigureConman(nodes, passwords)
	require.NoError(t, err)
	require.True(t, updated)

	// Read the generated config file
	generatedConfig, err := os.ReadFile(config.ConfFilePath)
	require.NoError(t, err)
	require.NotEmpty(t, generatedConfig)

	// SSH nodes (x0c0s2b0, x0c0s3b0) are managed by SSHConsoleManager and must
	// not appear in the conman config.
	expected := `# UPDATE_CONFIG=TRUE
SERVER keepalive=ON
SERVER logdir="/logs"
SERVER logfile="conman.log"
SERVER loopback=ON
SERVER pidfile="/conman.pid"
SERVER resetcmd="powerman -0 %N; sleep 3; powerman -1 %N"
SERVER timestamp=1h
GLOBAL seropts="115200,8n1"
GLOBAL log="conman/console.%N"
GLOBAL logopts="sanitize,timestamp"
console name="x0c0s1b0" dev="ipmi:x0c0s1b0" ipmiopts="U:admin,P:password1,W:solpayloadsize"
`
	// Remove temporary directory path from generated config for comparison
	generatedConfigStr := string(generatedConfig)
	generatedConfigStr = strings.ReplaceAll(generatedConfigStr, tempDir, "")

	require.Equal(t, expected, generatedConfigStr)
}

// A node whose credentials have not arrived is left out of the config rather
// than written with empty ones. conmand would take an empty-credential console
// and fail to authenticate on it indefinitely.
func TestConfigureConmanSkipsNodesWithoutCredentials(t *testing.T) {
	tempDir := t.TempDir()

	config := DefaultConmanConfig()
	config.BaseConfFilePath = "../../scripts/conman.conf.tmpl"
	config.ConfFilePath = filepath.Join(tempDir, "conman.conf")
	config.LogsPath = filepath.Join(tempDir, "logs")
	config.PidFilePath = filepath.Join(tempDir, "conman.pid")

	provisioned, waiting := "x0c0s1b0", "x0c0s2b0"
	nodeMap := map[string]*nodes.NodeConsoleInfo{
		provisioned: {ID: provisioned, ConnectionType: nodes.IPMI, ConnectionHost: provisioned},
		waiting:     {ID: waiting, ConnectionType: nodes.IPMI, ConnectionHost: waiting},
	}
	passwords := map[string]compcredentials.CompCredentials{
		provisioned: {Username: "admin", Password: "password1"},
	}

	service := NewConmanService(config)

	hasNodes, err := service.ConfigureConman(nodeMap, passwords)
	require.NoError(t, err)
	require.True(t, hasNodes, "the node that does have credentials is still worth running conman for")

	generatedConfig, err := os.ReadFile(config.ConfFilePath)
	require.NoError(t, err)
	require.Contains(t, string(generatedConfig), `console name="x0c0s1b0"`)
	require.NotContains(t, string(generatedConfig), waiting,
		"a node without credentials should not appear in the config at all")

	// With nothing configurable, runConman must be told there is nothing to run.
	service = NewConmanService(config)
	hasNodes, err = service.ConfigureConman(nodeMap, nil)
	require.NoError(t, err)
	require.False(t, hasNodes, "no node could be configured, so there is no conman to start")
}

func installFakeConmand(t *testing.T, body string) {
	t.Helper()

	binDir := t.TempDir()
	conmandPath := filepath.Join(binDir, "conmand")
	script := "#!/bin/sh\n" + body
	require.NoError(t, os.WriteFile(conmandPath, []byte(script), 0700)) //nolint:gosec // the test command must be executable
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestExecuteConmanClearsStateAfterExit(t *testing.T) {
	installFakeConmand(t, "exit 0\n")

	config := DefaultConmanConfig()
	config.ConfFilePath = filepath.Join(t.TempDir(), "missing-conman.conf")
	service := NewConmanService(config)

	for range 2 {
		require.NoError(t, service.ExecuteConman())

		service.mutex.Lock()
		require.Nil(t, service.command)
		service.mutex.Unlock()
	}
}

func TestSignalConmanTERM(t *testing.T) {
	installFakeConmand(t, "trap 'exit 0' TERM\nwhile :; do sleep 1; done\n")

	tempDir := t.TempDir()
	config := DefaultConmanConfig()
	config.BaseConfFilePath = "../../scripts/conman.conf.tmpl"
	config.ConfFilePath = filepath.Join(tempDir, "conman.conf")
	config.LogsPath = filepath.Join(tempDir, "logs")
	config.PidFilePath = filepath.Join(tempDir, "conman.pid")
	require.NoError(t, os.MkdirAll(filepath.Join(config.LogsPath, "conman"), 0755))

	service := NewConmanService(config)
	nodeID := "x0c0s1b0"
	nodeMap := map[string]*nodes.NodeConsoleInfo{
		nodeID: {
			ID:             nodeID,
			ConnectionType: nodes.IPMI,
			ConnectionHost: "127.0.0.1",
		},
	}
	passwords := map[string]compcredentials.CompCredentials{
		nodeID: {
			Username: "admin",
			Password: "password",
		},
	}
	hasNodes, err := service.ConfigureConman(nodeMap, passwords)
	require.NoError(t, err)
	require.True(t, hasNodes)

	executeDone := make(chan error, 1)
	go func() {
		executeDone <- service.ExecuteConman()
	}()

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		service.mutex.Lock()
		running := service.command != nil && service.command.Process != nil
		service.mutex.Unlock()
		if running {
			break
		}

		select {
		case err := <-executeDone:
			t.Fatalf("conmand exited before it could be signaled: %v", err)
		case <-deadline.C:
			t.Fatal("conmand did not start")
		default:
			runtime.Gosched()
		}
	}
	require.NoError(t, service.SignalConmanTERM())

	select {
	case err := <-executeDone:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("SIGTERM did not stop conmand")
	}
}
