// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/OpenCHAMI/jwtauth/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
	openchami_authenticator "github.com/openchami/chi-middleware/auth"
)

// JWKSConfig configures JWT authentication using a JWKS endpoint.
type JWKSConfig struct {
	JWKSURL       string
	RetryInterval time.Duration
}

type jwksAuth struct {
	tokenAuth *jwtauth.JWTAuth
}

func NewJWKSAuth(ctx context.Context, config JWKSConfig) (Auth, error) {
	var tokenAuth *jwtauth.JWTAuth
	if config.JWKSURL != "" {
		var err error
		slog.Info("Fetching public key from server...")
		for i := 0; i <= 5; i++ {
			tokenAuth, err = fetchPublicKeyFromURL(ctx, config.JWKSURL)
			if err != nil {
				slog.Warn("Failed to initialize JWT authentication", "error", err)
				if i == 5 {
					break
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(config.RetryInterval):
				}
				continue
			}
			slog.Info("Initialized the auth token successfully.")
			break
		}
		if err != nil {
			return nil, fmt.Errorf("initialize JWKS: %w", err)
		}
	}

	if tokenAuth == nil {
		slog.Warn("No JWKS URL provided - JWT authentication is disabled")
	}
	return &jwksAuth{tokenAuth: tokenAuth}, nil
}

func (a *jwksAuth) Wrap(next http.Handler) http.Handler {
	if a.tokenAuth == nil {
		return next
	}
	authenticate := openchami_authenticator.AuthenticatorWithRequiredClaims(
		a.tokenAuth,
		[]string{"sub", "iss", "aud"},
	)
	return jwtauth.Verifier(a.tokenAuth)(authenticate(next))
}

func fetchPublicKeyFromURL(ctx context.Context, url string) (*jwtauth.JWTAuth, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			slog.Debug("Failed to close JWKS response", "error", err)
		}
	}()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS status code: %d", response.StatusCode)
	}
	set, err := jwk.ParseReader(response.Body)
	if err != nil {
		msg := "%w"

		// if the error tree contains an EOF, it means that the response was empty,
		// so add a more descriptive message to the error tree
		if errors.Is(err, io.EOF) {
			msg = "received empty response for key: %w"
		}

		return nil, fmt.Errorf(msg, err)
	}
	if set.Len() == 0 {
		return nil, fmt.Errorf("JWKS contains no verification keys")
	}
	jwks, err := json.Marshal(set)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JWKS: %v", err)
	}
	tokenAuth, err := jwtauth.NewKeySet(jwks)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize JWKS: %v", err)
	}

	return tokenAuth, nil
}
