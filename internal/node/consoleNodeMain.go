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
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// global var to help with local running/debugging
var debugOnly bool = false

// Global to identify which pod this is
var podName string = ""
var podID string = ""
var podLocData *PodLocationDataResponse = &PodLocationDataResponse{PodName: "", Xname: "", Alias: ""}

// globals for http server
var httpListen string = ":26776"

// global to signify service is shutting down
var inShutdown bool = false

// identify what the name of this pod is
func setPodName() {
	// The pod name is set as an env variable by the k8s system on pod
	// startup.  It should be 'cray-console-node-#' where # is an
	// identifying string (number for stateful set, string for deployment)
	if val := os.Getenv("MY_POD_NAME"); val != "" {
		podName = val
		log.Printf("Pod name found: %s", podName)
	} else {
		// not found, so as stopgap make random number > 1000
		rand.Seed(time.Now().UnixNano())
		r := rand.Intn(2000) + 1000 // Random number between [1000,3000)
		podName = "cray-console-node-" + strconv.Itoa(r)
		log.Printf("Error: Pod name not set in env - defaulting to random id: %s", podName)
	}

	// pull the id off the back of the pod name
	if len(podName) > 0 {
		pos := strings.LastIndex(podName, "-")
		if pos > 0 {
			podID = podName[pos+1:]
			log.Printf("Pod id found: %s", podID)
		} else {
			log.Printf("Unexpected pod name format: %s", podName)
		}
	} else {
		log.Printf("Podname empty - unable to find pod id")
	}

	// set the aggregation log name based on the pod name
	conAggLogFile = conAggLogFileBase + podName + ".log"
}

// identify where the current pod is running, if there is no mapping with the node alias
// to the xname provided then pod location should be ignored. There is no guarantee that
// console-operator will able to provide a mapping from hms-sls at all times.
func setPodLocation(os OperatorService) {
	var resp *PodLocationDataResponse
	var err error
	var retryInterval time.Duration = os.OperatorRetryInterval()
	for {
		resp, err = os.getPodLocation(podName)
		if err != nil {
			log.Printf("Error: Failed to retrieve location from console-operator, retrying in %f\n", retryInterval.Seconds())
		} else {
			podLocData = resp
			return
		}
		// Block and retry until location is returned
		time.Sleep(retryInterval)
	}
}

// Main loop for the application
func main() {
	// NOTE: this is a work in progress starting to restructure this application
	//  to manage the console state - watching for hardware changes and
	//  updating / restarting the conman process when needed

	// parse the command line flags to the application
	flag.BoolVar(&debugOnly, "debug", false, "Run in debug only mode, not starting conmand")
	flag.Parse()

	// grab env vars
	if v := os.Getenv("DEBUG"); v == "TRUE" {
		debugOnly = true
	}
	readSingleEnvVarInt("HEARTBEAT_SEND_FREQ_SEC", &heartbeatIntervalSecs, 5, 300)
	readSingleEnvVarInt("NODE_UPDATE_FREQ_SEC", &newNodeLookupSec, 10, 600)
	readSingleEnvVarInt("MAX_ACQUIRE_PER_UPDATE_MTN", &maxAcquireMtn, 5, 2000)
	readSingleEnvVarInt("MAX_ACQUIRE_PER_UPDATE_RVR", &maxAcquireRvr, 5, 4000)

	// log the fact if we are in debug mode
	if debugOnly {
		log.Print("Running in DEBUG-ONLY mode.")
	}

	// do a quick check for creating needed directories
	// NOTE: should probably be moved somewhere else, but want it really early in the
	//  process for now...
	ensureDirPresent("/var/log/conman", 666)

	// identify this pod
	log.Printf("Setting pod information...")
	setPodName()

	// Construct services
	operatorService := NewOperatorService()

	// Find pod location in k8s, this must block and retry
	setPodLocation(operatorService)

	// start the aggregation log
	respinAggLog()

	// Initialize and start log rotation
	logRotate()

	// Set up the zombie killer
	log.Printf("Starting zombie killer...")
	go watchForZombies()

	// spin a thread that watches for changes in console configuration
	log.Printf("Starting hardware watch loop...")
	go watchForNodes()

	// start up the heartbeat in a separate thread
	go doHeartbeat()

	// start up the thread that runs conman
	go runConman()

	// start up the thread to monitor for configuration changes
	go doMonitor()

	// set up mechanism to test for killing tail functions
	if debugOnly {
		go killTails()
	}

	// set up a channel to wait for the os to tell us to stop
	// NOTE - must be set up before initializing anything that needs
	//  to be cleaned up.  This will trap any signals and wait to
	//  process them until the channel is read.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)

	// register handlers for http requests
	// NOTE: just doing it here for now, when it gets more complex break this
	//  into a separate function
	log.Printf("Setting http handlers...")
	http.HandleFunc("/console-node/liveness", doLiveness)
	http.HandleFunc("/console-node/readiness", doReadiness)
	http.HandleFunc("/console-node/health", doHealth)

	// spin the server in a separate thread so main can wait on an os
	// signal to cleanly shut down
	log.Printf("Spinning up http server...")
	httpSrv := http.Server{
		Addr:    httpListen,
		Handler: http.DefaultServeMux,
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
	log.Printf("Info: Detected signal to close service: %s", sig)
	inShutdown = true

	// release all the current nodes immediately so they can be re-assigned
	releaseAllNodes()

	// stop the server from taking requests
	// NOTE: this waits for active connections to finish
	log.Printf("Info: Server shutting down")
	httpSrv.Shutdown(context.Background())

	log.Printf("Info: Service Exiting.")
}

// make sure that all nodes are released immediately
func releaseAllNodes() {
	// make sure nobody else is messing with the current nodes
	currNodesMutex.Lock()
	defer currNodesMutex.Unlock()

	log.Printf("Releasing all nodes back for re-assignment")
	// gather all current nodes
	var rn []nodeConsoleInfo

	// iterate through all nodes to stop tailing into the aggregation logs
	allNodes := [3](*map[string]*nodeConsoleInfo){&currentRvrNodes, &currentPdsNodes, &currentMtnNodes}
	for _, ar := range allNodes {
		// release river nodes
		for key, ni := range *ar {
			// record and stop tailing
			rn = append(rn, *ni)
			stopTailing(key)
		}
	}

	// release all current node lists
	currentRvrNodes = make(map[string]*nodeConsoleInfo)
	currentPdsNodes = make(map[string]*nodeConsoleInfo)
	currentMtnNodes = make(map[string]*nodeConsoleInfo)

	// release the nodes from console-data
	releaseNodes(rn)
}

// Utility function to ensure that a directory exists
func ensureDirPresent(dir string, perm os.FileMode) (bool, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		log.Printf("Directory does not exist, creating: %s", dir)
		err = os.MkdirAll(dir, perm)
		if err != nil {
			log.Printf("Unable to create dir: %s", err)
			return false, err
		}
	}
	return true, nil
}

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
