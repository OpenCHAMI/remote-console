// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestJWKSInitializationFailsClosed(t *testing.T) {
	for _, body := range []string{"invalid", "", `{"keys":[]}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			provider, err := NewJWKSAuth(context.Background(), JWKSConfig{JWKSURL: server.URL})
			require.ErrorContains(t, err, "initialize JWKS")
			require.Nil(t, provider)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider, err := NewJWKSAuth(ctx, JWKSConfig{JWKSURL: "http://localhost:1", RetryInterval: time.Hour})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, provider)
}

func TestTokenSmithInitializationFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"malformed", "invalid", 200}, {"empty keys", `{"keys":[]}`, 200}, {"HTTP failure", `{"keys":[]}`, 503},
		{"symmetric key", `{"keys":[{"kty":"oct","k":"c2VjcmV0","kid":"test","alg":"HS256"}]}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			provider, err := NewTokenSmithAuth(context.Background(), TokensmithConfig{Issuer: server.URL, JwksURL: server.URL, Audience: "remote-console", AuthzMode: "off"})
			require.ErrorContains(t, err, "initialize TokenSmith JWKS")
			require.Nil(t, provider)
		})
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*TokensmithConfig)
		message string
	}{
		{"mode", func(c *TokensmithConfig) { c.AuthzMode = "invalid" }, "invalid tokensmith-authz-mode"},
		{"issuer", func(c *TokensmithConfig) { c.Issuer = "" }, "requires tokensmith-issuer"},
		{"audience", func(c *TokensmithConfig) { c.Audience = "" }, "requires tokensmith-issuer"},
		{"JWKS", func(c *TokensmithConfig) { c.JwksURL = "" }, "requires tokensmith-issuer"},
		{"policy required", func(c *TokensmithConfig) { c.AuthzPolicyPath = "" }, "requires tokensmith-authz-policy-path"},
		{"policy unreadable", func(c *TokensmithConfig) { c.AuthzPolicyPath = "/missing/policy" }, "configure TokenSmith authorization"},
		{"model unreadable", func(c *TokensmithConfig) { c.AuthzModelPath = "/missing/model" }, "configure TokenSmith authorization"},
		{"grouping unreadable", func(c *TokensmithConfig) { c.AuthzGroupingPath = "/missing/grouping" }, "configure TokenSmith authorization"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := TokensmithConfig{Issuer: "http://issuer", JwksURL: "http://localhost:1", Audience: "remote-console", AuthzMode: "enforce", AuthzPolicyPath: "../../configs/authz/policy.csv"}
			tc.mutate(&config)
			provider, err := NewTokenSmithAuth(context.Background(), config)
			require.ErrorContains(t, err, tc.message)
			require.Nil(t, provider)
		})
	}
}
