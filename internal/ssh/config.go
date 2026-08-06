// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh

import (
	"fmt"
	"time"
)

// SSHConfig configures SSH consoles.
type SSHConfig struct {
	TerminalType         string        `desc:"Terminal type for SSH PTY requests"`
	TCPKeepAlive         time.Duration `desc:"TCP keepalive interval for SSH connections (0 disables)"`
	ConnectTimeout       time.Duration `desc:"Deadline for the TCP dial and SSH handshake"`
	ReconnectMinInterval time.Duration `flag:"-"`
	ReconnectMaxInterval time.Duration `flag:"-"`
}

// DefaultSSHConfig returns the default SSH console settings.
func DefaultSSHConfig() SSHConfig {
	return SSHConfig{
		TerminalType:         "xterm-256color",
		TCPKeepAlive:         180 * time.Second,
		ConnectTimeout:       30 * time.Second,
		ReconnectMinInterval: 1 * time.Second,
		ReconnectMaxInterval: 30 * time.Second,
	}
}

// minReconnectInterval prevents tight reconnect loops.
const minReconnectInterval = 250 * time.Millisecond

// Validate checks SSH console settings.
func (c SSHConfig) Validate() error {
	if c.TerminalType == "" {
		return fmt.Errorf("ssh-terminal-type must not be empty")
	}
	if c.ConnectTimeout <= 0 {
		return fmt.Errorf("ssh-connect-timeout must be positive, got %s", c.ConnectTimeout)
	}
	if c.ReconnectMinInterval < minReconnectInterval {
		return fmt.Errorf("SSH reconnect minimum interval must be at least %s, got %s",
			minReconnectInterval, c.ReconnectMinInterval)
	}
	if c.ReconnectMaxInterval < c.ReconnectMinInterval {
		return fmt.Errorf("SSH reconnect maximum interval %s must not be less than minimum interval %s",
			c.ReconnectMaxInterval, c.ReconnectMinInterval)
	}
	return nil
}
