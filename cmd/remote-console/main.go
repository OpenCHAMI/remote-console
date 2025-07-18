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

	"github.com/OpenCHAMI/remote-console/internal/console"
)

var (
	// The API service port
	svcHost = "0.0.0.0:8080"

	// Debug logging (default is off)
	debugLog = DebugLog{enabled: false}

	// Most recent update from the HSM
	hardwareUpdateTime string = "Unknown"
)

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

	// allow for changes in the SMD URL
	console.HsmURL = getEnv("SMD_URL", "http://cray-smd/")
	console.DebugOnly = getEnv("DEBUG", "false") == "true"
	svcHost = getEnv("SVC_HOST", "0.0.0.0:8080")

	log.Printf("Remote console service starting")
	// Set up the zombie killer
	log.Printf("Starting zombie killer...")
	go console.WatchForZombies()

	// first we set up the goroutine that polls the hsm
	go console.WatchHardware()

	// then we set up the goroutine that controls conman
	console.EnsureDirPresent("/var/log/conman", 666)

	// I am not sure that we need this, so I am leaving it out for
	// now, I think that normal logging will work now that we only
	// have one container
	// respinAggLog()

	// Initialize and start log rotation
	console.LogRotate()

	// spin a thread that watches for changes in console configuration
	log.Printf("Starting hardware watch loop...")
	go console.WatchForNodes()

	// start up the thread that runs conman
	go console.RunConman()

	// start the thread that will make sure that the conman creds are correct
	go console.CredMonitor()

	// Setup a channel to wait for the os to tell us to stop.
	// NOTE - This must be set up before initializing anything that needs
	//  to be cleaned up.  This will trap any signals and wait to
	//  process them until the channel is read.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)

	// signal to cleanly shut down
	go func() {
		console.SetupRoutes()
		// NOTE: do not use log.Fatal as that will immediately exit
		// the program and short-circuit the shutdown logic below
		log.Printf("Info: Server %s\n", http.ListenAndServe(svcHost, console.RequestRouter))
	}()

	// Server run context
	server := &http.Server{Addr: svcHost, Handler: console.RequestRouter}
	serverCtx, serverStopCtx := context.WithCancel(context.Background())

	// Listen for syscall signals for process to interrupt/quit
	go func() {
		sig := <-sigs
		log.Printf("Info: Detected signal to close service: %s", sig)

		// Shutdown signal with grace period of 30 seconds
		shutdownCtx, shutdownCtxCancel := context.WithTimeout(serverCtx, 30*time.Second)

		go func() {
			<-shutdownCtx.Done()
			if shutdownCtx.Err() == context.DeadlineExceeded {
				shutdownCtxCancel()
				log.Fatal("graceful shutdown timed out.. forcing exit.")
			}
		}()

		// Trigger graceful shutdown
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			log.Fatal(err)
		}
		serverStopCtx()
	}()

	// Run the server
	log.Printf("Info: Console API listening on: %s\n", svcHost)
	err := server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}

	// Wait for server context to be stopped
	<-serverCtx.Done()
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
