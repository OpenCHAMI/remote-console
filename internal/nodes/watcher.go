//
//  MIT License
//
//  (C) Copyright 2021-2025 Hewlett Packard Enterprise Development LP
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

package nodes

import (
	"log"
	"time"

	"github.com/OpenCHAMI/remote-console/internal/types"
)

// WatcherDeps contains dependencies for the node watcher
type WatcherDeps struct {
	// SignalConmanTERM is called when node changes require conman restart
	SignalConmanTERM func()
	// UpdateLogRotateConf is called when node changes require log rotation config update
	UpdateLogRotateConf func()
	// StopTailing is called to stop tailing logs for a removed node
	StopTailing func(string)
	// NewNodeLookupSec is the interval between polling for node updates
	NewNodeLookupSec int
}

// nodeCache caches current node information
var nodeCache map[string]types.NodeConsoleInfo = make(map[string]types.NodeConsoleInfo)

// newHardwareCheckPeriodSec controls how often we check for hardware changes
var newHardwareCheckPeriodSec int = 120

// hardwareUpdateTime tracks the last hardware update attempt
var hardwareUpdateTime string = "Unknown"

// inShutdown signals we are shutting down and prevent periodic checks from happening
var inShutdown bool = false

// updateCachedNodeData fetches current nodes from HSM and compares with cache
func updateCachedNodeData() (bool, []types.NodeConsoleInfo) {
	// return if the console-data update succeeded
	updateSuccessful := true

	// get the current endpoints from hsm
	currNodes := GetCurrentNodesFromHSM()
	currNodesMap := make(map[string]types.NodeConsoleInfo)
	for _, n := range currNodes {
		currNodesMap[n.NodeName] = n
	}

	// Find new nodes that are in the currNodes but not in nodeCache
	var newNodes []types.NodeConsoleInfo = nil
	for _, n := range currNodes {
		if _, found := nodeCache[n.NodeName]; !found {
			newNodes = append(newNodes, n)
			log.Printf("Found new node: %s", n.String())
		}
	}

	// Find nodes to remove that are in the nodeCache but not in currNodes
	var removedNodes []types.NodeConsoleInfo = nil
	for _, n := range nodeCache {
		if _, found := currNodesMap[n.NodeName]; !found {
			removedNodes = append(removedNodes, n)
			log.Printf("Removing node: %s", n.String())
		}
	}

	// If the data updates succeeded we can update the cache
	if updateSuccessful {
		nodeCache = currNodesMap
	}

	// newNodes are returned, not nodesToUpdate because we only want to deploy
	// mountain keys for new nodes
	return updateSuccessful, newNodes
}

// doHardwareUpdate performs a hardware update check
func doHardwareUpdate() bool {
	// record the time of the hardware update attempt
	hardwareUpdateTime = time.Now().Format(time.RFC3339)

	// Update the cache and data in console-data
	updateSuccessful, _ := updateCachedNodeData()

	// return status
	return updateSuccessful
}

// WatchHardware is the main loop for hardware update checks (currently disabled)
func WatchHardware() {
	// Add a test node for development
	nodeCache["test-console"] = types.NodeConsoleInfo{
		NodeName: "test-console",
		BmcName:  "test-console-bmc",
		BmcFqdn:  "localhost",
		Class:    "River",
		NID:      1,
		Role:     "Compute",
	}

	// loop forever looking for updates to the hardware
	for {
		// NOTE: Hardware update check is currently disabled
		// if !inShutdown {
		// 	_ = doHardwareUpdate()
		// }

		time.Sleep(time.Duration(newHardwareCheckPeriodSec) * time.Second)
	}
}

// doGetNewNodes checks for new/changed/removed nodes and updates CurrentNodes
func doGetNewNodes(deps WatcherDeps) {
	// keep track of if we need to redo the configuration
	changed := false

	// Check if we need to gather more nodes - don't take more
	// if the service is shutting down
	if !inShutdown {
		fetched_nodes := GetCurrentNodesFromHSM()

		CurrNodesMutex.Lock()
		defer CurrNodesMutex.Unlock()

		new_nodes := make(map[string]*types.NodeConsoleInfo)
		names_map := make(map[string]bool)
		for name := range CurrentNodes {
			names_map[name] = true
		}

		for _, nci := range fetched_nodes {
			// accumulate data for missing nodes to delete
			delete(names_map, nci.NodeName)

			curr_nci, present := CurrentNodes[nci.NodeName]
			if !present {
				new_nodes[nci.NodeName] = &nci
			} else {
				if *curr_nci != nci {
					// something about the info has changed so we
					// probably need to update. we could refine this,
					// but I imagine it almost never happens
					changed = true
					CurrentNodes[nci.NodeName] = &nci
				}
			}
		}

		if len(names_map) != 0 {
			changed = true
			for name := range names_map {
				deps.StopTailing(name)
				delete(CurrentNodes, name)
			}
		}

		if len(new_nodes) != 0 {
			changed = true
			for name, nci := range new_nodes {
				CurrentNodes[name] = nci
			}
		}
	}

	// Restart the conman process if needed
	if changed {
		// term conman, which will trigger a regeneration of the
		// config file before it restarts
		deps.SignalConmanTERM()

		// rebuild the log rotation configuration file
		deps.UpdateLogRotateConf()
	}
}

// WatchForNodes is the primary loop to watch for node updates
func WatchForNodes(deps WatcherDeps) {
	// create a loop to poll for node changes
	for {
		// look for new nodes once
		doGetNewNodes(deps)

		// Wait for the correct polling interval
		time.Sleep(time.Duration(deps.NewNodeLookupSec) * time.Second)
	}
}
