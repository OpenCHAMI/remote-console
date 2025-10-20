//package nodes

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

// Package nodes manages node discovery and state from HSM
package nodes

import (
		"encoding/json"
		"fmt"
		"log"
		"strings"
		"sync"
		"time"

		"github.com/OpenCHAMI/remote-console/internal/types"
		"github.com/OpenCHAMI/remote-console/internal/utils"
)

var (
	// HsmURL is the base URL for HSM/SMD API
	HsmURL = "http://cray-smd/"
	
	// DebugOnly mode prevents actual operations
	DebugOnly = false
)

// Pause between each lookup for new node information
var newNodeLookupSec int = 30

// CurrNodesMutex protects access to CurrentNodes
var currNodesMutex = &sync.Mutex{}

// CurrentNodes is the map of all nodes being monitored
var currentNodes map[string]*types.NodeConsoleInfo = make(map[string]*types.NodeConsoleInfo)

// NodeInfoAdapter adapts types.NodeConsoleInfo to logs.NodeInfo interface
type NodeInfoAdapter struct {
	*types.NodeConsoleInfo
}

func (n *NodeInfoAdapter) GetNodeName() string {
	return n.NodeName
}

// redfishEndpoint holds HSM redfish endpoint information
type redfishEndpoint struct {
	ID       string
	Type     string
	FQDN     string
	User     string
	Password string
}

// String returns a string representation with password redacted
func (re redfishEndpoint) String() string {
	return fmt.Sprintf("ID:%s, Type:%s, FQDN:%s, User:%s, Password:REDACTED", re.ID, re.Type, re.FQDN, re.User)
}

// stateComponent holds HSM state component information
type stateComponent struct {
	ID    string
	Type  string
	Class string `json:",omitempty"`
	NID   int    `json:",omitempty"` // NOTE: NID value only valid if Role="Compute"
	Role  string `json:",omitempty"`
}

// String returns a string representation
func (sc stateComponent) String() string {
	return fmt.Sprintf("ID:%s, Type:%s, Class:%s, NID:%d, Role:%s", sc.ID, sc.Type, sc.Class, sc.NID, sc.Role)
}

// getRedfishEndpoints queries HSM for redfish endpoint information
func getRedfishEndpoints() ([]redfishEndpoint, error) {
	type response struct {
		RedfishEndpoints []redfishEndpoint
	}

	// Query hsm to get the redfish endpoints
	URL := HsmURL + "hsm/v2/Inventory/RedfishEndpoints"
		data, _, err := utils.GetURL(URL, nil)
	if err != nil {
		log.Printf("Unable to get redfish endpoints from hsm:%s", err)
		return nil, err
	}

	// decode the response
	rp := response{}
	err = json.Unmarshal(data, &rp)
	if err != nil {
		log.Printf("Error unmarshalling data: %s", err)
		return nil, err
	}

	return rp.RedfishEndpoints, nil
}

// getStateComponents queries HSM for state component information
func getStateComponents() ([]stateComponent, error) {
	// get the component states from hsm - includes river/mountain information
	type response struct {
		Components []stateComponent
	}

	// get the state components from hsm
	URL := HsmURL + "hsm/v2/State/Components"
		data, _, err := utils.GetURL(URL, nil)
	if err != nil {
		log.Printf("Unable to get state component information from hsm:%s", err)
		return nil, err
	}

	// decode the response
	rp := response{}
	err = json.Unmarshal(data, &rp)
	if err != nil {
		// handle error
		log.Printf("Error unmarshalling data: %s", err)
		return nil, nil
	}

	return rp.Components, nil
}

// getParadiseNodes queries HSM for Paradise (xd224) nodes
func getParadiseNodes() (map[string]struct{}, error) {
	// Paradise nodes are identified by having the manufacturer as 'Foxconn' and
	// the model as either 'HPE Cray Supercomputing XD224' or '1A62WCB00-600-G'.
	// There are a limited number of units that were sent to the field with the
	// incorrect model '1A62WCB00-600-G' so we must support that.

	// Structs to unmarshal the inventory data we care about
	type HsmNodeFRUInfo struct {
		Model        string
		Manufacturer string
		PartNumber   string
		SerialNumber string
	}
	type HsmPopulatedFRU struct {
		Type        string
		Subtype     string
		NodeFRUInfo HsmNodeFRUInfo
	}
	type HsmHardwareInventoryItem struct {
		ID           string
		Type         string
		PopulatedFRU HsmPopulatedFRU
	}

	// Query hsm to get the Paradise nodes
	// NOTE: this only pulls the Foxconn BMCs from the inventory so there is a bit of
	//  server side filtering going on
	URL := HsmURL + "hsm/v2/Inventory/Hardware?Manufacturer=Foxconn&Type=Node"
		data, _, err := utils.GetURL(URL, nil)
	if err != nil {
		log.Printf("Unable to get hardware inventory from hsm:%s", err)
		return nil, err
	}

	// decode the response
	rp := []HsmHardwareInventoryItem{}
	err = json.Unmarshal(data, &rp)
	if err != nil {
		log.Printf("Error unmarshalling data: %s", err)
		return nil, err
	}

	// create a set of the Paradise items
	nodes := map[string]struct{}{}
	for _, node := range rp {
		if node.PopulatedFRU.NodeFRUInfo.Model == "HPE Cray Supercomputing XD224" ||
			node.PopulatedFRU.NodeFRUInfo.Model == "1A62WCB00-600-G" {
			nodes[node.ID] = struct{}{}
		}
	}

	return nodes, nil
}

