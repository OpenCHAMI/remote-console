// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/openchami/tokensmith/pkg/tokenservice"
	"golang.org/x/oauth2"
)

const tokenSmithSessionRecoveryInterval = 30 * time.Second

// TokensmithClientConfig configures outbound service token acquisition.
type TokensmithClientConfig struct {
	URL    string `desc:"TokenSmith base URL for outbound SMD service tokens"`
	CAFile string `desc:"Additional CA certificate file for outbound TokenSmith"`
}

// NewTokenSmithTokenSource obtains and refreshes tokens for outbound SMD requests.
func NewTokenSmithTokenSource(ctx context.Context, config TokensmithClientConfig) (oauth2.TokenSource, error) {
	endpoint, err := url.Parse(config.URL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, fmt.Errorf("outbound TokenSmith requires a valid smd-tokensmith-url")
	}
	certPath := strings.TrimSpace(os.Getenv(tokenservice.ServiceIdentityCertEnvVar))
	keyPath := strings.TrimSpace(os.Getenv(tokenservice.ServiceIdentityKeyEnvVar))
	bootstrapToken := strings.TrimSpace(os.Getenv(tokenservice.BootstrapTokenEnvVar))
	if (certPath == "") != (keyPath == "") {
		return nil, fmt.Errorf("TOKENSMITH_SERVICE_IDENTITY_CERT and TOKENSMITH_SERVICE_IDENTITY_KEY must be set together")
	}
	if certPath == "" && bootstrapToken == "" {
		return nil, fmt.Errorf("outbound TokenSmith requires TOKENSMITH_BOOTSTRAP_TOKEN or service identity certificates")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if config.CAFile != "" {
		certs, err := os.ReadFile(config.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read TokenSmith CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system certificates: %w", err)
		}
		if !pool.AppendCertsFromPEM(certs) {
			return nil, fmt.Errorf("TokenSmith CA file contains no certificates")
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	if certPath != "" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("TokenSmith service identity requires an HTTPS URL")
	}
	source := &tokenSmithTokenSource{
		ctx: ctx, url: config.URL, certPath: certPath, keyPath: keyPath,
		httpClient: &http.Client{Transport: transport, Timeout: 10 * time.Second},
	}
	if err := source.initialize(); err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("initialize outbound TokenSmith: %w", err)
	}
	// ReuseTokenSource serializes refreshes so concurrent SMD requests do not
	// reuse a refresh token that TokenSmith has already rotated.
	return oauth2.ReuseTokenSource(nil, source), nil
}

type tokenSmithTokenSource struct {
	// TokenSource has no request context. Exchanges use the service context
	// and the HTTP client's timeout, independently of an SMD request's deadline.
	ctx        context.Context
	url        string
	certPath   string
	keyPath    string
	httpClient *http.Client
	client     *tokenservice.ServiceClient
	// nil means no pending recovery.
	nextRenewal *time.Time
}

func (s *tokenSmithTokenSource) initialize() error {
	options := []tokenservice.ServiceClientOption{
		tokenservice.WithTargetService("smd"),
		tokenservice.WithHTTPClient(s.httpClient),
		// We are handling the retry logic ourselves.
		tokenservice.WithBootstrapMaxAttempts(1),
	}
	if s.certPath != "" {
		// Validate on every session creation so missing identity files do not
		// make the library fall back to a bootstrap token.
		if _, err := tls.LoadX509KeyPair(s.certPath, s.keyPath); err != nil {
			return fmt.Errorf("load TokenSmith service identity: %w", err)
		}
		options = append(options, tokenservice.WithServiceIdentityCertKey(s.certPath, s.keyPath))
	}
	// Remote-console uses its own service identity for SMD, independent of incoming callers.
	client := tokenservice.NewServiceClientWithOptions(
		s.url, "remote-console", "remote-console", "", "", options...,
	)
	if err := client.Initialize(s.ctx); err != nil {
		return err
	}
	s.client = client
	return nil
}

func (s *tokenSmithTokenSource) Token() (*oauth2.Token, error) {
	if s.nextRenewal == nil {
		if err := s.client.RefreshTokenIfNeeded(s.ctx); err != nil {
			if s.certPath == "" || !tokenSmithSessionNeedsRenewal(err) {
				return nil, fmt.Errorf("refresh outbound TokenSmith token: %w", err)
			}
			// The server may have rotated the token. Never replay it, even if
			// creating the replacement session fails on this call.
			s.nextRenewal = new(time.Time)
		}
	}
	if s.nextRenewal != nil {
		if err := s.ctx.Err(); err != nil {
			return nil, fmt.Errorf("refresh outbound TokenSmith token: %w", err)
		}
		if time.Now().Before(*s.nextRenewal) {
			return nil, fmt.Errorf("outbound TokenSmith session recovery retry delayed")
		}
		if err := s.initialize(); err != nil {
			*s.nextRenewal = time.Now().Add(tokenSmithSessionRecoveryInterval)
			return nil, fmt.Errorf("renew outbound TokenSmith session: %w", err)
		}
		s.nextRenewal = nil
	}
	token := s.client.GetServiceToken()
	if token == nil || token.Token == "" || !token.ExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("TokenSmith returned no usable service token")
	}
	return &oauth2.Token{
		AccessToken: token.Token,
		TokenType:   "Bearer",
		Expiry:      token.ExpiresAt,
	}, nil
}

func tokenSmithSessionNeedsRenewal(err error) bool {
	// The TokenSmith client wraps network and truncated-response errors.
	// HTTP rejections may mean revocation and must not renew the session.
	var networkErr net.Error
	return errors.Is(err, tokenservice.ErrRefreshTokenExpired) ||
		errors.As(err, &networkErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
