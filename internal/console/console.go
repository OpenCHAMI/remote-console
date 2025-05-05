//
//  MIT License
//
//  (C) Copyright 2019-2024 Hewlett Packard Enterprise Development LP
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

// This file contains the code to manage the node consoles under this pod

package console

import (
	"time"
)

// maybe remove
// Pause between each lookup for new node information
var newNodeLookupSec int = 30

// File to hold target number of node information - it will reside on
// a shared file system so console-node pods can read what is set here
const targetNodeFile string = "/var/log/console/TargetNodes.txt"

func doGetNewNodes() {
	// keep track of if we need to redo the configuration
	changed := false

	// Check if we need to gather more nodes - don't take more
	//  if the service is shutting down
	if !inShutdown {

		fetched_nodes := getCurrentNodesFromHSM()

		currNodesMutex.Lock()
		defer currNodesMutex.Unlock()

		new_nodes := make(map[string]*nodeConsoleInfo)
		names_map := make(map[string]bool)
		for name, _ := range currentNodes {
			names_map[name] = true
		}

		for _, nci := range fetched_nodes {
			//accumulate data for missing nodes to delete
			delete(names_map, nci.NodeName)

			curr_nci, present := currentNodes[nci.NodeName]
			if !present {
				//
				new_nodes[nci.NodeName] = &nci
			} else {
				if *curr_nci != nci {
					// something about the info has changed so we
					// probably need to update.  we could refine this,
					// but I imagine it almost never happens
					changed = true
					currentNodes[nci.NodeName] = &nci
				}
			}
		}

		if len(names_map) != 0 {
			changed = true
			for name, _ := range names_map {
				delete(currentNodes, name)
			}
		}

		if len(new_nodes) != 0 {
			changed = true
			for name, nci := range new_nodes {
				currentNodes[name] = nci
			}
		}
	}

	// Restart the conman process if needed
	if changed {
		// term conman, which will trigger a regeneration of the
		// config file before it restarts
		signalConmanTERM()

		// rebuild the log rotation configuration file
		updateLogRotateConf() //TODO: look at this to make sure
	}

}

// Primary loop to watch for updates
func WatchForNodes() {
	// create a loop to execute the conmand command
	for {
		// look for new nodes once
		doGetNewNodes()

		// Wait for the correct polling interval
		time.Sleep(time.Duration(newNodeLookupSec) * time.Second)
	}
}

// Function to release the node from being monitored
func releaseNode(xname string) bool {
	// NOTE: called during heartbeat thread

	// This will remove it from the list of current nodes and stop tailing the
	// log file.
	found := false
	if _, ok := currentNodes[xname]; ok {
		delete(currentNodes, xname)
		found = true
	}

	// remove the tail process for this file
	stopTailing(xname)

	return found
}
