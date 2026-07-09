// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package v1

import "github.com/openchami/fabrica/pkg/fabrica"

// Console represents a console resource
type Console struct {
	APIVersion string           `json:"apiVersion" yaml:"apiVersion"`
	Kind       string           `json:"kind" yaml:"kind"`
	Metadata   fabrica.Metadata `json:"metadata" yaml:"metadata"`
	Spec       ConsoleSpec      `json:"spec" yaml:"spec" validate:"required"`
}

// ConsoleSpec defines the desired state of Console
type ConsoleSpec struct {
	ConnectionType      string `json:"connectionType" yaml:"connectionType"`
	ConnectionHost      string `json:"connectionHost" yaml:"connectionHost"`
	ConnectionPort      int    `json:"connectionPort" yaml:"connectionPort"`
	ConsoleEntryCommand string `json:"consoleEntryCommand,omitempty" yaml:"consoleEntryCommand,omitempty"`
}

// GetKind returns the kind of the resource
func (r *Console) GetKind() string {
	return "Console"
}

// GetName returns the name of the resource
func (r *Console) GetName() string {
	return r.Metadata.Name
}

// GetUID returns the UID of the resource
func (r *Console) GetUID() string {
	return r.Metadata.UID
}

// IsHub marks this as the hub/storage version
func (r *Console) IsHub() {}
