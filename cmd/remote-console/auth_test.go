// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuth2SMDClient(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "anonymous", true: "oauth2"}[enabled], func(t *testing.T) {
			var exchanges atomic.Int32
			tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id, secret, ok := r.BasicAuth()
				assert.True(t, ok)
				assert.Equal(t, "console-service", id)
				assert.Equal(t, "test-secret", secret)
				assert.NoError(t, r.ParseForm())
				assert.Equal(t, "client_credentials", r.Form.Get("grant_type"))
				assert.Equal(t, "read inventory", r.Form.Get("scope"))
				exchanges.Add(1)
				w.Header().Set("Content-Type", "application/json")
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"access_token": "smd-service-token", "token_type": "Bearer", "expires_in": 3600}))
			}))
			defer tokens.Close()
			smd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := ""
				if enabled {
					want = "Bearer smd-service-token"
				}
				assert.Equal(t, want, r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusNoContent)
			}))
			defer smd.Close()
			config := DefaultConfig()
			if enabled {
				config.Oauth2 = OAuth2Config{ClientID: "console-service", ClientSecret: "test-secret", TokenURL: tokens.URL, Scopes: []string{"read", "inventory"}}
			}
			client := newSMDHTTPClient(context.Background(), config)
			require.Equal(t, smdHTTPTimeout, client.Timeout)
			for range 2 {
				resp, err := client.Get(smd.URL)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, http.StatusNoContent, resp.StatusCode)
			}
			if enabled {
				require.EqualValues(t, 1, exchanges.Load(), "reuse the cached token")
			} else {
				require.Zero(t, exchanges.Load())
			}
		})
	}
}

func TestIncompleteOAuth2Config(t *testing.T) {
	for _, missing := range []string{"id", "secret", "url", "scopes"} {
		t.Run(missing, func(t *testing.T) {
			config := DefaultConfig()
			config.Oauth2 = OAuth2Config{ClientID: "id", ClientSecret: "secret", TokenURL: "http://issuer/token", Scopes: []string{"read"}}
			switch missing {
			case "id":
				config.Oauth2.ClientID = ""
			case "secret":
				config.Oauth2.ClientSecret = ""
			case "url":
				config.Oauth2.TokenURL = ""
			case "scopes":
				config.Oauth2.Scopes = nil
			}
			require.ErrorContains(t, validateConfig(&config), "incomplete OAuth2 configuration")
		})
	}
}

func TestOAuth2RefreshAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "refresh", true: "token failure"}[fail], func(t *testing.T) {
			var exchanges, requests atomic.Int32
			tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := exchanges.Add(1)
				if fail {
					w.WriteHeader(401)
					return
				}
				expiry := 3600
				if n == 1 {
					expiry = 1
				} // Forces the next call past OAuth2's refresh margin.
				w.Header().Set("Content-Type", "application/json")
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("token-%d", n), "token_type": "Bearer", "expires_in": expiry}))
			}))
			defer tokens.Close()
			smd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				assert.Equal(t, fmt.Sprintf("Bearer token-%d", n), r.Header.Get("Authorization"))
				w.WriteHeader(204)
			}))
			defer smd.Close()
			config := DefaultConfig()
			config.Oauth2 = OAuth2Config{ClientID: "id", ClientSecret: "secret", TokenURL: tokens.URL, Scopes: []string{"read"}}
			client := newSMDHTTPClient(context.Background(), config)
			for range 2 {
				resp, err := client.Get(smd.URL)
				if fail {
					require.Error(t, err)
					require.Nil(t, resp)
				} else {
					require.NoError(t, err)
					require.NoError(t, resp.Body.Close())
				}
			}
			require.EqualValues(t, 2, exchanges.Load())
			if fail {
				require.Zero(t, requests.Load(), "auth errors must not send unauthenticated SMD requests")
			}
		})
	}
}

func TestAuthenticationStartupFailsBeforeWorkers(t *testing.T) {
	for _, provider := range []string{"invalid", "tokensmith"} {
		t.Run(provider, func(t *testing.T) {
			config := DefaultConfig()
			config.AuthProvider = provider
			config.Log.ConsoleLogsBasePath = filepath.Join(t.TempDir(), "must-not-exist")
			require.Error(t, runService(config))
			_, err := os.Stat(config.Log.ConsoleLogsBasePath)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
