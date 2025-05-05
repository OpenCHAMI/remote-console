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

// This file contains the code needed to find node information

package console

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
)

var (
	HsmURL    = "http://cray-smd/"
	DebugOnly = false
)

// Globals for managing nodes being watched
//  Note that there are three classes of nodes:
//
//	IPMI Nodes: connect through ipmi protocol directly through conman
//	CertSSH Nodes: connect through expect script via passwordless ssh
//	PassSSH Nodes: connect through expect script via password based ssh

// we need a more general and configurable system for mapping classes
// connection methods, and we need to know how "class" is generated
// both in CSM and OC-SMD

var currNodesMutex = &sync.Mutex{}

// this should just be a map of all of them, surely the swtiching cost
// isn't very high?
var currentNodes map[string]*nodeConsoleInfo = make(map[string]*nodeConsoleInfo) // [xname,*consoleInfo]

// Struct to hold all node level information needed to form a console connection
// NOTE: this is the basic unit of information required for each node
// NOTE: expected values for 'Class' are:
//
//	Mountain - Cray hardware in full liquid cooled rack (ssh via key)
//	Hill - Cray hardware in freestanding rack (ssh via key)
//	River - Other brand hardware in freestanding rack (ipmi via user/password)
//	Paradise - Cray xd224 - foxconn bmc (ssh via user/password)
type nodeConsoleInfo struct {
	NodeName string // node xname
	BmcName  string // bmc xname
	BmcFqdn  string // full name of bmc
	Class    string // river/mtn class
	NID      int    // NID of the node
	Role     string // role of the node
}

// TODO: at some point we need to add a config file so that this
// isn't static and new nodes are allowed to be added.
func (node nodeConsoleInfo) isCertSSH() bool {
	return node.Class == "Mountain" || node.Class == "Hill"
}

func (node nodeConsoleInfo) isIPMI() bool {
	return node.Class == "River"
}

func (node nodeConsoleInfo) isPassSSH() bool {
	return node.Class == "Paradise"
}

// Provide a function to convert struct to string
func (nc nodeConsoleInfo) String() string {
	return fmt.Sprintf("NodeName:%s, BmcName:%s, BmcFqdn:%s, Class:%s, NID:%d, Role:%s",
		nc.NodeName, nc.BmcName, nc.BmcFqdn, nc.Class, nc.NID, nc.Role)
}

// Struct to hold hsm redfish endpoint information
type redfishEndpoint struct {
	ID       string
	Type     string
	FQDN     string
	User     string
	Password string
}

// Provide a function to convert struct to string
func (re redfishEndpoint) String() string {
	return fmt.Sprintf("ID:%s, Type:%s, FQDN:%s, User:%s, Password:REDACTED", re.ID, re.Type, re.FQDN, re.User)
}

// Struct to hold hsm state component information
type stateComponent struct {
	ID    string
	Type  string
	Class string `json:",omitempty"`
	NID   int    `json:",omitempty"` // NOTE: NID value only valid if Role="Compute"
	Role  string `json:",omitempty"`
}

// Provide a function to convert struct to string
func (sc stateComponent) String() string {
	return fmt.Sprintf("ID:%s, Type:%s, Class:%s, NID:%d, Role:%s", sc.ID, sc.Type, sc.Class, sc.NID, sc.Role)
}

// Query hsm for redfish endpoint information
func getRedfishEndpoints() ([]redfishEndpoint, error) {
	type response struct {
		RedfishEndpoints []redfishEndpoint
	}

	// Query hsm to get the redfish endpoints
	URL := HsmURL + "hsm/v2/Inventory/RedfishEndpoints"
	data, _, err := getURL(URL, nil)
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

// Query hsm for state component information
func getStateComponents() ([]stateComponent, error) {
	// get the component states from hsm - includes river/mountain information
	type response struct {
		Components []stateComponent
	}

	// get the state components from hsm
	URL := HsmURL + "hsm/v2/State/Components"
	data, _, err := getURL(URL, nil)
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

// Query hsm for Paradise (xd224) nodes
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
	data, _, err := getURL(URL, nil)
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

func getCurrentNodesFromHSM() (nodes []nodeConsoleInfo) {
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
			newNode := nodeConsoleInfo{NodeName: sc.ID, Class: sc.Class, NID: sc.NID, Role: sc.Role}

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
