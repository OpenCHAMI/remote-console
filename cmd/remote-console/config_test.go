// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

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

func testAuthFlagsAndEnvironment(t *testing.T, settings map[string]string, check func(*testing.T, remoteConsoleConfig)) {
	t.Helper()
	for _, useFlags := range []bool{false, true} {
		t.Run(map[bool]string{false: "environment", true: "flags override environment"}[useFlags], func(t *testing.T) {
			args := []string{"remote-console"}
			for flag, value := range settings {
				env := "RCS_" + strings.ToUpper(strings.ReplaceAll(flag, "-", "_"))
				if useFlags {
					t.Setenv(env, "invalid-env-value")
					args = append(args, "--"+flag+"="+value)
				} else {
					t.Setenv(env, value)
				}
			}
			config := DefaultConfig()
			cmd := command(&config)
			cmd.Action = func(context.Context, *cli.Command) error { return nil }
			require.NoError(t, cmd.Run(context.Background(), args))
			check(t, config)
		})
	}
}

func TestAuthFlagsAndEnvironment(t *testing.T) {
	settings := map[string]string{
		"jwks-url":             "http://jwks/keys",
		"jwks-fetch-interval":  "2",
		"oauth2-client-id":     "oauth2-id",
		"oauth2-client-secret": "oauth2-secret",
		"oauth2-token-url":     "http://oauth2/token",
		"oauth2-scopes":        "read",
	}
	testAuthFlagsAndEnvironment(t, settings, func(t *testing.T, config remoteConsoleConfig) {
		require.Equal(t, "http://jwks/keys", config.JwksURL)
		require.Equal(t, 2, config.JwksFetchInterval)
		require.Equal(t, "oauth2-id", config.Oauth2.ClientID)
		require.Equal(t, "oauth2-secret", config.Oauth2.ClientSecret)
		require.Equal(t, "http://oauth2/token", config.Oauth2.TokenURL)
		require.Equal(t, []string{"read"}, config.Oauth2.Scopes)
	})
}
