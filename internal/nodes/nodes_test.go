// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package nodes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func resetCurrentNodes() {
	currNodesMutex.Lock()
	defer currNodesMutex.Unlock()
	currentNodes = make(map[string]*NodeConsoleInfo)
}

func TestUpdateNodesAdd(t *testing.T) {
	resetCurrentNodes()

	newNodes := []NodeConsoleInfo{
		{
			ID:             "x0c0s1b0",
			ConnectionType: SSH,
			ConnectionHost: "x0c0s1b0",
			ConnectionPort: 22,
		},
		{
			ID:             "x0c0s1b2",
			ConnectionType: IPMI,
			ConnectionHost: "x0c0s1b2",
			ConnectionPort: 623,
		},
	}

	changed := updateNodes(newNodes)
	require.True(t, changed, "expected changes when nodes are added")

	currNodesMutex.Lock()
	defer currNodesMutex.Unlock()
	require.Equal(t, 2, len(currentNodes), "There should be 2 nodes after update")
	require.Contains(t, currentNodes, "x0c0s1b0")
	require.Contains(t, currentNodes, "x0c0s1b2")
	require.Equal(t, "x0c0s1b0", currentNodes["x0c0s1b0"].ConnectionHost)
	require.Equal(t, 22, currentNodes["x0c0s1b0"].ConnectionPort)
}

func TestUpdateNodesUpdate(t *testing.T) {
	resetCurrentNodes()

	currNodesMutex.Lock()
	currentNodes["x0c0s1b0"] = &NodeConsoleInfo{
		ID:             "x0c0s1b0",
		ConnectionType: SSH,
		ConnectionHost: "x0c0s1b0",
		ConnectionPort: 22,
	}
	currNodesMutex.Unlock()

	newNodes := []NodeConsoleInfo{
		{
			ID:             "x0c0s1b0",
			ConnectionType: SSH,
			ConnectionHost: "x0c0s1b0",
			ConnectionPort: 2222,
		},
	}

	changed := updateNodes(newNodes)
	require.True(t, changed, "expected changes when nodes are updated")

	currNodesMutex.Lock()
	defer currNodesMutex.Unlock()
	require.Equal(t, 1, len(currentNodes))
	require.Equal(t, "x0c0s1b0", currentNodes["x0c0s1b0"].ConnectionHost)
	require.Equal(t, 2222, currentNodes["x0c0s1b0"].ConnectionPort)
}

func TestUpdateNodesRemove(t *testing.T) {
	resetCurrentNodes()

	currNodesMutex.Lock()
	currentNodes["x0c0s1b0"] = &NodeConsoleInfo{
		ID:             "x0c0s1b0",
		ConnectionType: SSH,
		ConnectionHost: "x0c0s1b0",
		ConnectionPort: 22,
	}
	currentNodes["x0c0s1b1"] = &NodeConsoleInfo{
		ID:             "x0c0s1b1",
		ConnectionType: IPMI,
		ConnectionHost: "x0c0s1b1",
		ConnectionPort: 623,
	}
	currNodesMutex.Unlock()

	newNodes := []NodeConsoleInfo{
		{
			ID:             "x0c0s1b0",
			ConnectionType: SSH,
			ConnectionHost: "x0c0s1b0",
			ConnectionPort: 22,
		},
	}

	changed := updateNodes(newNodes)
	require.True(t, changed, "expected changes when nodes are removed")

	currNodesMutex.Lock()
	defer currNodesMutex.Unlock()
	require.Equal(t, 1, len(currentNodes))
	require.Contains(t, currentNodes, "x0c0s1b0")
	require.NotContains(t, currentNodes, "x0c0s1b1")
}

func TestUpdateNodesNoChange(t *testing.T) {
	resetCurrentNodes()

	currNodesMutex.Lock()
	currentNodes["x0c0s1b0"] = &NodeConsoleInfo{
		ID:             "x0c0s1b0",
		ConnectionType: SSH,
		ConnectionHost: "x0c0s1b0",
		ConnectionPort: 22,
	}
	currentNodes["x0c0s1b1"] = &NodeConsoleInfo{
		ID:             "x0c0s1b1",
		ConnectionType: IPMI,
		ConnectionHost: "x0c0s1b1",
		ConnectionPort: 623,
	}
	currNodesMutex.Unlock()

	newNodes := []NodeConsoleInfo{
		{
			ID:             "x0c0s1b0",
			ConnectionType: SSH,
			ConnectionHost: "x0c0s1b0",
			ConnectionPort: 22,
		},
		{
			ID:             "x0c0s1b1",
			ConnectionType: IPMI,
			ConnectionHost: "x0c0s1b1",
			ConnectionPort: 623,
		},
	}

	changed := updateNodes(newNodes)
	require.False(t, changed, "expected no changes when data is identical")

	currNodesMutex.Lock()
	defer currNodesMutex.Unlock()
	require.Equal(t, 2, len(currentNodes))
	require.Contains(t, currentNodes, "x0c0s1b0")
	require.Contains(t, currentNodes, "x0c0s1b1")
}

func TestCurrentNode(t *testing.T) {
	resetCurrentNodes()
	t.Cleanup(resetCurrentNodes)

	updateNodes([]NodeConsoleInfo{
		{ID: "ipmi", ConnectionType: IPMI},
		{ID: "ssh", ConnectionType: SSH},
	})

	node := CurrentNode("ipmi")
	require.NotNil(t, node)
	require.Equal(t, IPMI, node.ConnectionType)
	require.Nil(t, CurrentNode("missing"))

	node.ConnectionType = SSH
	require.Equal(t, IPMI, CurrentNode("ipmi").ConnectionType)
}

func mockSMDServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/hsm/v2/Inventory/ComponentEndpoints", r.URL.Path)
		_, _ = w.Write([]byte(`{"ComponentEndpoints":[
			{"ID":"x0c0s0b0n0","Enabled":true,"RedfishEndpointFQDN":"bmc0",
			 "RedfishSystemInfo":{"SerialConsole":{"SSH":{
			 "ServiceEnabled":true,"Port":2222,"ConsoleEntryCommand":"original"}}}},
			{"ID":"x0c0s0b1n0","Enabled":true,"RedfishEndpointFQDN":"bmc1"},
			{"ID":"x0c0s0b2n0","Enabled":false,"RedfishEndpointFQDN":"bmc2"}
		]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestConsoleOverrides(t *testing.T) {
	server := mockSMDServer(t)
	original := NodeConsoleInfo{
		ID: "x0c0s0b0n0", ConnectionType: SSH, ConnectionHost: "bmc0",
		ConnectionPort: 2222, ConsoleEntryCommand: "original",
	}
	tests := []struct {
		name    string
		config  string
		want    []NodeConsoleInfo
		wantErr string
	}{
		{name: "no file", want: []NodeConsoleInfo{original}},
		{name: "command override retains discovery", config: `{"x0c0s0b0n0":{"consoleEntryCommand":"console 0"}}`,
			want: []NodeConsoleInfo{{ID: original.ID, ConnectionType: SSH, ConnectionHost: "bmc0", ConnectionPort: 2222, ConsoleEntryCommand: "console 0"}}},
		{name: "explicit empty command", config: `{"x0c0s0b0n0":{"consoleEntryCommand":""}}`,
			want: []NodeConsoleInfo{{ID: original.ID, ConnectionType: SSH, ConnectionHost: "bmc0", ConnectionPort: 2222}}},
		{name: "missing metadata", config: `{"x0c0s0b1n0":{"connectionType":"ssh","consoleEntryCommand":"console 1"}}`,
			want: []NodeConsoleInfo{original, {ID: "x0c0s0b1n0", ConnectionType: SSH, ConnectionHost: "bmc1", ConsoleEntryCommand: "console 1"}}},
		{name: "connection override", config: `{"x0c0s0b0n0":{"connectionType":"ipmi","connectionHost":"other-bmc","connectionPort":0,"consoleEntryCommand":""}}`,
			want: []NodeConsoleInfo{{ID: original.ID, ConnectionType: IPMI, ConnectionHost: "other-bmc"}}},
		{name: "disabled and absent endpoints ignored", config: `{"x0c0s0b2n0":{"connectionType":"ssh"},"absent":{"connectionType":"ssh"}}`,
			want: []NodeConsoleInfo{original}},
		{name: "malformed file", config: `{`, wantErr: "unable to decode console overrides"},
		{name: "invalid field type", config: `{"x0c0s0b0n0":{"connectionPort":"bad"}}`, wantErr: "invalid console override"},
		{name: "missing connection type", config: `{"x0c0s0b1n0":{"consoleEntryCommand":"console 1"}}`, wantErr: "requires connectionType"},
		{name: "unsupported connection type", config: `{"x0c0s0b0n0":{"connectionType":"telnet"}}`, wantErr: "requires connectionType"},
		{name: "empty host", config: `{"x0c0s0b0n0":{"connectionHost":""}}`, wantErr: "invalid host or port"},
		{name: "invalid port", config: `{"x0c0s0b0n0":{"connectionPort":65536}}`, wantErr: "invalid host or port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var path string
			if tt.config != "" {
				path = filepath.Join(t.TempDir(), "consoles.json")
				require.NoError(t, os.WriteFile(path, []byte(tt.config), 0600))
			}
			got, err := currentNodesFromSMD(context.Background(), server.Client(), server.URL+"/", path)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestConsoleOverridesRefresh(t *testing.T) {
	resetCurrentNodes()
	t.Cleanup(resetCurrentNodes)
	server := mockSMDServer(t)
	path := filepath.Join(t.TempDir(), "consoles.json")
	refresh := func(config string) bool {
		t.Helper()
		require.NoError(t, os.WriteFile(path, []byte(config), 0600))
		return CheckForUpdates(context.Background(), server.Client(), server.URL+"/", path)
	}

	require.True(t, refresh(`{"x0c0s0b1n0":{"connectionType":"ssh","consoleEntryCommand":"console 1"}}`))
	require.False(t, refresh(`{"x0c0s0b1n0":{"connectionType":"ssh","consoleEntryCommand":"console 1"}}`))
	require.True(t, refresh(`{"x0c0s0b1n0":{"connectionType":"ssh","consoleEntryCommand":"console 2"}}`))
	require.Equal(t, "console 2", CurrentNode("x0c0s0b1n0").ConsoleEntryCommand)

	previous := CurrentNodes()
	require.False(t, refresh(`{`))
	require.Equal(t, previous, CurrentNodes())
	require.False(t, CheckForUpdates(context.Background(), server.Client(), server.URL+"/", path+".missing"))
	require.Equal(t, previous, CurrentNodes())

	require.True(t, refresh(`{}`))
	require.Nil(t, CurrentNode("x0c0s0b1n0"))
	require.Equal(t, "original", CurrentNode("x0c0s0b0n0").ConsoleEntryCommand)
}
