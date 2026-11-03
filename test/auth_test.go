// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func assertDiscovery(t *testing.T, console, token string) {
	t.Helper()
	// The inventory comes from real SMD. An empty 200 cannot satisfy this check.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, data := request(t, http.MethodGet, console+"/remote-console/consoles", token, nil)
		if status == 200 && bytes.Contains(data, []byte("x0c0s0b0n0")) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("remote-console must discover the protected SMD inventory")
}

func (s *IntegrationTestSuite) TestJWKSAndOAuth2Providers() {
	for _, inbound := range []bool{false, true} {
		for _, outbound := range []bool{false, true} {
			s.Run(strings.Join([]string{map[bool]string{false: "anonymous", true: "jwks"}[inbound], map[bool]string{false: "anonymous", true: "oauth2"}[outbound]}, "/"), func() {
				env := map[string]string{}
				token := ""
				if inbound {
					token = s.fetchToken("test-caller")
				} else {
					env["RCS_JWKS_URL"] = ""
				}
				if !outbound {
					for name := range oauthEnv() {
						env[name] = ""
					}
					env["RCS_SMD_URL"] = "http://smd:27779"
				}
				console := s.startConsole(env)
				status, _ := request(s.T(), http.MethodGet, console+"/remote-console/consoles", "", nil)
				want := 200
				if inbound {
					want = 401
				}
				require.Equal(s.T(), want, status)
				assertDiscovery(s.T(), console, token)
			})
		}
	}
	// Prove SMD itself requires credentials.
	for _, token := range []string{"", "invalid"} {
		status, _ := request(s.T(), http.MethodGet, s.smdURL+inventoryPath, token, nil)
		require.Equal(s.T(), 401, status)
	}
}

func (s *IntegrationTestSuite) TestOAuth2FlagsOverrideEnvironment() {
	env := oauthEnv()
	for name := range env {
		env[name] = "invalid"
	}
	console := s.startConsole(env, "--oauth2-client-id=rcs-service", "--oauth2-client-secret=rcs-service-test-secret", "--oauth2-token-url="+tokenURL, "--oauth2-scopes=rcs-test")
	assertDiscovery(s.T(), console, s.fetchToken("test-caller"))
}

func (s *IntegrationTestSuite) TestOAuth2TokenExpiryAndRenewal() {
	console := s.startConsole(nil)
	caller := s.fetchToken("expired-caller")
	status, _ := request(s.T(), http.MethodGet, console+"/remote-console/consoles", caller, nil)
	require.Equal(s.T(), 200, status)
	waitFor(s.T(), 15*time.Second, func() bool {
		status, _ := request(s.T(), http.MethodGet, console+"/remote-console/consoles", caller, nil)
		return status == 401
	})
	token := s.fetchToken("test-caller")
	assertDiscovery(s.T(), console, token)
	control := s.fetchToken("rcs-service")
	waitFor(s.T(), 45*time.Second, func() bool {
		status, _ := request(s.T(), http.MethodGet, s.smdURL+inventoryPath, control, nil)
		return status == 401
	})
	// Discover a new console after the original outbound token has expired.
	// Its appearance proves remote-console made a fresh authenticated SMD request.
	s.addConsoleEndpoint("x0c0s9b0")
	waitFor(s.T(), 30*time.Second, func() bool {
		status, data := request(s.T(), http.MethodGet, console+"/remote-console/consoles", token, nil)
		return status == 200 && bytes.Contains(data, []byte("x0c0s9b0n0"))
	})
}

func (s *IntegrationTestSuite) TestStartupFailures() {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		message string
	}{
		{"incomplete OAuth2", map[string]string{"RCS_OAUTH2_CLIENT_ID": "incomplete"}, "incomplete OAuth2 configuration"},
		{"JWKS failure", map[string]string{"RCS_JWKS_URL": jwksURL + "/missing", "RCS_JWKS_FETCH_INTERVAL": "0"}, "initialize JWKS"},
	} {
		s.Run(tc.name, func() { s.assertStartupFailure(tc.env, tc.message) })
	}
}
