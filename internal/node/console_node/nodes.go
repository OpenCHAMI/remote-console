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

package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Struct to hold all node level information needed to form a console connection
type nodeConsoleInfo struct {
	NodeName string // node xname
	BmcName  string // bmc xname
	BmcFqdn  string // full name of bmc
	Class    string // river/mtn class
	NID      int    // NID of the node
	Role     string // role of the node
}

// Function to determine if a node is Mountain hardware
func (node nodeConsoleInfo) isMountain() bool {
	return node.Class == "Mountain" || node.Class == "Hill"
}

// Function to determine if a node is River hardware
func (node nodeConsoleInfo) isRiver() bool {
	return node.Class == "River"
}

// Function to determine if a node is Paradise hardware
func (node nodeConsoleInfo) isParadise() bool {
	return node.Class == "Paradise"
}

// Provide a function to convert struct to string
func (nc nodeConsoleInfo) String() string {
	return fmt.Sprintf("NodeName:%s, BmcName:%s, BmcFqdn:%s, Class:%s, NID:%d, Role:%s",
		nc.NodeName, nc.BmcName, nc.BmcFqdn, nc.Class, nc.NID, nc.Role)
}

// Globals for managing nodes being watched
// NOTE: the current nodes need to be kept in 3 distinct groups:
//
//	River Nodes: connect through ipmi protocol directly through conman
//	Mountain Nodes: connect through expect script via passwordless ssh
//	Paradise Nodes: connect through expect script via password based ssh
var currNodesMutex = &sync.Mutex{}
var currentMtnNodes map[string]*nodeConsoleInfo = make(map[string]*nodeConsoleInfo) // [xname,*consoleInfo]
var currentRvrNodes map[string]*nodeConsoleInfo = make(map[string]*nodeConsoleInfo) // [xname,*consoleInfo]
var currentPdsNodes map[string]*nodeConsoleInfo = make(map[string]*nodeConsoleInfo) // [xname,*consoleInfo]

// Number of nodes this pod should be watching
var targetRvrNodes int = -1
var targetMtnNodes int = -1

// Number of nodes to get per acquisition query
var maxAcquireRvr int = 500
var maxAcquireMtn int = 200

// Pause between each lookup for new node information
var newNodeLookupSec int = 30

// File to hold target number of node information - it will reside on
// a shared file system so console-node pods can read what is set here
const targetNodeFile string = "/var/log/console/TargetNodes.txt"

// small helper function to insure correct number of nodes asked for
func pinNumNodes(numAsk, numMax int) int {
	// insure the input number ends in range [0,numMax]
	if numAsk < 0 {
		// already have too many
		numAsk = 0
	} else if numAsk > numMax {
		// pin at the maximum
		numAsk = numMax
	}
	return numAsk
}

func doGetNewNodes() {
	// put a lock on the current nodes while looking for new ones
	currNodesMutex.Lock()
	defer currNodesMutex.Unlock()

	// keep track of if we need to redo the configuration
	changed := false

	// Update the target number of nodes being monitored
	updateNodesPerPod()

	// Check if we need to gather more nodes - don't take more
	//  if the service is shutting down
	if !inShutdown && (len(currentRvrNodes) < targetRvrNodes || len(currentMtnNodes) < targetMtnNodes) {
		// figure out how many of each to ask for
		numRvr := pinNumNodes(targetRvrNodes-len(currentRvrNodes), maxAcquireRvr)
		numMtn := pinNumNodes(targetMtnNodes-len(currentMtnNodes), maxAcquireMtn)

		// attempt to acquire more nodes
		if numRvr > 0 || numMtn > 0 {
			// NOTE: this should be the ONLY place where the maps of
			//  current nodes is updated!!!
			// NOTE: paradise nodes are included in mountain count
			newNodes := acquireNewNodes(numMtn, numRvr, podLocData)
			// process the new nodes
			for i, node := range newNodes {
				//log.Printf("  Processing node: %s", node.String())
				if node.isRiver() {
					currentRvrNodes[node.NodeName] = &newNodes[i]
					changed = true
				} else if node.isMountain() {
					currentMtnNodes[node.NodeName] = &newNodes[i]
					changed = true
				} else if node.isParadise() {
					currentPdsNodes[node.NodeName] = &newNodes[i]
					changed = true
				}
			}
		} else {
			log.Printf("Nothing to acquire after pin...")
		}
	} else {
		log.Printf("Skipping acquire - at capacity. CurRvr:%d, TarRvr:%d, CurMtn:%d, TarMtn:%d",
			len(currentRvrNodes), targetRvrNodes, len(currentMtnNodes)+len(currentPdsNodes), targetMtnNodes)
	}

	// See if we have too many nodes
	if rebalanceNodes() {
		changed = true
	}

	// Restart the conman process if needed
	if changed {
		// trigger a re-configuration and restart of conman
		signalConmanTERM()

		// rebuild the log rotation configuration file
		updateLogRotateConf()
	}

}

