// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package console

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/openchami/remote-console/internal/auth"
	"github.com/openchami/tokensmith/pkg/keys"
	tstoken "github.com/openchami/tokensmith/pkg/token"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tokenSmithTestIssuer struct {
	key *ecdsa.PrivateKey
	kid string
	url string
}

func newTokenSmithTestIssuer(t *testing.T) *tokenSmithTestIssuer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	kid, err := keys.RFC7638Thumbprint(&key.PublicKey)
	require.NoError(t, err)
	public, err := jwk.FromRaw(&key.PublicKey)
	require.NoError(t, err)
	require.NoError(t, public.Set(jwk.KeyIDKey, kid))
	require.NoError(t, public.Set(jwk.AlgorithmKey, "ES256"))
	set := jwk.NewSet()
	require.NoError(t, set.AddKey(public))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		assert.NoError(t, json.NewEncoder(w).Encode(set))
	}))
	t.Cleanup(server.Close)
	return &tokenSmithTestIssuer{key: key, kid: kid, url: server.URL}
}

func (issuer *tokenSmithTestIssuer) sign(t *testing.T, role string, mutate func(*tstoken.TSClaims)) string {
	t.Helper()
	claims := tstoken.NewClaims()
	claims.Issuer, claims.Subject, claims.Audience = issuer.url, "test-caller", []string{"remote-console"}
	claims.Scope = []string{role}
	claims.AuthLevel, claims.AuthFactors, claims.AuthMethods = "AAL2", 2, []string{"pwd", "otp"}
	claims.SessionID, claims.AuthEvents = "test-session", []string{"login"}
	if mutate != nil {
		mutate(claims)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = issuer.kid
	encoded, err := token.SignedString(issuer.key)
	require.NoError(t, err)
	return encoded
}

func (issuer *tokenSmithTestIssuer) config(mode string) auth.TokensmithConfig {
	config := auth.TokensmithConfig{
		Issuer: issuer.url, JwksURL: issuer.url, Audience: "remote-console", AuthzMode: mode,
		AuthzPolicyPath: "../../configs/authz/policy.csv", AuthzGroupingPath: "../../configs/authz/grouping.csv",
	}
	if mode == "off" {
		config.AuthzPolicyPath, config.AuthzGroupingPath = "", ""
	}
	return config
}

func newTokenSmithTestRouter(t *testing.T, config auth.TokensmithConfig) http.Handler {
	t.Helper()
	authentication, err := auth.NewTokenSmithAuth(context.Background(), config)
	require.NoError(t, err)
	return SetupRoutes(t.TempDir(), nil, authentication)
}

func tokenSmithRequest(t *testing.T, router http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func requireTokenSmithDenial(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusForbidden, response.Code)
	// Inspect wire names independently of TokenSmith types.
	var denial map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &denial))
	require.Equal(t, "policy_denied", denial["reason"])
	require.NotEmpty(t, denial["policy_version"])
}

func TestTokenSmithAuthentication(t *testing.T) {
	issuer := newTokenSmithTestIssuer(t)
	reader := issuer.sign(t, "remote-console-reader", nil)
	for _, mode := range []string{"enforce", "shadow", "off"} {
		t.Run(mode, func(t *testing.T) {
			router := newTokenSmithTestRouter(t, issuer.config(mode))
			require.Equal(t, http.StatusOK, tokenSmithRequest(t, router, "/remote-console/consoles", reader).Code)
			for _, tc := range []struct{ name, token string }{
				{"missing", ""},
				{"malformed", "invalid"},
				{"invalid signature", corruptSignature(reader)},
				{"wrong issuer", issuer.sign(t, "remote-console-reader", func(c *tstoken.TSClaims) { c.Issuer = "other" })},
				{"wrong audience", issuer.sign(t, "remote-console-reader", func(c *tstoken.TSClaims) { c.Audience = []string{"smd"} })},
				{"expired", issuer.sign(t, "remote-console-reader", func(c *tstoken.TSClaims) { c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour)) })},
				{"missing subject", issuer.sign(t, "remote-console-reader", func(c *tstoken.TSClaims) { c.Subject = "" })},
				{"invalid TokenSmith claims", issuer.sign(t, "remote-console-reader", func(c *tstoken.TSClaims) { c.AuthFactors = 1 })},
			} {
				t.Run(tc.name, func(t *testing.T) {
					for _, path := range []string{"/remote-console/consoles", "/remote-console/consoles/x0?mode=invalid"} {
						response := tokenSmithRequest(t, router, path, tc.token)
						require.Equal(t, http.StatusUnauthorized, response.Code, "%s: %s", path, response.Body.String())
					}
				})
			}
		})
	}
}

