// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// Copyright © 2023 Hewlett Packard Enterprise Development LP
//
// SPDX-License-Identifier: MIT

package console

import (
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
)

const routePrefix = "/remote-console"

// WebSocket upgrader
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// JWT authentication is handled by middleware before upgrade
		// No need for Origin checking since this is for CLI clients
		return true
	},
}

// WebSocketHandler adapts the legacy console WebSocket implementation for
// servers whose routes are registered elsewhere.
type WebSocketHandler struct {
	consoleLogsPath string
	sessions        *interactiveSessions
}

func NewWebSocketHandler(consoleLogsPath string) *WebSocketHandler {
	return &WebSocketHandler{
		consoleLogsPath: consoleLogsPath,
		sessions:        newInteractiveSessions(),
	}
}

func (h *WebSocketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	doConsole(h.consoleLogsPath, h.sessions, w, r)
}

// doConsole dispatches to either interactive or tail mode based on the mode query parameter
func doConsole(consoleLogsPath string, sessions *interactiveSessions, w http.ResponseWriter, r *http.Request) {
	// Parse mode parameter (defaults to "interactive")
	params := r.URL.Query()
	mode := params.Get("mode")
	if mode == "" {
		mode = "interactive"
	}

	switch mode {
	case "tail":
		doTailConsole(consoleLogsPath, w, r)
	case "interactive":
		doInteractiveConsole(sessions, w, r)
	default:
		http.Error(w, fmt.Sprintf("Invalid mode parameter: %s (must be 'interactive' or 'tail')", mode), http.StatusBadRequest)
	}
}
