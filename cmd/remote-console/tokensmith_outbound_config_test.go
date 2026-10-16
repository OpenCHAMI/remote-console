// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSMDTokenSmithFlagsAndEnvironment(t *testing.T) {
	settings := map[string]string{
		"smd-auth-provider":      "tokensmith",
		"smd-tokensmith-url":     "http://tokensmith",
		"smd-tokensmith-ca-file": "/ca",
	}
	testAuthFlagsAndEnvironment(t, settings, func(t *testing.T, config remoteConsoleConfig) {
		require.Equal(t, "tokensmith", config.SmdAuthProvider)
		require.Equal(t, "http://tokensmith", config.SmdTokensmith.URL)
		require.Equal(t, "/ca", config.SmdTokensmith.CAFile)
	})
}