// Primary loop to watch for updates
func watchForNodes() {
	// create a loop to execute the conmand command
	for {
		// look for new nodes once
		doGetNewNodes()

		// Wait for the correct polling interval
		time.Sleep(time.Duration(newNodeLookupSec) * time.Second)
	}
}

// If we have too many nodes, release some
func rebalanceNodes() bool {
	// NOTE: this function just modifies currentNodes lists and stops
	//  tailing operation.  The configuration files will be triggered to be
	//  regenerated outside of this operation.

	// NOTE: in doGetNewNodes thread

	// see if we need to release any nodes
	if len(currentRvrNodes) <= targetRvrNodes && len(currentMtnNodes) <= targetMtnNodes {
		log.Printf("Current number of nodes within target range - no rebalance needed")
		return false
	}

	// gather nodes to give back
	var rn []nodeConsoleInfo

	// release river nodes until match target number
	// NOTE: map iteration is random
	for key, ni := range currentRvrNodes {
		if len(currentRvrNodes) > targetRvrNodes {
			// remove another one
			rn = append(rn, *ni)
			delete(currentRvrNodes, key)

			// stop tailing this file
			stopTailing(key)
		} else {
			// done so break
			break
		}
	}

	// release mtn nodes until match target number
	// NOTE: paradise nodes count towards mountain limits, remove from both
	for len(currentMtnNodes)+len(currentPdsNodes) > targetMtnNodes {
		// balance removal so take from whichever pool is larger, one at a time
		targetPool := &currentPdsNodes
		if len(currentMtnNodes) > len(currentPdsNodes) {
			targetPool = &currentMtnNodes
		}

		// make sure we didn't hit some weird condition where both lists are empty
		if len(*targetPool) == 0 {
			break
		}

		// remove a node from the target pool
		// NOTE: map iteration is random - use it to grab a random node to remove
		for key, ni := range *targetPool {
			// remove node
			rn = append(rn, *ni)
			delete(*targetPool, key)

			// stop tailing this file
			stopTailing(key)

			// only want to remove one at a time
			break
		}
	}

	if len(rn) > 0 {
		// notify console-data that we are no longer tracking these nodes
		releaseNodes(rn)

		// signify that we have removed nodes and something has changed
		return true
	}

	// signify nothing has really changed
	return false
}

// Function to release the node from being monitored
func releaseNode(xname string) bool {
	// NOTE: called during heartbeat thread

	// This will remove it from the list of current nodes and stop tailing the
	// log file.
	found := false
	if _, ok := currentRvrNodes[xname]; ok {
		delete(currentRvrNodes, xname)
		found = true
	} else if _, ok := currentMtnNodes[xname]; ok {
		delete(currentMtnNodes, xname)
		found = true
	} else if _, ok := currentPdsNodes[xname]; ok {
		delete(currentPdsNodes, xname)
		found = true
	}

	// remove the tail process for this file
	stopTailing(xname)

	return found
}

// Update the number of target consoles per node pod
func updateNodesPerPod() {
	// NOTE: for the time being we will just put this information
	//  into a simple text file on a pvc shared with console-operator
	//  and console-node pods.  The console-operator will write changes
	//  and the console-node pods will read periodically for changes.
	//  This mechanism can be made more elegant later if needed but it
	//  needs to be something that can be picked up by all console-node
	//  pods without restarting them.

	// NOTE: in doGetNewNodes thread

	log.Printf("Updating nodes per pod")
	// open the state file
	sf, err := os.Open(targetNodeFile)
	if err != nil {
		log.Printf("Unable to open target node file %s: %s", targetNodeFile, err)
		return
	}
	defer sf.Close()

	// process the lines in the file
	newRvr := -1
	newMtn := -1
	er := bufio.NewReader(sf)
	for {
		// read the next line
		line, err := er.ReadString('\n')
		if err != nil {
			// done reading file
			break
		}

		// find if this is a river line
		const rvrTxt string = "River:"
		const mtnTxt string = "Mountain:"

		if pos := strings.Index(line, rvrTxt); pos >= 0 {
			// peel out the number between : and eol
			numStr := line[pos+len(rvrTxt) : len(line)-1]
			newRvr, err = strconv.Atoi(numStr)
			if err != nil {
				log.Printf("Error reading number of river nodes: %s", err)
			}
		}

		// find if this is a mountain line
		if pos := strings.Index(line, mtnTxt); pos >= 0 {
			// peel out the number between : and eol
			numStr := line[pos+len(mtnTxt) : len(line)-1]
			newMtn, err = strconv.Atoi(numStr)
			if err != nil {
				log.Printf("Error reading number of mountain nodes: %s", err)
			}
		}
	}

	// set the new values with a little sanity checking
	if newRvr >= 0 {
		targetRvrNodes = newRvr
	}
	if newMtn >= 0 {
		targetMtnNodes = newMtn
	}
	log.Printf("  New target nodes - mtn: %d, rvr: %d", newMtn, newRvr)
}
