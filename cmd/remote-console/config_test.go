// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package main

import "testing"

func TestCommandIncludesConsoleLogSettings(t *testing.T) {
	config := DefaultConfig()
	cmd := command(&config)
	flags := make(map[string]bool)
	for _, flag := range cmd.Flags {
		for _, name := range flag.Names() {
			flags[name] = true
		}
	}

	for _, name := range []string{"console-logs-base-path", "conman-logs-path"} {
		if !flags[name] {
			t.Errorf("command does not provide %s", name)
		}
	}
}

func TestCommandOmitsSSHReconnectSettings(t *testing.T) {
	config := DefaultConfig()
	cmd := command(&config)
	for _, name := range []string{"ssh-reconnect-min-interval", "ssh-reconnect-max-interval"} {
		for _, flag := range cmd.Flags {
			if flag.Names()[0] == name {
				t.Errorf("command unexpectedly provides %s", name)
			}
		}
	}
}

func TestResolveConsoleLogsBasePath(t *testing.T) {
	defaultPath := DefaultConfig().Log.ConsoleLogsBasePath
	tests := []struct {
		name       string
		newPath    string
		legacyPath string
		want       string
		wantErr    bool
	}{
		{
			name:       "defaults agree",
			newPath:    defaultPath,
			legacyPath: defaultPath,
			want:       defaultPath,
		},
		{
			name:       "new setting overrides legacy default",
			newPath:    "/var/log/console",
			legacyPath: defaultPath,
			want:       "/var/log/console",
		},
		{
			name:       "legacy setting remains supported",
			newPath:    defaultPath,
			legacyPath: "/var/log/legacy",
			want:       "/var/log/legacy",
		},
		{
			name:       "matching custom settings agree",
			newPath:    "/var/log/console",
			legacyPath: "/var/log/console",
			want:       "/var/log/console",
		},
		{
			name:       "custom settings conflict",
			newPath:    "/var/log/console",
			legacyPath: "/var/log/legacy",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()
			config.Log.ConsoleLogsBasePath = tt.newPath
			config.Conman.LogsPath = tt.legacyPath

			err := resolveConsoleLogsBasePath(&config)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected conflicting settings to fail")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve console logs base path: %v", err)
			}
			if config.Log.ConsoleLogsBasePath != tt.want {
				t.Errorf("generic path = %q, want %q", config.Log.ConsoleLogsBasePath, tt.want)
			}
			if config.Conman.LogsPath != tt.want {
				t.Errorf("ConMan path = %q, want %q", config.Conman.LogsPath, tt.want)
			}
		})
	}
}