// GetCurrentNodesFromHSM queries HSM for all node information and returns a slice of NodeConsoleInfo
func GetCurrentNodesFromHSM() (nodes []types.NodeConsoleInfo) {
	// Get the BMC IP addresses and user, and password for individual nodes.
	// conman is only set up for River nodes.
	log.Printf("Starting to get current nodes on the system")

	rfEndpoints, err := getRedfishEndpoints()
	if err != nil {
		log.Printf("Unable to build configuration file - error fetching redfish endpoints: %s", err)
		return nil
	}

	// get the state information to find mountain/river designation
	stComps, err := getStateComponents()
	if err != nil {
		log.Printf("Unable to build configuration file - error fetching state components: %s", err)
		return nil
	}

	// get the paradise nodes
	// NOTE: this returns a pseudo-set to speed up lookups
	paradiseNodes, err := getParadiseNodes()
	if err != nil {
		// log the error but don't die - most systems will not have Paradise nodes anyway
		log.Printf("Unable to identify if there are any Paradise nodes on the system. %s", err)
	}

	// create a lookup map for the redfish information
	rfMap := make(map[string]redfishEndpoint)
	for _, rf := range rfEndpoints {
		rfMap[rf.ID] = rf
	}

	// create river and mountain node information
	nodes = nil
	for _, sc := range stComps {
		if sc.Type == "Node" {
			// create a new entry for this node - take initial vals from state component info
			newNode := types.NodeConsoleInfo{NodeName: sc.ID, Class: sc.Class, NID: sc.NID, Role: sc.Role}

			// If this is a paradise node, switch the class name
			if _, isParadise := paradiseNodes[sc.ID]; isParadise {
				newNode.Class = "Paradise"
			}

			// pull information about the node BMC from the redfish information
			bmcName := sc.ID[0:strings.LastIndex(sc.ID, "n")]
			//log.Printf("Parsing node info. Node:%s, bmc:%s", sc.ID, bmcName)
			if rf, ok := rfMap[bmcName]; ok {
				//log.Print("  Found redfish endpoint info")
				// found the bmc in the redfish information
				newNode.BmcName = bmcName
				newNode.BmcFqdn = rf.FQDN

				// add to the list of nodes
				nodes = append(nodes, newNode)

			} else {
				log.Printf("Node with no BMC present: %s, bmcName:%s", sc.ID, bmcName)
			}
		}
	}

	return nodes
}

func doGetNewNodes(signalConmanTERM, updateLogRotateConf func()) {
	// keep track of if we need to redo the configuration
	changed := false

	// Check if we need to gather more nodes - don't take more
	//  if the service is shutting down
	if !inShutdown {

		fetched_nodes := GetCurrentNodesFromHSM()

		currNodesMutex.Lock()
		defer currNodesMutex.Unlock()

		new_nodes := make(map[string]*types.NodeConsoleInfo)
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
		if signalConmanTERM != nil {
			signalConmanTERM()
		}

		// rebuild the log rotation configuration file
		if updateLogRotateConf != nil {
			updateLogRotateConf()
		}
	}

}

// Primary loop to watch for updates
func WatchForNodes(signalConmanTERM, updateLogRotateConf func()) {
	// create a loop to execute the conmand command
	for {
		// look for new nodes once
		doGetNewNodes(signalConmanTERM, updateLogRotateConf)

		// Wait for the correct polling interval
		time.Sleep(time.Duration(newNodeLookupSec) * time.Second)
	}
}

func CurrentNodes() map[string]*types.NodeConsoleInfo {
	currNodesMutex.Lock()
	defer currNodesMutex.Unlock()

	// create a copy of the current nodes to return
	nodesCopy := make(map[string]*types.NodeConsoleInfo)
	for k, v := range currentNodes {
		nodesCopy[k] = v
	}

	return nodesCopy
}

// Function to release the node from being monitored
func releaseNode(xname string, stopTailing func(string)) bool {
	currNodesMutex.Lock()
	defer currNodesMutex.Unlock()
	// NOTE: called during heartbeat thread

	// This will remove it from the list of current nodes and stop tailing the
	// log file.
	found := false
	if _, ok := currentNodes[xname]; ok {
		delete(currentNodes, xname)
		found = true
	}

	// remove the tail process for this file
	if stopTailing != nil {
		stopTailing(xname)
	}

	return found
}
