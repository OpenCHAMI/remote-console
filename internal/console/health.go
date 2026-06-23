// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// Copyright © 2021 - 2024 Hewlett Packard Enterprise Development LP
//
// SPDX-License-Identifier: MIT

// This file contains the health endpoint functions

package console

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
)

// HealthResponse - used to report service health stats
type HealthResponse struct {
	NumberConsoles     string `json:"consoles"`
	LastHardwareUpdate string `json:"hardwareupdate"`
}

type errorResponse struct {
	E      int    `json:"e"`
	ErrMsg string `json:"err_msg"`
}

func sendJSONError(w http.ResponseWriter, ecode int, message string) {
	httpCode := ecode
	if ecode >= 200 && ecode <= 299 {
		ecode = 0
	}
	data := errorResponse{
		E:      ecode,
		ErrMsg: message,
	}
	sendResponseJSON(w, httpCode, data)
}

// Debugging information query
func doHealth(w http.ResponseWriter, r *http.Request) {
	// NOTE: this is provided as a quick check of the internal status for
	//  administrators to aid in determining the health of this service.

	// only allow 'GET' calls
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		sendJSONError(w, http.StatusMethodNotAllowed,
			fmt.Sprintf("(%s) Not Allowed", r.Method))
		return
	}

	// get the current health status
	stats := getCurrentHealth()

	// log the query
	slog.Debug("Health check", "consoles", stats.NumberConsoles, "lastUpdate", stats.LastHardwareUpdate)

	// write the output
	sendResponseJSON(w, http.StatusOK, stats)
}

// Fill out the current status of a HealthResponse object
func getCurrentHealth() HealthResponse {
	var stats HealthResponse
	stats.LastHardwareUpdate = nodes.GetHardwareUpdateTime()
	stats.NumberConsoles = fmt.Sprintf("%d", len(nodes.CurrentNodes()))
	return stats
}

// Basic liveness probe
func doLiveness(w http.ResponseWriter, r *http.Request) {
	// NOTE: this is coded in accordance with kubernetes best practices
	//  for liveness/readiness checks.  This function should only be
	//  used to indicate the server is still alive and processing requests.

	// only allow 'GET' calls
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		sendJSONError(w, http.StatusMethodNotAllowed,
			fmt.Sprintf("(%s) Not Allowed", r.Method))
		return
	}

	// return simple StatusOK response to indicate server is alive
	w.WriteHeader(http.StatusNoContent)
}

// Basic readiness probe
func doReadiness(w http.ResponseWriter, r *http.Request) {
	// NOTE: this is coded in accordance with kubernetes best practices
	//  for liveness/readiness checks.  This function should only be
	//  used to indicate the server is still alive and processing requests.

	// only allow 'GET' calls
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		sendJSONError(w, http.StatusMethodNotAllowed,
			fmt.Sprintf("(%s) Not Allowed", r.Method))
		return
	}

	// return simple StatusOK response to indicate server is alive
	w.WriteHeader(http.StatusNoContent)
}
