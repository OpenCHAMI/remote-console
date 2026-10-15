// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
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
)

func TestTokenSmithTokenSourceRefresh(t *testing.T) {
	t.Setenv(tokenservice.ServiceIdentityCertEnvVar, "")
	t.Setenv(tokenservice.ServiceIdentityKeyEnvVar, "")
	t.Setenv(tokenservice.BootstrapTokenEnvVar, "bootstrap")
	var exchanges, refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/oauth/token", r.URL.Path)
		assert.NoError(t, r.ParseForm())
		response := tokenservice.OAuthTokenResponse{
			AccessToken: "refreshed", ExpiresIn: 3600,
			RefreshToken: "rotated", RefreshExpiresIn: 86400,
		}
		switch r.Form.Get("grant_type") {
		case tokenservice.GrantTypeTokenExchange:
			exchanges.Add(1)
			assert.Equal(t, "bootstrap", r.Form.Get("subject_token"))
			response.AccessToken, response.ExpiresIn, response.RefreshToken = "initial", 1, "refresh"
		case tokenservice.GrantTypeRefreshTokenRFC8693:
			refreshes.Add(1)
			assert.Equal(t, "refresh", r.Form.Get("refresh_token"))
		default:
			t.Errorf("unexpected grant %q", r.Form.Get("grant_type"))
		}
		assert.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	defer server.Close()
	source, err := NewTokenSmithTokenSource(context.Background(), TokensmithClientConfig{URL: server.URL})
	require.NoError(t, err)

	// Concurrent callers must share the refreshed token, not race to redeem
	// the same single-use refresh token.
	results := make(chan error, 16)
	for range cap(results) {
		go func() {
			token, err := source.Token()
			if err == nil && (token.AccessToken != "refreshed" || token.TokenType != "Bearer" || !token.Valid()) {
				err = fmt.Errorf("unexpected token returned by adapter")
			}
			results <- err
		}()
	}
	for range cap(results) {
		require.NoError(t, <-results)
	}
	require.EqualValues(t, 1, exchanges.Load())
	require.EqualValues(t, 1, refreshes.Load())
}

func TestTokenSmithServiceIdentity(t *testing.T) {
	t.Setenv(tokenservice.BootstrapTokenEnvVar, "")
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := tokenservice.OAuthTokenResponse{
			AccessToken: "service-token", ExpiresIn: 1,
			RefreshToken: "refresh", RefreshExpiresIn: 86400,
		}
		if r.URL.Path == "/service-identity/session" {
			assert.Len(t, r.TLS.PeerCertificates, 1)
		} else {
			assert.Equal(t, "/oauth/token", r.URL.Path)
			assert.Empty(t, r.TLS.PeerCertificates, "refresh must not send a cached client certificate")
			response.AccessToken, response.ExpiresIn = "refreshed", 3600
		}
		assert.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequestClientCert, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()

	// Reuse the test server's key pair as client material to exercise remote-console's
	// certificate loading and custom CA configuration.
	certPath, keyPath := filepath.Join(t.TempDir(), "cert.pem"), filepath.Join(t.TempDir(), "key.pem")
	cert := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600))
	t.Setenv(tokenservice.ServiceIdentityCertEnvVar, certPath)
	t.Setenv(tokenservice.ServiceIdentityKeyEnvVar, keyPath)
	source, err := NewTokenSmithTokenSource(context.Background(), TokensmithClientConfig{URL: server.URL, CAFile: certPath})
	require.NoError(t, err)
	token, err := source.Token()
	require.NoError(t, err)
	require.Equal(t, "refreshed", token.AccessToken)
}

