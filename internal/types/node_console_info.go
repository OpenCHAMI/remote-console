// MIT License
// (C) Copyright 2019-2024 Hewlett Packard Enterprise Development LP
//
// NodeConsoleInfo type and helpers for console node metadata

package types

import "fmt"

// NodeConsoleInfo holds all node level information needed to form a console connection
// NOTE: this is the basic unit of information required for each node
// Exported for use by console and creds packages

type NodeConsoleInfo struct {
	NodeName string // node xname
	BmcName  string // bmc xname
	BmcFqdn  string // full name of bmc
	Class    string // river/mtn class
	NID      int    // NID of the node
	Role     string // role of the node
}

func (node NodeConsoleInfo) IsKeySSH() bool {
	return node.Class == "Mountain" || node.Class == "Hill"
}

func (node NodeConsoleInfo) IsIPMI() bool {
	return node.Class == "River"
}

func (node NodeConsoleInfo) IsPassSSH() bool {
	return node.Class == "Paradise"
}

func (nc NodeConsoleInfo) String() string {
	return fmt.Sprintf("NodeName:%s, BmcName:%s, BmcFqdn:%s, Class:%s, NID:%d, Role:%s",
		nc.NodeName, nc.BmcName, nc.BmcFqdn, nc.Class, nc.NID, nc.Role)
}
