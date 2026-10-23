// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package console

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/openchami/remote-console/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func corruptSignature(token string) string {
	parts := strings.Split(token, ".")
	replacement := "A"
	if strings.HasPrefix(parts[2], replacement) {
		replacement = "B"
	}
	parts[2] = replacement + parts[2][1:]
	return strings.Join(parts, ".")
}

// Exercise the real router and JWKS middleware before changing providers.
func TestJWKSAuthentication(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	public, err := jwk.FromRaw(&key.PublicKey)
	require.NoError(t, err)
	require.NoError(t, public.Set(jwk.AlgorithmKey, jwa.RS256))
	require.NoError(t, public.Set(jwk.KeyIDKey, "jwks-key"))
	set := jwk.NewSet()
	require.NoError(t, set.AddKey(public))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		assert.NoError(t, json.NewEncoder(w).Encode(set))
	}))
	defer server.Close()
	authentication, err := auth.NewJWKSAuth(context.Background(), auth.JWKSConfig{JWKSURL: server.URL})
	require.NoError(t, err)
	signer, err := jwk.FromRaw(key)
	require.NoError(t, err)
	require.NoError(t, signer.Set(jwk.KeyIDKey, "jwks-key"))
	sign := func(remove string, expired bool) string {
		claims := map[string]any{"sub": "caller", "iss": "jwks-issuer", "aud": "remote-console", "exp": time.Now().Add(time.Hour).Unix()}
		delete(claims, remove)
		if expired {
			claims["exp"] = time.Now().Add(-time.Hour).Unix()
		}
		payload := jwt.New()
		for k, v := range claims {
			require.NoError(t, payload.Set(k, v))
		}
		token, err := jwt.Sign(payload, jwt.WithKey(jwa.RS256, signer))
		require.NoError(t, err)
		return string(token)
	}
	router := SetupRoutes(t.TempDir(), nil, authentication)
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"valid", sign("", false), 200},
		{"missing", "", 401},
		{"invalid signature", corruptSignature(sign("", false)), 401},
		{"malformed", "not-a-jwt", 401},
		{"expired", sign("", true), 401},
		{"missing subject", sign("sub", false), 401},
		{"missing issuer", sign("iss", false), 401},
		{"missing audience", sign("aud", false), 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/remote-console/consoles", "/remote-console/consoles/x0?mode=invalid"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				if tc.token != "" {
					req.Header.Set("Authorization", "Bearer "+tc.token)
				}
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				want := tc.status
				if want == 200 && path != "/remote-console/consoles" {
					want = 400
				}
				require.Equal(t, want, rr.Code, "%s: %s", path, rr.Body.String())
			}
		})
	}
	for path, status := range map[string]int{"liveness": 204, "readiness": 204, "health": 200} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/remote-console/"+path, nil)
		req.Header.Set("Authorization", "Bearer invalid")
		router.ServeHTTP(rr, req)
		require.Equal(t, status, rr.Code)
	}
	cookieRequest := httptest.NewRequest(http.MethodGet, "/remote-console/consoles", nil)
	cookieRequest.AddCookie(&http.Cookie{Name: "jwt", Value: sign("", false)})
	cookieResponse := httptest.NewRecorder()
	router.ServeHTTP(cookieResponse, cookieRequest)
	require.Equal(t, http.StatusOK, cookieResponse.Code, "JWKS authentication supports JWT cookies")

	disabledAuth, err := auth.NewJWKSAuth(context.Background(), auth.JWKSConfig{})
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	SetupRoutes(t.TempDir(), nil, disabledAuth).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/remote-console/consoles", nil))
	require.Equal(t, http.StatusOK, rr.Code)
}
