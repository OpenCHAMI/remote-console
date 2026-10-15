// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openchami/remote-console/internal/auth"
	"golang.org/x/oauth2"
)

func newSMDHTTPClient(ctx context.Context, config remoteConsoleConfig) (*http.Client, error) {
	var source oauth2.TokenSource
	switch config.SmdAuthProvider {
	case "oauth2":
		if config.SmdTokensmith.URL != "" || config.SmdTokensmith.CAFile != "" {
			slog.Warn("Ignoring outbound TokenSmith settings because smd-auth-provider is oauth2")
		}
		if config.Oauth2.TokenURL != "" {
			source = auth.NewOAuth2TokenSource(ctx, auth.OAuth2Config{
				ClientID: config.Oauth2.ClientID, ClientSecret: config.Oauth2.ClientSecret,
				TokenURL: config.Oauth2.TokenURL, Scopes: config.Oauth2.Scopes,
			})
		}
	case "tokensmith":
		if config.Oauth2.ClientID != "" || config.Oauth2.ClientSecret != "" || config.Oauth2.TokenURL != "" || len(config.Oauth2.Scopes) > 0 {
			slog.Warn("Ignoring OAuth2 settings because smd-auth-provider is tokensmith")
		}
		if strings.HasPrefix(config.SmdTokensmith.URL, "http://") {
			slog.Warn("Outbound TokenSmith uses HTTP: bootstrap and refresh tokens will be sent without TLS")
		}
		var err error
		source, err = auth.NewTokenSmithTokenSource(ctx, config.SmdTokensmith)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("invalid smd-auth-provider %q: use oauth2 or tokensmith", config.SmdAuthProvider)
	}
	client := &http.Client{Timeout: smdHTTPTimeout}
	if source != nil {
		client.Transport = &oauth2.Transport{Source: source, Base: http.DefaultTransport}
	}
	return client, nil
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
