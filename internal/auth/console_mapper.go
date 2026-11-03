// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// SPDX-License-Identifier: MIT

package auth

import (
	"net/http"
	"strings"

	"github.com/openchami/tokensmith/pkg/authz"
)

// consoleRouteMapper preserves TokenSmith's path normalization while treating
// interactive console access as write access, even though its handshake uses GET.
type consoleRouteMapper struct {
	authz.PathMethodMapper
}

func (m consoleRouteMapper) Map(r *http.Request, principal authz.Principal) (authz.RouteDecision, error) {
	decision, err := m.PathMethodMapper.Map(r, principal)
	if err != nil || !decision.Mapped {
		return decision, err
	}
	if decision.Action == "read" && strings.HasPrefix(decision.Object, "/remote-console/consoles/") && r.URL.Query().Get("mode") != "tail" {
		// Match the handler's mode selection. An empty mode means interactive,
		// and the first query value wins. Unknown modes receive no reader privilege.
		decision.Action = "write"
	}
	return decision, nil
}
