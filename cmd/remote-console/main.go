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
	"strconv"

	"github.com/OpenCHAMI/remote-console/internal/console"
	"github.com/OpenCHAMI/remote-console/internal/creds"
	"github.com/OpenCHAMI/remote-console/internal/conman"
	"github.com/OpenCHAMI/remote-console/internal/logs"
	"github.com/OpenCHAMI/remote-console/internal/nodes"
	"github.com/OpenCHAMI/remote-console/internal/utils"
)

var (
	// The API service port
	svcHost = "0.0.0.0:8080"

	// Debug logging (default is off)
	debugLog = DebugLog{enabled: false}


	DebugOnly bool = false

	inShutdown bool = false

	// Pause between each lookup for new node information
	 newNodeLookupSec int = 120

	// Time to wait between checking for credential changes
	monitorIntervalSecs int = 30
)

// Get environment var with default.
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func isShuttingDown() bool {
	return inShutdown
}

// Watch for node updates and signal conman and log rotation as needed
func watchForNodesUpdates() {
	for {
		// look for new nodes once
		if isShuttingDown != nil && isShuttingDown() {
			log.Printf("Info: Exiting node watch loop due to shutdown")
			return
		}
		changed := nodes.CheckForUpdates()

		if changed {
			log.Printf("Info: Node changes detected, signaling conman to restart")
			conman.SignalConmanTERM()

			// also update log rotation configuration
			log.Printf("Info: Node changes detected, updating log rotation configuration")
			logs.UpdateLogRotateConf()

			// make sure we are aggregating any new console log files
			log.Printf("Info: Node changes detected, updating log aggregation configuration")
			logs.AggregateFiles()
		}

		// Wait for the correct polling interval
		time.Sleep(time.Duration(newNodeLookupSec) * time.Second)
	}
}

// Watch for credential updates and signal conman as needed
func watchForCredUpdates() {
	time.Sleep(time.Duration(monitorIntervalSecs) * time.Second)
	for {
		changed :=  creds.CheckForUpdates()

		if changed {
			log.Printf("Info: Credential changes detected, signaling conman to restart")
			conman.SignalConmanTERM()
		}

		time.Sleep(time.Duration(monitorIntervalSecs) * time.Second)
	}
}


func isTrue(str string) bool {
	lStr := strings.ToLower(str)
	if len(lStr) == 1 && (lStr[0] == 't' || lStr[0] == '1') {
		return true
	}
	if len(lStr) > 1 && lStr == "true" {
		return true
	}
	return false
}

// Log rotation setup and loop
func logRotate() {
	var logRotEnabled bool = true
	var logRotCheckFreqSec = 600
	var logRotConFileSize string = "5M"  // size of the console log file to rotate
	var logRotConNumRotate int = 2       // number of console log backup copies to keep
	var logRotAggFileSize string = "20M" // size of the aggregation file to rotate
	var logRotAggNumRotate int = 1       // number of aggregation backup copies to keep

		// Check for log rotation env vars
	if val := os.Getenv("LOG_ROTATE_ENABLE"); val != "" {
		log.Printf("Found LOG_ROTATE_ENABLE: %s", val)
		logRotEnabled = isTrue(val)
	}
	if val := os.Getenv("LOG_ROTATE_FILE_SIZE"); val != "" {
		log.Printf("Found LOG_ROTATE_FILE_SIZE: %s", val)
		logRotConFileSize = val
	}
	if val := os.Getenv("LOG_ROTATE_SEC_FREQ"); val != "" {
		log.Printf("Found LOG_ROTATE_SEC_FREQ: %s", val)
		envFreq, err := strconv.Atoi(val)
		if err != nil {
			log.Printf("Error converting log rotation frequency - expected an integer:%s", err)
		} else {
			logRotCheckFreqSec = envFreq
		}
	}
	if val := os.Getenv("LOG_ROTATE_NUM_KEEP"); val != "" {
		log.Printf("Found LOG_ROTATE_NUM_KEEP: %s", val)
		envNum, err := strconv.Atoi(val)
		if err != nil {
			log.Printf("Error converting log rotation number - expected an integer:%s", err)
		} else {
			logRotConNumRotate = envNum
		}
	}

	// log the log rotation parameters
	log.Printf("LOG ROTATE: Log rotation enabled: %v, Check Freq Sec: %d", logRotEnabled, logRotCheckFreqSec)
	log.Printf("LOG ROTATE: Log rotation console file size: %s, num rotate: %d", logRotConFileSize, logRotConNumRotate)
	log.Printf("LOG ROTATE: Log rotation aggregation file size: %s, num rotate: %d", logRotAggFileSize, logRotAggNumRotate)

	// Init log rotation
	logs.InitLogRotate(logRotEnabled, logRotCheckFreqSec, logRotConFileSize, logRotConNumRotate,  logRotAggFileSize, logRotAggNumRotate)

	sleepSecs := time.Duration(300) * time.Second
	if logRotCheckFreqSec > 0 {
		sleepSecs = time.Duration(logRotCheckFreqSec) * time.Second
	} else {
		log.Printf("Log rotation frequency invalid, defaulting to 5 min. Input value:%d", logRotCheckFreqSec)
	}

	for {
		restartConman := logs.LogRotate()
		if restartConman {
			log.Print("LOG ROTATE: Log files rotated, signaling conmand")
			conman.SignalConmanHUP()
		}

		time.Sleep(sleepSecs)
	}
} 



func main() {
	// Enable debug logging if requested.
	debugLog.Init()

	// allow for changes in the SMD URL
	nodes.HsmURL = getEnv("SMD_URL", "http://cray-smd/")
	nodes.DebugOnly = getEnv("DEBUG", "false") == "true"
	svcHost = getEnv("SVC_HOST", "0.0.0.0:8080")

	log.Printf("Remote console service starting")
	// Set up the zombie killer
	log.Printf("Starting zombie killer...")
	go conman.WatchForZombies()

	// then we set up the goroutine that controls conman
	_, err := utils.EnsureDirPresent("/var/log/conman", 0755)
	if err != nil {
		log.Fatal(err)
	}

	// I am not sure that we need this, so I am leaving it out for
	// now, I think that normal logging will work now that we only
	// have one container
	// respinAggLog()

	// Start log rotation with callback to signal conman
	go logRotate()

	// spin a thread that watches for changes in console configuration
	go watchForNodesUpdates()

	// start up the thread that runs conman
	go conman.RunConman()

	// start the thread that will make sure that the conman creds are correct
	go watchForCredUpdates()

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
		inShutdown = true
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
	// log.Printf("Info: Console API listening on: %s\n", svcHost)
	// err = server.ListenAndServe()
	// if err != nil && err != http.ErrServerClosed {
	// 	log.Fatal(err)
	// }

	// // Wait for server context to be stopped
	<-serverCtx.Done()
	log.Printf("Info: Shutdown complete.")
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