func TestTokenSmithAuthorization(t *testing.T) {
	issuer := newTokenSmithTestIssuer(t)
	for _, mode := range []string{"enforce", "shadow", "off"} {
		t.Run(mode, func(t *testing.T) {
			router := newTokenSmithTestRouter(t, issuer.config(mode))
			for _, tc := range []struct {
				name, role                string
				listStatus, consoleStatus int
			}{
				{"reader", "remote-console-reader", http.StatusOK, http.StatusForbidden},
				{"role inheritance", "remote-console-operator", http.StatusOK, http.StatusBadRequest},
				{"denied", "unprivileged", http.StatusForbidden, http.StatusForbidden},
			} {
				t.Run(tc.name, func(t *testing.T) {
					token := issuer.sign(t, tc.role, nil)
					listStatus, consoleStatus := tc.listStatus, tc.consoleStatus
					if mode != "enforce" {
						listStatus, consoleStatus = http.StatusOK, http.StatusBadRequest
					}
					for path, want := range map[string]int{
						"/remote-console/consoles":                 listStatus,
						"/remote-console/consoles/x0?mode=invalid": consoleStatus,
					} {
						response := tokenSmithRequest(t, router, path, token)
						require.Equal(t, want, response.Code, "%s: %s", path, response.Body.String())
						if want == http.StatusForbidden {
							requireTokenSmithDenial(t, response)
						}
					}
				})
			}
		})
	}
}

func TestTokenSmithPublicHealthEndpoints(t *testing.T) {
	issuer := newTokenSmithTestIssuer(t)
	for _, mode := range []string{"enforce", "shadow", "off"} {
		t.Run(mode, func(t *testing.T) {
			router := newTokenSmithTestRouter(t, issuer.config(mode))
			for path, want := range map[string]int{"liveness": 204, "readiness": 204, "health": 200} {
				response := tokenSmithRequest(t, router, "/remote-console/"+path, "invalid")
				require.Equal(t, want, response.Code, "%s", path)
			}
		})
	}
}

func TestTokenSmithConsolePermissions(t *testing.T) {
	issuer := newTokenSmithTestIssuer(t)
	for _, mode := range []string{"enforce", "shadow", "off"} {
		t.Run(mode, func(t *testing.T) {
			router := newTokenSmithTestRouter(t, issuer.config(mode))
			for _, tc := range []struct {
				name, query  string
				readerStatus int
			}{
				{"tail", "?mode=tail", http.StatusNotFound},
				{"interactive", "?mode=interactive", http.StatusForbidden},
				{"default interactive", "", http.StatusForbidden},
				{"empty mode", "?mode=", http.StatusForbidden},
				{"first tail", "?mode=tail&mode=interactive", http.StatusNotFound},
				{"first interactive", "?mode=interactive&mode=tail", http.StatusForbidden},
				{"encoded tail", "?mode=%74ail", http.StatusNotFound},
			} {
				t.Run(tc.name, func(t *testing.T) {
					for _, role := range []string{"remote-console-reader", "remote-console-operator"} {
						response := tokenSmithRequest(t, router, "/remote-console/consoles/x0"+tc.query, issuer.sign(t, role, nil))
						want := http.StatusNotFound // Authorized requests reach the missing-node check.
						if mode == "enforce" && role == "remote-console-reader" {
							want = tc.readerStatus
						}
						require.Equal(t, want, response.Code, "%s: %s", role, response.Body.String())
					}
				})
			}
		})
	}
}

func TestTokenSmithWebSocketRejection(t *testing.T) {
	issuer := newTokenSmithTestIssuer(t)
	denied := issuer.sign(t, "unprivileged", nil)
	for _, mode := range []string{"enforce", "shadow", "off"} {
		t.Run(mode, func(t *testing.T) {
			router := newTokenSmithTestRouter(t, issuer.config(mode))
			server := httptest.NewServer(router)
			t.Cleanup(server.Close)
			for _, token := range []string{"", denied} {
				if token == denied && mode != "enforce" {
					continue
				}
				for _, consoleMode := range []string{"tail", "interactive"} {
					conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/remote-console/consoles/x0?mode="+consoleMode, http.Header{"Authorization": []string{"Bearer " + token}})
					require.Error(t, err)
					require.Nil(t, conn)
					require.NotNil(t, resp)
					require.NoError(t, resp.Body.Close())
					want := http.StatusUnauthorized
					if token != "" {
						want = http.StatusForbidden
					}
					require.Equal(t, want, resp.StatusCode)
				}
			}
		})
	}
}

func TestTokenSmithNodePolicy(t *testing.T) {
	issuer := newTokenSmithTestIssuer(t)
	policy := filepath.Join(t.TempDir(), "policy.csv")
	require.NoError(t, os.WriteFile(policy, []byte("p, role:remote-console-reader, /remote-console/consoles, read\n"), 0600))
	config := issuer.config("enforce")
	config.AuthzPolicyPath, config.AuthzGroupingPath = policy, ""
	router := newTokenSmithTestRouter(t, config)
	reader := issuer.sign(t, "remote-console-reader", nil)

	require.Equal(t, http.StatusOK, tokenSmithRequest(t, router, "/remote-console/consoles", reader).Code)
	requireTokenSmithDenial(t, tokenSmithRequest(t, router, "/remote-console/consoles/x0?mode=tail", reader))
}
