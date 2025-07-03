//
//  MIT License
//
//  (C) Copyright 2021-2023 Hewlett Packard Enterprise Development LP
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

package console

import (
	"log"
	"time"
)

// globals to cache current node information
var nodeCache map[string]nodeConsoleInfo = make(map[string]nodeConsoleInfo)

// Global var to control how often we check for hardware changes,
// probably should be tunable
var newHardwareCheckPeriodSec int = 120
var hardwareUpdateTime string = "Unknown"

// Global var to signal we are shutting down and prevent periodic checks from happening
var inShutdown bool = false

func updateCachedNodeData() (bool, []nodeConsoleInfo) {
	// return if the console-data update succeeded
	updateSuccessful := true

	// get the current endpoints from hsm
	currNodes := getCurrentNodesFromHSM()
	currNodesMap := make(map[string]nodeConsoleInfo)
	for _, n := range currNodes {
		currNodesMap[n.NodeName] = n
	}

	// Find new nodes that are in the currNodes but not in nodeCache
	var newNodes []nodeConsoleInfo = nil
	for _, n := range currNodes {
		if _, found := nodeCache[n.NodeName]; !found {
			newNodes = append(newNodes, n)
			log.Printf("Found new node: %s", n.String())
		}
	}

	// Find nodes to remove that are in the nodeCache but not in currNodes
	var removedNodes []nodeConsoleInfo = nil
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
	// 		mountain keys for new nodes
	return updateSuccessful, newNodes
}

// Function to do a hardware update check
func doHardwareUpdate() bool {
	// record the time of the hardware update attempt
	hardwareUpdateTime = time.Now().Format(time.RFC3339)

	// Update the cache and data in console-data
	updateSuccessful, _ := updateCachedNodeData()

	// return status
	return updateSuccessful
}

// Main loop for console-operator stuff
func WatchHardware() {
	// loop forever looking for updates to the hardware
	for {
		// do a check of the current hardware
		// NOTE: if the service is currently in the process of shutting down
		//  do not perform the hardware update check
		if !inShutdown {
			// do the update
			_ = doHardwareUpdate()
		}

		// There are times we want to wait for a little before starting a new
		// process - ie killproc may get caught trying to kill all instances
		time.Sleep(time.Duration(newHardwareCheckPeriodSec) * time.Second)
	}
}
