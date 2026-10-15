// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

// Package auth provides inbound middleware and outbound service credentials.
package auth

import "net/http"

// Auth protects handlers using an initialized authentication provider.
type Auth interface {
	Wrap(http.Handler) http.Handler
}
