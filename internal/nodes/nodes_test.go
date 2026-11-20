// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package nodes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
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

type smdRoundTripper func(*http.Request) (*http.Response, error)

func (f smdRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

const smdSnapshotResponse = `{"ComponentEndpoints":[{"ID":"x0c0s2b0","Enabled":true,"RedfishEndpointFQDN":"bmc.example","RedfishSystemInfo":{"SerialConsole":{"SSH":{"ServiceEnabled":true,"Port":22}}}}]}`

func TestCheckForUpdatesPreservesSnapshotOnFailure(t *testing.T) {
	testCases := []struct {
		name   string
		status int
		body   string
		err    error
	}{
		{name: "transport error", err: errors.New("SMD unavailable")},
		{name: "service unavailable", status: http.StatusServiceUnavailable, body: `{}`},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`},
		{name: "error with inventory", status: http.StatusServiceUnavailable, body: smdSnapshotResponse},
		{name: "partial content", status: http.StatusPartialContent, body: smdSnapshotResponse},
		{name: "missing inventory", status: http.StatusOK, body: `{}`},
		{name: "null response", status: http.StatusOK, body: `null`},
		{name: "null inventory", status: http.StatusOK, body: `{"ComponentEndpoints":null}`},
		{name: "invalid inventory type", status: http.StatusOK, body: `{"ComponentEndpoints":{}}`},
		{name: "truncated inventory", status: http.StatusOK, body: strings.TrimSuffix(smdSnapshotResponse, "]}") + ","},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			resetCurrentNodes()
			t.Cleanup(resetCurrentNodes)
			updateNodes([]NodeConsoleInfo{
				{ID: "x0c0s1b0", ConnectionType: SSH, ConnectionHost: "ssh-bmc", ConnectionPort: 22},
				{ID: "x0c0s1b1", ConnectionType: IPMI, ConnectionHost: "ipmi-bmc", ConnectionPort: 623},
			})
			before := CurrentNodes()
			requests := 0
			client := &http.Client{Transport: smdRoundTripper(func(req *http.Request) (*http.Response, error) {
				requests++
				require.Equal(t, http.MethodGet, req.Method)
				require.Equal(t, "/hsm/v2/Inventory/ComponentEndpoints", req.URL.Path)
				if testCase.err != nil {
					return nil, testCase.err
				}
				return &http.Response{StatusCode: testCase.status, Body: io.NopCloser(strings.NewReader(testCase.body))}, nil
			})}

			require.False(t, CheckForUpdates(context.Background(), client, "http://smd/"))
			require.Equal(t, before, CurrentNodes())
			require.Equal(t, 1, requests, "refresh should use one bulk request")
		})
	}
}

func TestCheckForUpdatesRefreshesSnapshot(t *testing.T) {
	resetCurrentNodes()
	t.Cleanup(resetCurrentNodes)
	updateNodes([]NodeConsoleInfo{{ID: "x0c0s1b0", ConnectionType: IPMI}})

	body := smdSnapshotResponse
	requests := 0
	client := &http.Client{Transport: smdRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests++
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "/hsm/v2/Inventory/ComponentEndpoints", req.URL.Path)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	require.True(t, CheckForUpdates(context.Background(), client, "http://smd/"))
	require.Equal(t, map[string]*NodeConsoleInfo{
		"x0c0s2b0": {ID: "x0c0s2b0", ConnectionType: SSH, ConnectionHost: "bmc.example", ConnectionPort: 22},
	}, CurrentNodes())
	require.False(t, CheckForUpdates(context.Background(), client, "http://smd/"), "identical inventory should not report changes")

	body = `{"ComponentEndpoints":[]}`
	require.True(t, CheckForUpdates(context.Background(), client, "http://smd/"))
	require.Empty(t, CurrentNodes(), "a valid empty inventory should remove previous nodes")
	require.Equal(t, 3, requests, "each refresh should use one bulk request")
}
