// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package test

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

const tokenSmithIssuer = "http://tokensmith:8080"

func (s *IntegrationTestSuite) startTokenSmith() {
	t := s.T()
	s.tokensmith = newContainer(t, testcontainers.ContainerRequest{
		Image:    "ghcr.io/openchami/tokensmith:v0.4.2",
		Env:      map[string]string{"TOKENSMITH_ISSUER": tokenSmithIssuer, "TOKENSMITH_OIDC_PROVIDER": "http://keycloak:8080/realms/rcs-auth-test", "TOKENSMITH_CONFIG": "", "TOKENSMITH_KEY_DIR": "/tmp/keys", "TOKENSMITH_RFC8693_BOOTSTRAP_STORE": "/tmp/bootstrap", "TOKENSMITH_RFC8693_REFRESH_STORE": "/tmp/refresh"},
		Networks: []string{s.rcsNetwork.Name}, NetworkAliases: map[string][]string{s.rcsNetwork.Name: {"tokensmith"}}, ExposedPorts: []string{"8080/tcp"}, WaitingFor: wait.ForHTTP("/.well-known/jwks.json").WithPort("8080/tcp"),
	})
}

func tokenSmithEnv() map[string]string {
	return map[string]string{"RCS_AUTH_PROVIDER": "tokensmith", "RCS_JWKS_URL": "http://invalid-jwks-provider", "RCS_TOKENSMITH_ISSUER": tokenSmithIssuer, "RCS_TOKENSMITH_JWKS_URL": tokenSmithIssuer + "/.well-known/jwks.json", "RCS_TOKENSMITH_AUTHZ_POLICY_PATH": "/app/configs/authz/policy.csv", "RCS_TOKENSMITH_AUTHZ_GROUPING_PATH": "/app/configs/authz/grouping.csv"}
}

func (s *IntegrationTestSuite) mintToken(scope string) string {
	code, output, err := s.tokensmith.Exec(context.Background(), []string{"tokensmith", "user-token", "create", "--enable-local-user-mint", "--key-file=/tmp/keys/private.pem", "--subject=rcs-test-caller", "--issuer=" + tokenSmithIssuer, "--audience=remote-console", "--scopes=" + scope}, tcexec.Multiplexed())
	require.NoError(s.T(), err)
	data, err := io.ReadAll(output)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0, code, "%s", data)
	return strings.TrimSpace(string(data))
}

func (s *IntegrationTestSuite) TestTokenSmithAuthentication() {
	console := s.startConsole(tokenSmithEnv())
	assertDiscovery(s.T(), console, s.mintToken("remote-console-operator"))
	for _, token := range []string{"", "invalid", s.fetchToken("test-caller")} {
		status, _ := request(s.T(), http.MethodGet, console+"/remote-console/consoles", token, nil)
		require.Equal(s.T(), http.StatusUnauthorized, status)
	}
	for _, caller := range []struct {
		role   string
		status int
	}{
		{"remote-console-reader", http.StatusOK},
		{"unprivileged", http.StatusForbidden},
	} {
		status, body := request(s.T(), http.MethodGet, console+"/remote-console/consoles", s.mintToken(caller.role), nil)
		require.Equal(s.T(), caller.status, status, "%s", body)
	}
	for path, status := range map[string]int{"liveness": 204, "readiness": 204, "health": 200} {
		actual, _ := request(s.T(), http.MethodGet, console+"/remote-console/"+path, "", nil)
		require.Equal(s.T(), status, actual)
	}
}

func (s *IntegrationTestSuite) TestTokenSmithConsoleAuthorization() {
	console := s.startConsole(tokenSmithEnv())
	reader := s.mintToken("remote-console-reader")
	operator := s.mintToken("remote-console-operator")
	denied := s.mintToken("unprivileged")
	assertDiscovery(s.T(), console, operator)
	for _, tc := range []struct {
		name, query, token string
		status             int
	}{
		{"reader tail", "?mode=tail", reader, http.StatusSwitchingProtocols},
		{"reader interactive", "?mode=interactive", reader, http.StatusForbidden},
		{"reader default", "", reader, http.StatusForbidden},
		{"reader empty mode", "?mode=", reader, http.StatusForbidden},
		{"operator tail", "?mode=tail", operator, http.StatusSwitchingProtocols},
		{"operator interactive", "?mode=interactive", operator, http.StatusSwitchingProtocols},
		{"operator default", "", operator, http.StatusSwitchingProtocols},
		{"missing tail", "?mode=tail", "", http.StatusUnauthorized},
		{"missing interactive", "?mode=interactive", "", http.StatusUnauthorized},
		{"denied tail", "?mode=tail", denied, http.StatusForbidden},
		{"denied interactive", "?mode=interactive", denied, http.StatusForbidden},
	} {
		s.Run(tc.name, func() {
			conn, resp, err := websocket.DefaultDialer.Dial(
				"ws"+strings.TrimPrefix(console, "http")+"/remote-console/consoles/x0c0s0b0n0"+tc.query,
				http.Header{"Authorization": []string{"Bearer " + tc.token}},
			)
			require.NotNil(s.T(), resp)
			require.Equal(s.T(), tc.status, resp.StatusCode)
			require.NoError(s.T(), resp.Body.Close())
			if tc.status == http.StatusSwitchingProtocols {
				require.NoError(s.T(), err)
				require.NoError(s.T(), conn.Close())
			} else {
				require.Error(s.T(), err)
				require.Nil(s.T(), conn)
			}
		})
	}
}

func (s *IntegrationTestSuite) TestTokenSmithModes() {
	for _, mode := range []string{"shadow", "off"} {
		s.Run(mode, func() {
			env := tokenSmithEnv()
			env["RCS_TOKENSMITH_AUTHZ_MODE"] = mode
			if mode == "off" {
				env["RCS_TOKENSMITH_AUTHZ_POLICY_PATH"] = ""
				env["RCS_TOKENSMITH_AUTHZ_GROUPING_PATH"] = ""
			}
			console := s.startConsole(env)
			status, _ := request(s.T(), http.MethodGet, console+"/remote-console/consoles", s.mintToken("unprivileged"), nil)
			require.Equal(s.T(), 200, status)
			status, _ = request(s.T(), http.MethodGet, console+"/remote-console/consoles", "", nil)
			require.Equal(s.T(), 401, status)
		})
	}
}

func (s *IntegrationTestSuite) TestTokenSmithInvalidConfiguration() {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		message string
	}{
		{"inbound provider", map[string]string{"RCS_AUTH_PROVIDER": "invalid"}, "invalid auth-provider"},
		{"TokenSmith config", map[string]string{"RCS_AUTH_PROVIDER": "tokensmith"}, "TokenSmith requires"},
	} {
		s.Run(tc.name, func() { s.assertStartupFailure(tc.env, tc.message) })
	}
}
