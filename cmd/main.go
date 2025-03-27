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

// This file handles command line entry and enables the http service.

package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// The API service port
var svcHost = "0.0.0.0:80"

// Debug logging (default is off)
var debugLog = DebugLog{enabled: false}

// Get environment var with default.
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func main() {
	// Enable debug logging if requested.
	debugLog.Init()

	log.Printf("Console data service starting")

	// Setup a channel to wait for the os to tell us to stop.
	// NOTE - This must be set up before initializing anything that needs
	//  to be cleaned up.  This will trap any signals and wait to
	//  process them until the channel is read.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)

	// Ensure the database connection and schema are setup.
	log.Printf("Initializing DB conn")
	initDBConn()

	// Wait until we can complete schema initialization.
	log.Printf("Prepare DB")
	const (
		initBackoff time.Duration = 5
		maxBackoff  time.Duration = 60
	)
	backoff := initBackoff
	for {
		if err := prepareDB(); err != nil {
			log.Printf("prepareDB has not completed yet")
			time.Sleep(backoff * time.Second)
		} else {
			log.Printf("prepareDB complete")
			break
		}
		if backoff < maxBackoff {
			backoff += backoff
		}
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	defer DB.Close()

	// spin the server in a separate thread so main can wait on an os
	// signal to cleanly shut down
	httpSrv := http.Server{
		Addr:    svcHost,
		Handler: http.HandlerFunc(RequestRouter),
	}
	go func() {
		// NOTE: do not use log.Fatal as that will immediately exit
		// the program and short-circuit the shutdown logic below
		log.Printf("Info: Server %s\n", httpSrv.ListenAndServe())
	}()
	log.Printf("Info: Console data API listening on: %s\n", svcHost)

	// wait here for a signal from the os that we are shutting down
	sig := <-sigs
	log.Printf("Info: Detected signal to close service: %s", sig)

	// stop the server from taking requests
	// NOTE: this waits for active connections to finish
	log.Printf("Info: Server shutting down")
	httpSrv.Shutdown(context.Background())
}

// DebugLog enables debug logging.
type DebugLog struct {
	enabled bool
}

// Init initializes the debug logger.
func (l *DebugLog) Init() {
	if value, ok := os.LookupEnv("DEBUG"); ok {
		if value != "" && strings.ToUpper(value) == "TRUE" {
			l.enabled = true
			l.Println("Debug logging enabled.")
		}
	}
}

// Println writes out a debug log statement.
func (l *DebugLog) Println(msg string) {
	if l.enabled {
		log.Printf("[DEBUG]: %s\n", msg)
	}
}
