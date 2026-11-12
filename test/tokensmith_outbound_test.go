// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/stretchr/testify/require"
)

func (s *IntegrationTestSuite) bootstrap() string {
	code, output, err := s.tokensmith.Exec(context.Background(), []string{"tokensmith", "bootstrap-token", "create", "--subject=remote-console", "--audience=smd", "--scopes=read", "--ttl=10m", "--refresh-ttl=24h", "--bootstrap-store=/tmp/bootstrap", "--output-format=json"})
	require.NoError(s.T(), err)
	var stdout bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, io.Discard, output)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0, code)
	var response struct {
		Token string `json:"bootstrap_token"`
	}
	require.NoError(s.T(), json.Unmarshal(stdout.Bytes(), &response))
	require.NotEmpty(s.T(), response.Token)
	return response.Token
}

func (s *IntegrationTestSuite) startTokenSmithSMD() {
	s.smdTokenSmithURL = s.startSMD("smd-tokensmith", tokenSmithIssuer+"/.well-known/jwks.json")
}

func (s *IntegrationTestSuite) TestTokenSmithOutbound() {
	for _, token := range []string{"", s.fetchToken("test-caller")} {
		status, _ := request(s.T(), http.MethodGet, s.smdTokenSmithURL+inventoryPath, token, nil)
		require.Equal(s.T(), http.StatusUnauthorized, status)
	}
	for _, inbound := range []string{"jwks", "tokensmith"} {
		s.Run(inbound, func() {
			env := map[string]string{
				"RCS_SMD_AUTH_PROVIDER": "tokensmith", "RCS_SMD_TOKENSMITH_URL": tokenSmithIssuer,
				"RCS_OAUTH2_TOKEN_URL": "", "TOKENSMITH_BOOTSTRAP_TOKEN": s.bootstrap(),
				"RCS_SMD_URL": "http://smd-tokensmith:27779",
			}
			token := s.fetchToken("test-caller")
			if inbound == "tokensmith" {
				maps.Copy(env, tokenSmithEnv())
				token = s.mintToken("remote-console-operator")
			}
			console := s.startConsole(env)
			assertDiscovery(s.T(), console, token)
			for _, bad := range []string{"", "invalid"} {
				status, _ := request(s.T(), http.MethodGet, console+"/remote-console/consoles", bad, nil)
				require.Equal(s.T(), http.StatusUnauthorized, status)
			}
		})
	}
}

func (s *IntegrationTestSuite) TestTokenSmithOutboundInvalidConfiguration() {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		message string
	}{
		{"outbound provider", map[string]string{"RCS_SMD_AUTH_PROVIDER": "invalid"}, "invalid smd-auth-provider"},
		{"outbound credentials", map[string]string{"RCS_SMD_AUTH_PROVIDER": "tokensmith", "RCS_SMD_TOKENSMITH_URL": tokenSmithIssuer}, "requires TOKENSMITH_BOOTSTRAP_TOKEN"},
		{"bootstrap exchange", map[string]string{"RCS_SMD_AUTH_PROVIDER": "tokensmith", "RCS_SMD_TOKENSMITH_URL": tokenSmithIssuer, "TOKENSMITH_BOOTSTRAP_TOKEN": "invalid"}, "initialize outbound TokenSmith"},
	} {
		s.Run(tc.name, func() { s.assertStartupFailure(tc.env, tc.message) })
	}
}
