//
//  MIT License
//
//  (C) Copyright 2021-2024 Hewlett Packard Enterprise Development LP
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

// This file contains the main elements of the application used to
// monitor console applications

package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

// global var to help with local running/debugging
var debugOnly bool = false

// globals for http server
var httpListen string = ":26777"

// Function to read a single env variable into a variable with min/max checks
func readSingleEnvVarInt(envVar string, outVar *int, minVal, maxVal int) {
	// get the env var for maximum number of mountain nodes per pod
	if v := os.Getenv(envVar); v != "" {
		log.Printf("Found %s env var: %s", envVar, v)
		vi, err := strconv.Atoi(v)
		if err != nil {
			log.Printf("Error converting value for %s - expected an integer:%s", envVar, err)
		} else {
			// do some sanity checking
			if vi < minVal {
				log.Printf("Defaulting %s to minimum value:%d", envVar, minVal)
				vi = minVal
			}
			if vi > maxVal {
				log.Printf("Defaulting %s to maximum value:%d", envVar, maxVal)
				vi = maxVal
			}
			*outVar = vi
		}
	}
}

// Main loop for the application
func main() {
	// parse the command line flags to the application
	flag.BoolVar(&debugOnly, "debug", false, "Run in debug only mode, not starting conmand")
	flag.Parse()

	// read the env variables into global vars with min/max sanity checks
	if v := os.Getenv("DEBUG"); v == "TRUE" {
		debugOnly = true
	}
	readSingleEnvVarInt("MAX_MTN_NODES_PER_POD", &maxMtnNodesPerPod, 5, 1500)
	readSingleEnvVarInt("MAX_RVR_NODES_PER_POD", &maxRvrNodesPerPod, 5, 4000)
	readSingleEnvVarInt("HARDWARE_UPDATE_SEC_FREQ", &newHardwareCheckPeriodSec, 10, 14400) // 10 sec -> 4 hrs
	readSingleEnvVarInt("HEARTBEAT_CHECK_SEC_FREQ", &heartbeatCheckPeriodSec, 10, 300)     // 10 sec -> 5 min
	readSingleEnvVarInt("HEARTBEAT_STALE_DURATION_MINUTES", &heartbeatStaleMinutes, 1, 60) // 1 min -> 60 min

	// log the fact if we are in debug mode
	if debugOnly {
		log.Print("Running in DEBUG-ONLY mode.")
	}

	// construct dependency injection
	k8Manager, err := NewK8Manager()
	if err != nil {
		log.Panicf("ERROR: k8Manager failed to initialize")
	}
	slsManager := NewSlsManager()
	nodeManager := NewNodeManager(k8Manager)
	dataManager := NewDataManager(k8Manager, slsManager)
	healthManager := NewHealthManager(dataManager)
	debugManager := NewDebugManager(dataManager, healthManager)

	// Set up the zombie killer
	go watchForZombies()

	// loop over new hardware
	go watchHardware(dataManager, nodeManager)

	// spin a thread to check for stale heartbeat information
	go dataManager.checkHeartbeats()

	// set up a channel to wait for the os to tell us to stop
	// NOTE - must be set up before initializing anything that needs
	//  to be cleaned up.  This will trap any signals and wait to
	//  process them until the channel is read.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)

	setupRoutes(dataManager, healthManager, debugManager)

	// spin the server in a separate thread so main can wait on an os
	// signal to cleanly shut down
	log.Printf("Spinning up http server...")
	httpSrv := http.Server{
		Addr:    httpListen,
		Handler: router,
	}
	go func() {
		// NOTE: do not use log.Fatal as that will immediately exit
		// the program and short-circuit the shutdown logic below
		log.Printf("Info: Server %s\n", httpSrv.ListenAndServe())
	}()
	log.Printf("Info: console-operator API listening on: %v\n", httpListen)

	//////////////////
	// Clean shutdown section
	//////////////////

	// wait here for a signal from the os that we are shutting down
	sig := <-sigs
	inShutdown = true
	log.Printf("Info: Detected signal to close service: %s", sig)

	// stop the server from taking requests
	// NOTE: this waits for active connections to finish
	log.Printf("Info: Server shutting down")
	httpSrv.Shutdown(context.Background())

	log.Printf("Info: Service Exiting.")
}
