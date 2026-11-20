// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchami/tokensmith/pkg/tokenservice"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func newTokenSmithRecoveryServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, string, string) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{ClientAuth: tls.RequestClientCert, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	cert := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	require.NoError(t, err)
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600))
	t.Setenv(tokenservice.ServiceIdentityCertEnvVar, certPath)
	t.Setenv(tokenservice.ServiceIdentityKeyEnvVar, keyPath)
	t.Setenv(tokenservice.BootstrapTokenEnvVar, "must-not-be-replayed")
	return server, certPath, keyPath
}

func TestTokenSmithLostRefreshResponseRecoversSMDRequests(t *testing.T) {
	for _, partialBody := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost headers", true: "truncated body"}[partialBody], func(t *testing.T) {
			var sessions, refreshes, smdRequests atomic.Int32
			var rotated atomic.Bool
			server, certPath, _ := newTokenSmithRecoveryServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/service-identity/session":
					assert.Len(t, r.TLS.PeerCertificates, 1)
					token, ttl := "recovered", 3600
					if sessions.Add(1) == 1 {
						token, ttl = "initial", 1
					} else {
						assert.True(t, rotated.Load(), "recover only after the refresh token was consumed")
					}
					assert.NoError(t, json.NewEncoder(w).Encode(tokenservice.OAuthTokenResponse{
						AccessToken: token, ExpiresIn: ttl, RefreshToken: "session-refresh", RefreshExpiresIn: 86400,
					}))
				case "/oauth/token":
					refreshes.Add(1)
					assert.Empty(t, r.TLS.PeerCertificates)
					assert.NoError(t, r.ParseForm())
					assert.Equal(t, tokenservice.GrantTypeRefreshTokenRFC8693, r.Form.Get("grant_type"))
					assert.Equal(t, "session-refresh", r.Form.Get("refresh_token"))
					if rotated.Swap(true) {
						t.Error("consumed refresh token was replayed")
						http.Error(w, "invalid_grant", http.StatusBadRequest)
						return
					}
					// Rotation committed, but the client never receives the replacement token.
					if partialBody {
						w.Header().Set("Content-Length", "1000")
						_, err := w.Write([]byte(`{"access_token":`))
						assert.NoError(t, err)
						w.(http.Flusher).Flush()
					}
					panic(http.ErrAbortHandler)
				default:
					t.Errorf("unexpected TokenSmith path %q", r.URL.Path)
				}
			})
			source, err := NewTokenSmithTokenSource(context.Background(), TokensmithClientConfig{URL: server.URL, CAFile: certPath})
			require.NoError(t, err)
			smd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				smdRequests.Add(1)
				assert.Equal(t, "Bearer recovered", r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusNoContent)
			}))
			defer smd.Close()
			client := &http.Client{Transport: &oauth2.Transport{Source: source}, Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			results := make(chan error, 4)
			for range cap(results) {
				go func() {
					response, err := client.Get(smd.URL)
					if err == nil {
						err = response.Body.Close()
					}
					results <- err
				}()
			}
			for range cap(results) {
				require.NoError(t, <-results)
			}
			require.EqualValues(t, 2, sessions.Load(), "concurrent SMD requests must share one recovery")
			require.EqualValues(t, 1, refreshes.Load())
			require.EqualValues(t, cap(results), smdRequests.Load())
		})
	}
}

func TestTokenSmithRefreshRejectionDoesNotRenewSession(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var sessions, smdRequests atomic.Int32
			server, certPath, _ := newTokenSmithRecoveryServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/service-identity/session" {
					sessions.Add(1)
					assert.NoError(t, json.NewEncoder(w).Encode(tokenservice.OAuthTokenResponse{
						AccessToken: "initial", ExpiresIn: 1, RefreshToken: "revoked", RefreshExpiresIn: 86400,
					}))
					return
				}
				// TokenSmith uses this same invalid_grant response for revoked families.
				w.WriteHeader(status)
				_, err := w.Write([]byte(`{"error":"invalid_grant","error_description":"The provided refresh token is invalid or has expired"}`))
				assert.NoError(t, err)
			})
			source, err := NewTokenSmithTokenSource(context.Background(), TokensmithClientConfig{URL: server.URL, CAFile: certPath})
			require.NoError(t, err)
			smd := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { smdRequests.Add(1) }))
			defer smd.Close()
			client := &http.Client{Transport: &oauth2.Transport{Source: source}, Timeout: 5 * time.Second}
			for range 2 {
				_, err := client.Get(smd.URL)
				require.Error(t, err)
			}
			require.EqualValues(t, 1, sessions.Load())
			require.Zero(t, smdRequests.Load(), "rejected credentials must not reach SMD")
		})
	}
}

func TestTokenSmithSessionRecoveryIsBounded(t *testing.T) {
	var sessions, refreshes atomic.Int32
	var allowRecovery atomic.Bool
	server, certPath, keyPath := newTokenSmithRecoveryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			refreshes.Add(1)
			assert.NoError(t, r.ParseForm())
			assert.Equal(t, tokenservice.GrantTypeRefreshTokenRFC8693, r.Form.Get("grant_type"))
			panic(http.ErrAbortHandler)
		}
		assert.Len(t, r.TLS.PeerCertificates, 1)
		count := sessions.Add(1)
		if count > 1 && !allowRecovery.Load() {
			http.Error(w, "service identity denied", http.StatusForbidden)
			return
		}
		ttl := 3600
		if count == 1 {
			ttl = 1
		}
		assert.NoError(t, json.NewEncoder(w).Encode(tokenservice.OAuthTokenResponse{
			AccessToken: "service-token", ExpiresIn: ttl, RefreshToken: "refresh", RefreshExpiresIn: 86400,
		}))
	})
	source := &tokenSmithTokenSource{ctx: context.Background(), url: server.URL, certPath: certPath, keyPath: keyPath, httpClient: server.Client()}
	require.NoError(t, source.initialize())
	shared := oauth2.ReuseTokenSource(nil, source)
	for range 2 {
		_, err := shared.Token()
		require.Error(t, err)
	}
	require.EqualValues(t, 2, sessions.Load(), "one recovery attempt during the retry interval")
	require.EqualValues(t, 1, refreshes.Load(), "do not replay the uncertain refresh token")

	allowRecovery.Store(true)
	require.NotNil(t, source.nextRenewal)
	*source.nextRenewal = time.Now().Add(-time.Second) // Advance past the cooldown without sleeping.
	token, err := shared.Token()
	require.NoError(t, err)
	require.True(t, token.Valid())
	require.EqualValues(t, 3, sessions.Load())
	require.EqualValues(t, 1, refreshes.Load())
}
