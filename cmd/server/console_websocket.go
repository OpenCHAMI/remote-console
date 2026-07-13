// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package main

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/OpenCHAMI/remote-console/internal/console"
)

var consoleWebSocketHandler *console.WebSocketHandler

func initializeConsoleWebSocketHandler(config remoteConsoleConfig) {
	consoleWebSocketHandler = console.NewWebSocketHandler(filepath.Join(config.Conman.LogsPath, "conman"))
}

func isConsoleWebSocketRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

func consoleWebSocketMiddleware(webSocketHandler http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isConsoleWebSocketRequest(r) && isConsoleWebSocketPath(r.URL.Path) {
				webSocketHandler.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isConsoleWebSocketPath(path string) bool {
	uid := strings.TrimPrefix(path, "/remote-console/consoles/")
	return uid != path && uid != "" && !strings.Contains(uid, "/")
}

func serveConsoleWebSocket(w http.ResponseWriter, r *http.Request) {
	if consoleWebSocketHandler == nil {
		http.Error(w, "Console WebSocket handler is not initialized", http.StatusServiceUnavailable)
		return
	}
	consoleWebSocketHandler.ServeHTTP(w, r)
}
