// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// Copyright © 2019 - 2025 Hewlett Packard Enterprise Development LP
//
// SPDX-License-Identifier: MIT

package console

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

func sendResponseJSON(w http.ResponseWriter, sc int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(sc)
	err := json.NewEncoder(w).Encode(data)
	if err != nil {
		slog.Error("encoding/sending JSON response", "error", err)
		return
	}
}
