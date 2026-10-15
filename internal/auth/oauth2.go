// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package auth

import (
	"context"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// OAuth2Config configures outbound authentication using client credentials.
type OAuth2Config struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// NewOAuth2TokenSource obtains and refreshes tokens using client credentials.
func NewOAuth2TokenSource(ctx context.Context, config OAuth2Config) oauth2.TokenSource {
	clientConfig := &clientcredentials.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		TokenURL:     config.TokenURL,
		Scopes:       config.Scopes,
		AuthStyle:    oauth2.AuthStyleInHeader,
	}
	return clientConfig.TokenSource(ctx)
}
