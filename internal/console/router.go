//
//  MIT License
//
//  (C) Copyright 2023 Hewlett Packard Enterprise Development LP
//
//  Permission is hereby granted, free of charge, to any person obtaining a
//  copy of this software and associated documentation files (the "Software"),
//  to deal in the Software without restriction, including without limitation
//  the rights to use, copy, modify, merge, publish, distribute, sublicense,
//  and/or sell copies of the Software, and to permit persons to whom the
//  Software is furnished to do so, subject to the following conditions:
//
//  The above copyright notice and this permission notice shall be included
//  in all copies or substantial portions of the Software.
//
//  THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
//  IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
//  FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL
//  THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR
//  OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE,
//  ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR
//  OTHER DEALINGS IN THE SOFTWARE.
//

package console

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

type endpointError struct {
	statusCode int
	message    string
}

func (e *endpointError) Error() string {
	return e.message
}

// Helper to create API errors
func newEndpointError(status int, message string) *endpointError {
	return &endpointError{statusCode: status, message: message}
}

// Define a handler type that returns an error
type handlerE func(ctx context.Context, w http.ResponseWriter, r *http.Request) error

func sendResponseJSON(w http.ResponseWriter, sc int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(sc)

	if data != nil {
		err := json.NewEncoder(w).Encode(data)
		if err != nil {
			log.Printf("Error: encoding/sending JSON response: %s\n", err)
			return
		}
	}
}

// Middleware that wraps your error-returning handlers
func errorHandler(h handlerE) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := h(r.Context(), w, r)
		if err != nil {
			// Handle different error types
			switch e := err.(type) {
			case *endpointError:
				SendResponseJSON(w, e.statusCode, map[string]string{
					"error": e.message,
				})
			default:
				SendResponseJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Internal server error",
				})
				log.Printf("Internal error: %v", err)
			}
		}
	}
}

var RequestRouter = chi.NewRouter()

// WebSocket upgrader
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// Allow connections from any origin for now
		// In production, you should check the origin
		return true
	},
}

func SetupRoutes() {
	// k8s routes
	RequestRouter.Get("/remote-console/liveness", doLiveness)
	RequestRouter.Get("/remote-console/readiness", doReadiness)
	RequestRouter.Get("/remote-console/health", doHealth)

	// WebSocket console access - tail mode
	RequestRouter.Get("/remote-console/console/{nodeID}/tail", errorHandler(doTailConsole))
	// WebSocket console access
	RequestRouter.Get("/remote-console/console/{nodeID}", errorHandler(doInteractiveConsole))

	// debug only routes
	// router.Get("/remote-console/info", dbs.doInfo)
	// router.Delete("/remote-console/clearData", dbs.doClearData)
	// router.Post("/remote-console/suspend", dbs.doSuspend)
	// router.Post("/remote-console/resume", dbs.doResume)
}
