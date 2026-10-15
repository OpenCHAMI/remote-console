// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"net/http"

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
