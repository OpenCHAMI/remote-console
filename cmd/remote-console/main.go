// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// Copyright © 2021-2023 Hewlett Packard Enterprise Development LP
//
// SPDX-License-Identifier: MIT
// This file handles command line entry and enables the http service.

package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// initLogger sets up logging with slog
func initLogger() {
	// Determine log level from environment
	level := slog.LevelInfo
	if logLevel := os.Getenv("LOG_LEVEL"); logLevel != "" {
		if err := level.UnmarshalText([]byte(logLevel)); err != nil {
			// If parsing fails, keep default INFO level
			slog.Warn("Invalid LOG_LEVEL, using INFO", "value", logLevel, "error", err)
		}
	}

	// Determine format from environment (json or text)
	format := strings.ToLower(os.Getenv("LOG_FORMAT"))
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		// Default to text format for development
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}

	slog.SetDefault(slog.New(handler))
	slog.Info("Logger initialized", "level", level.String(), "format", format)
}

func main() {
	initLogger()

	config := DefaultConfig()
	cli := command(&config)

	if err := cli.Run(context.Background(), os.Args); err != nil {
		slog.Error("Service failed", "error", err)
		os.Exit(1)
	}
}
