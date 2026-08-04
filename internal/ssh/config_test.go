// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh_test

import (
	"testing"
	"time"

	"github.com/OpenCHAMI/remote-console/internal/ssh"
)

func TestSSHConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ssh.SSHConfig)
		wantErr bool
	}{
		{
			name:   "defaults",
			mutate: func(*ssh.SSHConfig) {},
		},
		{
			name:   "keepalive may be disabled",
			mutate: func(c *ssh.SSHConfig) { c.TCPKeepAlive = 0 },
		},
		{
			name:    "empty terminal type",
			mutate:  func(c *ssh.SSHConfig) { c.TerminalType = "" },
			wantErr: true,
		},
		{
			name:    "zero connect timeout",
			mutate:  func(c *ssh.SSHConfig) { c.ConnectTimeout = 0 },
			wantErr: true,
		},
		{
			// A zero interval causes a tight reconnect loop.
			name:    "zero reconnect min interval",
			mutate:  func(c *ssh.SSHConfig) { c.ReconnectMinInterval = 0 },
			wantErr: true,
		},
		{
			name:    "reconnect min interval below floor",
			mutate:  func(c *ssh.SSHConfig) { c.ReconnectMinInterval = time.Millisecond },
			wantErr: true,
		},
		{
			name: "max interval below min",
			mutate: func(c *ssh.SSHConfig) {
				c.ReconnectMinInterval = 10 * time.Second
				c.ReconnectMaxInterval = 5 * time.Second
			},
			wantErr: true,
		},
		{
			name: "max interval equal to min",
			mutate: func(c *ssh.SSHConfig) {
				c.ReconnectMinInterval = 5 * time.Second
				c.ReconnectMaxInterval = 5 * time.Second
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ssh.DefaultSSHConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestZeroSSHConfigIsInvalid(t *testing.T) {
	if err := (ssh.SSHConfig{}).Validate(); err == nil {
		t.Fatal("zero SSHConfig{} must not validate")
	}
}
