// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/openchami/tokensmith/pkg/authn"
	"github.com/openchami/tokensmith/pkg/authz"
	"github.com/openchami/tokensmith/pkg/authz/engine"
	"github.com/openchami/tokensmith/pkg/authz/presets"
	"github.com/openchami/tokensmith/pkg/keys"
)

// TokensmithConfig configures inbound authentication and authorization only.
type TokensmithConfig struct {
	JwksURL           string `desc:"JWKS endpoint for inbound TokenSmith validation"`
	Issuer            string `desc:"Expected TokenSmith issuer"`
	Audience          string `desc:"Expected TokenSmith audience"`
	AuthzMode         string `desc:"Authorization mode: enforce, shadow, or off (authentication remains required)"`
	AuthzModelPath    string `desc:"Casbin model file; defaults to TokenSmith RBAC path model"`
	AuthzPolicyPath   string `desc:"Casbin policy file or directory (required in enforce/shadow)"`
	AuthzGroupingPath string `desc:"Optional Casbin role grouping file or directory"`
}

type tokenSmithAuth struct {
	authenticate func(http.Handler) http.Handler
	authorize    *authz.Middleware
}

func NewTokenSmithAuth(ctx context.Context, config TokensmithConfig) (Auth, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	mode := authz.Mode(config.AuthzMode)

	client := &http.Client{Timeout: 10 * time.Second}
	authenticate, err := authn.Middleware(authn.Options{
		Issuers:    []string{config.Issuer},
		Audiences:  []string{config.Audience},
		JWKSURLs:   []string{config.JwksURL},
		HTTPClient: client,
		Mapper:     tokenSmithPrincipal,
	})
	if err != nil {
		return nil, fmt.Errorf("configure TokenSmith authentication: %w", err)
	}

	var authorizer *authz.Authorizer
	if mode != authz.ModeOff {
		builder := engine.NewBuilder().
			WithModelPreset(presets.RBACKeyMatch2REST()).
			WithPolicyPath(config.AuthzPolicyPath).
			WithGroupingPath(config.AuthzGroupingPath).
			WithAuthorizerOptions(authz.WithDecisionCacheFromEnv())
		if config.AuthzModelPath != "" {
			builder.WithModelPath(config.AuthzModelPath)
		}
		authorizer, err = builder.Build()
		if err != nil {
			return nil, fmt.Errorf("configure TokenSmith authorization: %w", err)
		}
	}

	// TokenSmith fetches JWKS lazily; check them before starting the service as well.
	if err := checkTokenSmithJWKS(ctx, client, config.JwksURL); err != nil {
		return nil, fmt.Errorf("initialize TokenSmith JWKS: %w", err)
	}

	authorize := authz.NewMiddleware(authorizer,
		consoleRouteMapper{authz.PathMethodMapper{MethodToAction: authz.MethodToActionREST()}},
		authz.WithMode(mode),
		authz.WithRequireAuthn(true),
		authz.WithAllowUnmapped(false),
		authz.WithRequestIDFromContext(middleware.GetReqID),
	)
	authorize.OnDecision = func(ctx context.Context, decision authz.DecisionRecord) {
		level := slog.LevelInfo
		if decision.Decision == authz.DecisionAllow {
			level = slog.LevelDebug
		}
		slog.LogAttrs(ctx, level, "TokenSmith authorization", slog.Any("decision", decision))
	}
	slog.Info("TokenSmith authentication configured", "authz_mode", config.AuthzMode,
		"policy_source", config.AuthzPolicyPath, "policy_version", authorizer.PolicyVersion())

	return &tokenSmithAuth{
		authenticate: authenticate,
		authorize:    authorize,
	}, nil
}

func (a *tokenSmithAuth) Wrap(next http.Handler) http.Handler {
	return a.authenticate(a.authorize.Handler(next))
}

func tokenSmithPrincipal(_ context.Context, _ *jwt.Token, claims jwt.MapClaims) (authz.Principal, error) {
	subject, err := claims.GetSubject()
	if err != nil || subject == "" {
		return authz.Principal{}, fmt.Errorf("missing subject")
	}
	principal := authz.Principal{ID: subject}
	// TokenSmith exposes its granted roles in the scope array.
	if scopes, ok := claims["scope"].([]any); ok {
		for _, scope := range scopes {
			if role, ok := scope.(string); ok {
				principal.Roles = append(principal.Roles, role)
			}
		}
	}
	return principal, nil
}

func checkTokenSmithJWKS(ctx context.Context, client *http.Client, url string) error {
	set, err := jwk.Fetch(ctx, url, jwk.WithHTTPClient(client))
	if err != nil {
		return err
	}
	for i := range set.Len() {
		key, _ := set.Key(i)
		kid := key.KeyID()
		if !keys.IsRFC7638Thumbprint(kid) {
			continue
		}
		if algorithm := key.Algorithm(); algorithm != nil && keys.ValidateAlgorithm(algorithm.String()) != nil {
			continue
		}
		var publicKey any
		err := key.Raw(&publicKey)
		if err != nil {
			continue
		}
		if use := key.KeyUsage(); use != "" && use != "sig" {
			continue
		}
		algorithm := ""
		if key.Algorithm() != nil {
			algorithm = key.Algorithm().String()
		}
		switch publicKey.(type) {
		case *rsa.PublicKey:
			if algorithm == "" || strings.HasPrefix(algorithm, "RS") || strings.HasPrefix(algorithm, "PS") {
				return nil
			}
		case *ecdsa.PublicKey:
			if algorithm == "" || strings.HasPrefix(algorithm, "ES") {
				return nil
			}
		}
	}
	return fmt.Errorf("JWKS contains no usable TokenSmith verification keys")
}

// Validate checks inbound settings without making network requests.
func (config TokensmithConfig) Validate() error {
	mode := authz.Mode(config.AuthzMode)
	switch mode {
	case authz.ModeOff, authz.ModeShadow, authz.ModeEnforce:
	default:
		return fmt.Errorf("invalid tokensmith-authz-mode %q: use off, shadow, or enforce", config.AuthzMode)
	}
	if config.Issuer == "" || config.Audience == "" || config.JwksURL == "" {
		return fmt.Errorf("TokenSmith requires tokensmith-issuer, tokensmith-audience, and tokensmith-jwks-url")
	}
	if mode != authz.ModeOff && config.AuthzPolicyPath == "" {
		return fmt.Errorf("TokenSmith requires tokensmith-authz-policy-path in %s mode", mode)
	}

	return nil
}
