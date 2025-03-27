//
//  MIT License
//
//  (C) Copyright 2021-2023 Hewlett Packard Enterprise Development LP
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

// This file contains functionality for defining and handling http routing.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
)

// httpRoute specifies the method, uri regex and the handler.
type httpRoute struct {
	httpMethod string
	uriRegex   *regexp.Regexp
	handler    http.HandlerFunc
}

// Route definitions.
var httpRoutes = []httpRoute{

	// Note: the API below is not published and only intended for internal use
	// by the Console Operator and Node services.  A CLI will not be needed for
	// this API.

	createRoute("GET", "/liveness", doLiveness),
	createRoute("GET", "/readiness", doReadiness),
	createRoute("GET", "/v1/liveness", doLiveness),
	createRoute("GET", "/v1/readiness", doReadiness),

	// Add nodes(s) to the console inventory.
	// Expects []NodeConsoleInfo
	// Returns rhe number of nodes created in BaseResponse.
	createRoute("PUT", "/v1/inventory", updateNodes),

	// Delete any node(s) in the list from the console inventory.
	// Expects 1+ NodeConsoleInfo
	// Returns the number of nodes created in BaseResponse.
	createRoute("DELETE", "/v1/inventory", deleteNodes),

	// Remove any console pod ownership if the heartbeat is
	// older than the duration (specified in the URI).
	// The duration unit is in minute(s).
	// Returns the number of nodes updated in BaseResponse.
	createRoute("DELETE", "/v1/consolepod/([0-9]+)/clear", clearStaleNodes),

	// Acquire node(s) for a console pod (specified in the URI).
	// Expects the requested node counts specified in consolePodAcquireNodes.ReqData
	// Returns the list of nodes acquired as []NodeConsoleInfo
	createRoute("POST", "/v1/consolepod/([0-9a-z-]+)/acquire", consolePodAcquireNodes),

	// Update the heartbeat for the console pod (specified in the URI).
	// Expects 1+ NodeConsoleInfo
	// Returns list of nodes no longer assigned to this pod as
	// []NodeConsoleInfo
	createRoute("POST", "/v1/consolepod/([0-9a-z-]+)/heartbeat", consolePodHeartbeat),

	// Release the console pod (specified in the URI) from all nodes in the given list.
	// Expects 1+ NodeConsoleInfo
	// Returns the count of nodes that were updated in BaseResponse.
	createRoute("POST", "/v1/consolepod/([0-9a-z-]+)/release", consolePodRelease),

	// Find the console pod for the node (specified in the URI).
	// Returns a NodeConsoleInfo with the console pod name or
	// empty pod name if not assigned.
	createRoute("GET", "/v1/consolepod/([0-9a-z]+)", findConsolePodForNode),

	// Find the console pod for the node (specified in the URI).
	// Returns a NodeConsoleInfo with the console pod name or
	// empty pod name if not assigned.
	createRoute("GET", "/v1/activepods", getNumActiveNodePods),
}

func createRoute(httpMethod, uriPattern string, handler http.HandlerFunc) httpRoute {
	return httpRoute{httpMethod, regexp.MustCompile("^" + uriPattern + "$"), handler}
}

// Http request handler
func RequestRouter(w http.ResponseWriter, r *http.Request) {
	var allow []string
	for _, httpRoute := range httpRoutes {
		matches := httpRoute.uriRegex.FindStringSubmatch(r.URL.Path)
		if len(matches) > 0 {
			if r.Method != httpRoute.httpMethod {
				allow = append(allow, httpRoute.httpMethod)
				continue
			}
			ctx := context.WithValue(r.Context(), ctxKey{}, matches[1:])
			httpRoute.handler(w, r.WithContext(ctx))
			return
		}
	}
	if len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.NotFound(w, r)
}

type ctxKey struct{}

func getField(r *http.Request, index int) string {
	fields := r.Context().Value(ctxKey{}).([]string)
	return fields[index]
}

// Base response.
type BaseResponse struct {
	Msg string `json:"message"` // Message
}

// SendResponseJSON sends data marshalled as a JSON body and sets the HTTP
// status code to sc.
func SendResponseJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if data == nil {
		// We may have nothing to return other than a status code.
		return
	}
	err := json.NewEncoder(w).Encode(data)
	if err != nil {
		log.Printf("Error: encoding/sending JSON response: %s\n", err)
		return
	}
}

// NotImplemented is used as a placeholder API entry point.
func NotImplemented(w http.ResponseWriter, r *http.Request) {
	var body = BaseResponse{
		Msg: fmt.Sprintf("%s API Unavailable/Not Implemented", r.URL.Path),
	}

	SendResponseJSON(w, http.StatusNotImplemented, body)
}
