//
//  MIT License
//
//  (C) Copyright 2021-2022, 2024 Hewlett Packard Enterprise Development LP
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

// This file contains the health endpoint functions

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// HealthResponse - used to report service health stats
type HealthResponse struct {
	NumMtnConnected string `json:"num_mtn"`
	NumRvrConnected string `json:"num_rvr"`
	TargetNumMtn    string `json:"target_mtn"`
	TargetNumRvr    string `json:"target_rvr"`
	LastHeartbeat   string `json:"last_heartbeat"`
}

// ErrResponse - Simple struct to return error information
type ErrResponse struct {
	E      int    `json:"e"` // Error code
	ErrMsg string `json:"err_msg"`
}

// Information on the status
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

	// Rough first draft we can provide information on:
	// 1) number of nodes being monitored
	// 2) last timestamp of hardware update
	// 3) number of console-node replicas requested
	// 4) active connection to console-data service

	var stats HealthResponse

	// NOTE: Paradise nodes are counted as Mountain nodes due to needing to run through an expect script
	stats.NumMtnConnected = fmt.Sprintf("%d", len(currentMtnNodes)+len(currentPdsNodes))
	stats.NumRvrConnected = fmt.Sprintf("%d", len(currentRvrNodes))
	stats.TargetNumMtn = fmt.Sprintf("%d", targetMtnNodes)
	stats.TargetNumRvr = fmt.Sprintf("%d", targetRvrNodes)
	stats.LastHeartbeat = lastHeartbeatTime

	// write the output
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
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