func TestServiceIdentityRenewalLoadsRotatedCertificate(t *testing.T) {
	t.Setenv(tokenservice.BootstrapTokenEnvVar, "")
	var sessions atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/service-identity/session", r.URL.Path)
		if !assert.Len(t, r.TLS.PeerCertificates, 1) {
			return
		}
		response := tokenservice.OAuthTokenResponse{
			AccessToken: "renewed", ExpiresIn: 3600,
			RefreshToken: "refresh", RefreshExpiresIn: 86400,
		}
		if sessions.Add(1) == 1 {
			response.AccessToken, response.ExpiresIn, response.RefreshExpiresIn = "initial", 1, 1
		} else {
			assert.Equal(t, int64(42), r.TLS.PeerCertificates[0].SerialNumber.Int64(), "renewal must load the rotated certificate")
		}
		assert.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()

	cert := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	require.NoError(t, err)
	dir := t.TempDir()
	certPath, keyPath, caPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), filepath.Join(dir, "ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	require.NoError(t, os.WriteFile(certPath, certPEM, 0600))
	require.NoError(t, os.WriteFile(caPath, certPEM, 0600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600))
	t.Setenv(tokenservice.ServiceIdentityCertEnvVar, certPath)
	t.Setenv(tokenservice.ServiceIdentityKeyEnvVar, keyPath)
	source, err := NewTokenSmithTokenSource(context.Background(), TokensmithClientConfig{URL: server.URL, CAFile: caPath})
	require.NoError(t, err)

	// Let the real SDK expire its private refresh session.
	time.Sleep(1100 * time.Millisecond)
	rotated := *server.Certificate()
	rotated.SerialNumber = big.NewInt(42)
	der, err := x509.CreateCertificate(rand.Reader, &rotated, &rotated, rotated.PublicKey, cert.PrivateKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	results := make(chan error, 16)
	for range cap(results) {
		go func() {
			token, err := source.Token()
			if err == nil && token.AccessToken != "renewed" {
				err = fmt.Errorf("unexpected renewed token")
			}
			results <- err
		}()
	}
	for range cap(results) {
		require.NoError(t, <-results)
	}
	require.EqualValues(t, 2, sessions.Load(), "concurrent callers must share one renewed session")
}

func TestMissingServiceIdentityDoesNotUseBootstrap(t *testing.T) {
	t.Setenv(tokenservice.BootstrapTokenEnvVar, "unused-bootstrap")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.NoError(t, json.NewEncoder(w).Encode(tokenservice.OAuthTokenResponse{
			AccessToken: "unexpected-bootstrap", ExpiresIn: 3600,
			RefreshToken: "refresh", RefreshExpiresIn: 86400,
		}))
	}))
	defer server.Close()
	dir := t.TempDir()
	// Session initialization must reject missing files before the SDK can
	// fall back to the available bootstrap token.
	source := tokenSmithTokenSource{
		ctx: context.Background(), url: server.URL, httpClient: server.Client(),
		certPath: filepath.Join(dir, "missing-cert.pem"),
		keyPath:  filepath.Join(dir, "missing-key.pem"),
	}
	err := source.initialize()
	require.ErrorContains(t, err, "load TokenSmith service identity")
	require.Zero(t, requests.Load())
}

func TestBootstrapExchangeDoesNotRetry(t *testing.T) {
	t.Setenv(tokenservice.ServiceIdentityCertEnvVar, "")
	t.Setenv(tokenservice.ServiceIdentityKeyEnvVar, "")
	t.Setenv(tokenservice.BootstrapTokenEnvVar, "bootstrap")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	_, err := NewTokenSmithTokenSource(context.Background(), TokensmithClientConfig{URL: server.URL})
	require.Error(t, err)
	require.EqualValues(t, 1, requests.Load())
}

func TestExpiredSessionDoesNotReplayBootstrap(t *testing.T) {
	t.Setenv(tokenservice.ServiceIdentityCertEnvVar, "")
	t.Setenv(tokenservice.ServiceIdentityKeyEnvVar, "")
	t.Setenv(tokenservice.BootstrapTokenEnvVar, "bootstrap")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.NoError(t, json.NewEncoder(w).Encode(tokenservice.OAuthTokenResponse{
			AccessToken: "initial", ExpiresIn: 1,
			RefreshToken: "refresh", RefreshExpiresIn: 1,
		}))
	}))
	defer server.Close()
	source, err := NewTokenSmithTokenSource(context.Background(), TokensmithClientConfig{URL: server.URL})
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond)
	_, err = source.Token()
	require.ErrorIs(t, err, tokenservice.ErrRefreshTokenExpired)
	require.EqualValues(t, 1, requests.Load())
}

func TestTokenSmithClientInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, url, cert, key, bootstrap, ca, message string }{
		{name: "URL", url: "", message: "valid smd-tokensmith-url"},
		{name: "credentials", url: "http://localhost", message: "requires TOKENSMITH_BOOTSTRAP_TOKEN"},
		{name: "certificate pair", url: "https://localhost", cert: "/missing/cert", message: "must be set together"},
		{name: "TLS required", url: "http://localhost", cert: "/missing/cert", key: "/missing/key", message: "requires an HTTPS URL"},
		{name: "missing identity", url: "https://localhost", cert: "/missing/cert", key: "/missing/key", bootstrap: "unused", message: "load TokenSmith service identity"},
		{name: "missing CA", url: "https://localhost", bootstrap: "unused", ca: "/missing/ca", message: "read TokenSmith CA file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tokenservice.ServiceIdentityCertEnvVar, tc.cert)
			t.Setenv(tokenservice.ServiceIdentityKeyEnvVar, tc.key)
			t.Setenv(tokenservice.BootstrapTokenEnvVar, tc.bootstrap)
			source, err := NewTokenSmithTokenSource(context.Background(), TokensmithClientConfig{URL: tc.url, CAFile: tc.ca})
			require.ErrorContains(t, err, tc.message)
			require.Nil(t, source)
		})
	}
}
