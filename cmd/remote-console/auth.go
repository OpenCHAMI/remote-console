// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/openchami/remote-console/internal/auth"
	"golang.org/x/oauth2"
)

func newSMDHTTPClient(ctx context.Context, config remoteConsoleConfig) *http.Client {
	client := &http.Client{Timeout: smdHTTPTimeout}
	if config.Oauth2.TokenURL != "" {
		source := auth.NewOAuth2TokenSource(ctx, auth.OAuth2Config{
			ClientID: config.Oauth2.ClientID, ClientSecret: config.Oauth2.ClientSecret,
			TokenURL: config.Oauth2.TokenURL, Scopes: config.Oauth2.Scopes,
		})
		client.Transport = &oauth2.Transport{Source: source, Base: http.DefaultTransport}
	}
	return client
}

func newInboundAuth(ctx context.Context, config remoteConsoleConfig) (auth.Auth, error) {
	switch config.AuthProvider {
	case "jwks":
		return auth.NewJWKSAuth(ctx, auth.JWKSConfig{JWKSURL: config.JwksURL, RetryInterval: time.Duration(config.JwksFetchInterval) * time.Second})
	case "tokensmith":
		return auth.NewTokenSmithAuth(ctx, config.Tokensmith)
	default:
		return nil, fmt.Errorf("invalid auth-provider %q: use jwks or tokensmith", config.AuthProvider)
	}
}
