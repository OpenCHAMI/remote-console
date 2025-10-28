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
		if isShuttingDown() {
			log.Printf("Info: Exiting node watch loop due to shutdown")
			return
		}
		changed := nodes.CheckForUpdates(smdURL())

		if changed {
			log.Printf("Info: Node changes detected, signaling conman to restart")
			conman.SignalConmanTERM()

			nodes := nodes.CurrentNodes()

			// also update log rotation configuration
			log.Printf("Info: Node changes detected, updating log rotation configuration")
			config := logConfig()
			logs.UpdateLogRotateConf(config, nodes)

			// make sure we are aggregating any new console log files
			log.Printf("Info: Node changes detected, updating log aggregation configuration")
			logs.AggregateFiles(config, nodes)
		}

		// Wait for the correct polling interval
		time.Sleep(time.Duration(newNodeLookupSec) * time.Second)
	}
}



// Watch for credential updates and signal conman as needed
func watchForCredUpdates(config creds.CredsConfig) {
	time.Sleep(time.Duration(monitorIntervalSecs) * time.Second)
	for {
		changed, err :=  creds.CheckForUpdates(config)
		if err != nil {
			log.Printf("Error checking for credential updates: %s", err)
		}

		if changed {
			log.Printf("Info: Credential changes detected, signaling conman to restart")
			conman.SignalConmanTERM()
		}

		time.Sleep(time.Duration(monitorIntervalSecs) * time.Second)
	}
}


// Log rotation setup and loop
func logRotate() {
	logConfig := logConfig()
	conmanConfig := conmanConfig()

	// log the log rotation parameters
	log.Printf("LOG ROTATE: Log rotation enabled: %v, Check Freq Sec: %d", logConfig.ConsoleLogRotateEnabled, logConfig.RotateCheckFrequency)
	log.Printf("LOG ROTATE: Log rotation console file size: %s, num rotate: %d", logConfig.ConsoleLogFileSize, logConfig.ConsoleLogNumRotate)
	log.Printf("LOG ROTATE: Log rotation aggregation file size: %s, num rotate: %d", logConfig.AggLogFileSize, logConfig.AggLogNumRotate)

	// Init log rotation
	logs.InitLogRotate(logConfig)
	// Create the log rotation configuration file
	logs.UpdateLogRotateConf(logConfig, nodes.CurrentNodes())

	sleepSecs := time.Duration(300) * time.Second
	logRotCheckFreqSec := logConfig.RotateCheckFrequency
	if logRotCheckFreqSec > 0 {
		sleepSecs = time.Duration(logRotCheckFreqSec) * time.Second
	} else {
		log.Printf("Log rotation frequency invalid, defaulting to 5 min. Input value:%d", logRotCheckFreqSec)
	}

	for {
		restartConman := logs.LogRotate(logConfig)
		if restartConman {
			log.Print("LOG ROTATE: Log files rotated, signaling conmand")
			conman.SignalConmanHUP(conmanConfig)
		}

		time.Sleep(sleepSecs)
	}
} 

func runConman()  {
	conmanConfig := conmanConfig()

	credsConfig, err := credsConfig()
	if err != nil {
		log.Panicf("Error getting creds config: %s", err)
	}

	for {
		nodes := nodes.CurrentNodes()

		var requirePasswords []string
		for _, nci := range nodes {
			if nci.IsIPMI() || nci.IsPassSSH()  {
				requirePasswords = append(requirePasswords, nci.BmcName)
			}
		}

		passwords := creds.GetPasswordsWithRetries(credsConfig, requirePasswords, 15, 10)
		hasNodes, err := conman.ConfigureConman(conmanConfig, nodes, passwords)
		if err != nil {
			log.Panicf("Error configuring conman: %s", err)
		}

		if conmanConfig.DebugOnly {
			time.Sleep(25 * time.Second)
			log.Printf("Sleeping the executeConman process")
		} else if !hasNodes {
			log.Printf("No console nodes found - trying again")
			time.Sleep(30 * time.Second)
		} else {
			err := conman.ExecuteConman(conmanConfig)
			if err != nil {
				log.Panicf("Error executing conman: %s", err)
			}
		}
		time.Sleep(10 * time.Second)
	}
}

func smdURL() string {
	return getEnv("SMD_URL", "http://cray-smd/")
}

func main() {
	// Enable debug logging if requested.
	debugLog.Init()

	// allow for changes in the SMD URL
	svcHost = getEnv("SVC_HOST", "0.0.0.0:8080")

	log.Printf("Remote console service starting")
	// Set up the zombie killer
	log.Printf("Starting zombie killer...")
	go conman.WatchForZombies()

	conmanConfig := conmanConfig()
	// then we set up the goroutine that controls conman
	_, err := utils.EnsureDirPresent(conmanConfig.LogFilesPath, 0755)
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
	go runConman()

	// start the thread that will make sure that the conman creds are correct
	credsConfig, err := credsConfig()
	if err != nil {
		log.Panicf("Error getting creds config: %s", err)
	}

	go watchForCredUpdates(credsConfig)

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
