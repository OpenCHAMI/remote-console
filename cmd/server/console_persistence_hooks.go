// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"sort"

	v1 "github.com/OpenCHAMI/remote-console/apis/remote-console.openchami.io/v1"
	"github.com/OpenCHAMI/remote-console/internal/nodes"
	"github.com/openchami/fabrica/pkg/fabrica"
)

func consoleFromNode(node *nodes.NodeConsoleInfo) *v1.Console {
	return &v1.Console{
		APIVersion: "remote-console.openchami.io/v1",
		Kind:       "Console",
		Metadata: fabrica.Metadata{
			Name: node.ID,
			UID:  node.ID,
		},
		Spec: v1.ConsoleSpec{
			ConnectionType:      node.ConnectionType,
			ConnectionHost:      node.ConnectionHost,
			ConnectionPort:      node.ConnectionPort,
			ConsoleEntryCommand: node.ConsoleEntryCommand,
		},
	}
}

// ListConsoleResources supplies Console resources from discovered SMD Redfish data.
func ListConsoleResources(_ context.Context) ([]*v1.Console, error) {
	currentNodes := nodes.CurrentNodes()
	ids := make([]string, 0, len(currentNodes))
	for id := range currentNodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	consoles := make([]*v1.Console, 0, len(ids))
	for _, id := range ids {
		if currentNodes[id] == nil {
			continue
		}
		consoles = append(consoles, consoleFromNode(currentNodes[id]))
	}

	return consoles, nil
}
