// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenSmithFlagsAndEnvironment(t *testing.T) {
	settings := map[string]string{
		"auth-provider":                  "tokensmith",
		"tokensmith-jwks-url":            "http://issuer/keys",
		"tokensmith-issuer":              "http://issuer",
		"tokensmith-audience":            "remote-console",
		"tokensmith-authz-mode":          "shadow",
		"tokensmith-authz-model-path":    "/model",
		"tokensmith-authz-policy-path":   "/policy",
		"tokensmith-authz-grouping-path": "/grouping",
	}
	testAuthFlagsAndEnvironment(t, settings, func(t *testing.T, config remoteConsoleConfig) {
		require.Equal(t, "tokensmith", config.AuthProvider)
		require.Equal(t, "http://issuer/keys", config.Tokensmith.JwksURL)
		require.Equal(t, "http://issuer", config.Tokensmith.Issuer)
		require.Equal(t, "remote-console", config.Tokensmith.Audience)
		require.Equal(t, "shadow", config.Tokensmith.AuthzMode)
		require.Equal(t, "/model", config.Tokensmith.AuthzModelPath)
		require.Equal(t, "/policy", config.Tokensmith.AuthzPolicyPath)
		require.Equal(t, "/grouping", config.Tokensmith.AuthzGroupingPath)
	})
}
