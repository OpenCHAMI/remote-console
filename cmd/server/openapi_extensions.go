// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT
//
// This file contains the user-editable OpenAPI extension hook.
//
// ✅ This file is safe to edit: it will NOT be overwritten by regeneration.
//
// Add any routes that are not Fabrica-generated (legacy APIs, custom endpoints,
// WireGuard, cloud-init, etc.) to registerCustomOpenAPIPaths so they appear in
// the served OpenAPI spec and Swagger UI at /openapi.json and /docs.
//
// Example:
//
//	func registerCustomOpenAPIPaths(spec *openapi3.T) {
//	    metaDataOp := openapi3.NewOperation()
//	    metaDataOp.OperationID = "getMetaData"
//	    metaDataOp.Summary = "Cloud-init meta-data endpoint"
//	    metaDataOp.Tags = []string{"cloud-init"}
//	    metaDataOp.Responses = openapi3.NewResponses()
//	    metaDataOp.Responses.Set("200", &openapi3.ResponseRef{
//	        Value: openapi3.NewResponse().WithDescription("YAML metadata for the requesting node"),
//	    })
//	    spec.Paths.Set("/meta-data", &openapi3.PathItem{Get: metaDataOp})
//	}
package main

import "github.com/getkin/kin-openapi/openapi3"

// registerCustomOpenAPIPaths is called by GenerateOpenAPISpec after all
// Fabrica-generated resource paths have been registered.
func registerCustomOpenAPIPaths(spec *openapi3.T) {
	consoleWebSocketOp := openapi3.NewOperation()
	consoleWebSocketOp.OperationID = "connectConsoleWebSocket"
	consoleWebSocketOp.Summary = "Connect to a console WebSocket"
	consoleWebSocketOp.Description = "Upgrades the request to a WebSocket for an interactive console session or console log tail. This is not a normal REST get-one Console operation; clients must send WebSocket Upgrade headers."
	consoleWebSocketOp.Tags = []string{"Console"}
	consoleWebSocketOp.Parameters = openapi3.Parameters{
		{
			Value: &openapi3.Parameter{
				Name:        "uid",
				In:          "path",
				Required:    true,
				Description: "Console/node identifier.",
				Schema:      &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
			},
		},
		{
			Value: &openapi3.Parameter{
				Name:        "mode",
				In:          "query",
				Required:    false,
				Description: "Console session mode. Defaults to interactive.",
				Schema: &openapi3.SchemaRef{Value: openapi3.NewStringSchema().
					WithEnum("interactive", "tail")},
			},
		},
		{
			Value: &openapi3.Parameter{
				Name:        "follow",
				In:          "query",
				Required:    false,
				Description: "Tail mode only. Continue streaming appended console log lines.",
				Schema:      &openapi3.SchemaRef{Value: openapi3.NewBoolSchema()},
			},
		},
		{
			Value: &openapi3.Parameter{
				Name:        "lines",
				In:          "query",
				Required:    false,
				Description: "Tail mode only. Number of historical console log lines to return before following.",
				Schema:      &openapi3.SchemaRef{Value: openapi3.NewIntegerSchema()},
			},
		},
	}
	consoleWebSocketOp.Responses = openapi3.NewResponses()
	consoleWebSocketOp.Responses.Set("101", &openapi3.ResponseRef{
		Value: openapi3.NewResponse().WithDescription("Switching Protocols; WebSocket session established"),
	})
	consoleWebSocketOp.Responses.Set("400", &openapi3.ResponseRef{
		Value: openapi3.NewResponse().WithDescription("Invalid console path or query parameter"),
	})
	consoleWebSocketOp.Responses.Set("404", &openapi3.ResponseRef{
		Value: openapi3.NewResponse().WithDescription("Console node not found"),
	})
	consoleWebSocketOp.Responses.Set("409", &openapi3.ResponseRef{
		Value: openapi3.NewResponse().WithDescription("Interactive console is already in use"),
	})
	consoleWebSocketOp.Responses.Set("503", &openapi3.ResponseRef{
		Value: openapi3.NewResponse().WithDescription("Console WebSocket handler is not initialized"),
	})

	spec.Paths.Set("/remote-console/consoles/{uid}", &openapi3.PathItem{Get: consoleWebSocketOp})
}
