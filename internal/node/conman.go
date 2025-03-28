//
//  MIT License
//
//  (C) Copyright 2019-2024 Hewlett Packard Enterprise Development LP
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

// This file contains the functions to configure and interact with conman

package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Global to access running conmand process
var command *exec.Cmd = nil

// Location of configuration files
const baseConfFile string = "/app/conman_base.conf"
const confFile string = "/etc/conman.conf"

// Do all the steps needed to update configurations for a given conmand process
func configConman(forceConfigUpdate bool) bool {
	// maintain a lock on the current nodes while doing complete configuration
	// NOTE: this prevents the lists from being updated in the middle of doing
	//  the configuration
	currNodesMutex.Lock()
	defer currNodesMutex.Unlock()

	// Set up or update the conman configuration file.
	updateConfigFile(forceConfigUpdate)

	// set up a thread to add log output to the aggregation file
	allNodes := [3](*map[string]*nodeConsoleInfo){&currentRvrNodes, &currentPdsNodes, &currentMtnNodes}
	for _, ar := range allNodes {
		for nn := range *ar {
			// make sure the node is being aggregated - no-op if already being done
			aggregateFile(nn)
		}
	}

	// Make sure that we have a proper ssh console keypair deployed
	// here and on the Mountain BMCs before starting conman.
	// NOTE: this function will wait to return until keys are
	//  present if there are Mountain consoles to configure
	ensureMountainConsoleKeysPresent()

	// return if there are any nodes
	return (len(currentMtnNodes) + len(currentRvrNodes) + len(currentPdsNodes)) > 0
}

// Loop that starts / restarts conmand process
func runConman() {
	// This loop runs forever, updating the configuration file and
	// starting or restarting the conmand process when needed
	// NOTE: force a creation of the config file the first time through
	//  the loop even if the user requests no updates
	forceConfigUpdate := true
	for {
		// do the configuration steps - force update on first pass
		hasNodes := configConman(forceConfigUpdate)
		forceConfigUpdate = false

		// start the conmand process
		if debugOnly {
			// not really running, just give a longer pause before re-running config
			time.Sleep(25 * time.Second)
			log.Printf("Sleeping the executeConman process")
		} else if !hasNodes {
			// nothing found, don't try to start conmand
			log.Printf("No console nodes found - trying again")
			time.Sleep(30 * time.Second)
		} else {
			// looks good to start the conmand process
			// NOTE: this function will not exit until the process exits, and will
			//  spin up a new one on exit.  This will allow a user to manually
			//  kill the conmand process and this will restart while re-reading
			//  the configuration file.
			executeConman()
		}

		// There are times we want to wait for a little before starting a new
		// process - ie killproc may get caught trying to kill all instances
		time.Sleep(10 * time.Second)
	}
}

// Function to send SIGHUP to running conmand process
func signalConmanHUP() {
	// send interrupt to tell conman to re-initialize - this is usually called
	//  after a log rotation and all log files will be regenerated
	if command != nil {
		log.Print("Signaling conman with SIGHUP")
		command.Process.Signal(syscall.SIGHUP)
	} else {
		log.Print("Warning: Attempting to signal conman process when nil.")

		// if we are in debug mode, respin the fake logs as needed
		if debugOnly {
			// NOTE - debugging test code, so don't worry about mutex for current nodes
			log.Printf("Respinning current log test files...")
			for nn := range currentRvrNodes {
				go createTestLogFile(nn, true)
			}
			for nn := range currentMtnNodes {
				go createTestLogFile(nn, true)
			}
		}
	}
}

// Function to send SIGTERM to running conmand process
func signalConmanTERM() {
	// send interupt to tell conmand process to terminate
	//  NOTE: this is called to force a complete re-initialization including
	//   regenerating the configuration file
	if command != nil {
		log.Print("Signaling conman with SIGTERM")
		command.Process.Signal(syscall.SIGTERM)
	} else {
		log.Print("Warning: Attempting to signal conman process when nil.")
	}
}

// Execute the conman process
func executeConman() {
	// This function  will start an instance of 'conmand' on the local
	// system, route the output from that process into this log stream,
	// and exit when that process is killed
	log.Print("Starting a new instance of conmand")

	// NOTE - should not happen, just checking
	if command != nil {
		log.Print("ERROR: command not nil on entry to executeComman!!")
	}

	// Start the conmand command with arguments
	//   -F : run in foreground
	//   -v : enable verbose mode for logging
	//   -c : specify the configuration file
	command = exec.Command("conmand", "-F", "-v", "-c", confFile)

	// capture the stderr and stdout pipes from this command
	cmdStdErr, err := command.StderrPipe()
	if err != nil {
		log.Panicf("Unable to connect to conmand stderr pipe: %s", err)
	}
	cmdStdOut, err := command.StdoutPipe()
	if err != nil {
		log.Panicf("Unable to connect to conmand stdout pipe: %s", err)
	}

	// spin a thread to read the stderr pipe
	go logPipeOutput(&cmdStdErr, "stderr")

	// spin a thread to read the stdout pipe
	go logPipeOutput(&cmdStdOut, "stdout")

	// start the command
	log.Print("Starting conmand process")
	if err = command.Start(); err != nil {
		log.Panicf("Unable to start the command: %s", err)
	}

	// wait for the process to exit
	// NOTE - execution will stop here until the process completes!
	if err = command.Wait(); err != nil {
		// Report error and pause before trying again
		log.Printf("Error from command wait: %s", err)
		time.Sleep(15 * time.Second)
	}
	command = nil
	log.Print("Conmand process has exited")
}

// read the beginning of the input file to see if we should skip this update
func willUpdateConfig(fp *os.File) bool {
	// if the first line of the base configuration file has '# UPDATE_CONFIG=FALSE'
	// then bail on the update
	// NOTE: only reading first 50 bytes of file, should be at least that many
	//  present if this is a valid base configuration file and don't need to read more.
	buff := make([]byte, 50)
	n, err := fp.Read(buff)
	if err != nil || n < 50 {
		log.Printf("Read of base configuration failed. Bytes read: %d, error:%s", n, err)
		return false
	}

	// convert to string for easier handling
	s := string(buff[:n])
	//log.Printf("Skip update test line: %s", s)

	// search for config flag
	retVal := false
	ss := "UPDATE_CONFIG="
	pos := strings.Index(s, ss)
	if pos > 0 {
		// found it - get the value
		valPos := pos + len(ss)
		retVal = s[valPos] != 'F' && s[valPos] != 'f'
		//log.Printf("Found update string. pos:%d, valPod:%d, val:%q, retVal:%t", pos, valPos, s[valPos], retVal)
		//} else {
		//	log.Printf("Didn't find update string")
	}

	// reset the file pointer so later read starts at beginning of file
	_, err = fp.Seek(0, 0)
	if err != nil {
		log.Printf("Reset of file pointer to beginning of file failed:%s", err)
	}

	return retVal
}

// Update the configuration file with the current endpoints
func updateConfigFile(forceUpdate bool) {
	// NOTE: in update config thread

	log.Print("Updating the configuration file")

	// open the base file
	log.Printf("Opening base configuration file: %s", baseConfFile)
	bf, err := os.Open(baseConfFile)
	if err != nil {
		// log the problem and bail
		log.Panicf("Unable to open base config file: %s", err)
	}
	defer bf.Close()

	// if the skip update flag has been set then don't do this update
	if !forceUpdate && !willUpdateConfig(bf) {
		log.Print("Skipping update due to base config file flag")
		return
	}

	// open the configuration file for output
	log.Printf("Opening conman configuration file for output: %s", confFile)
	cf, err := os.OpenFile(confFile, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		// log the problem and panic
		log.Panicf("Unable to open config file to write: %s", err)
	}
	defer cf.Close()

	// copy the base file to the configuration file
	_, err = io.Copy(cf, bf)
	if err != nil {
		log.Printf("Unable to copy base file into config: %s", err)
	}

	// collect the creds for the river and paradise endpoints
	var rvrXNames []string = nil
	for _, v := range currentRvrNodes {
		rvrXNames = append(rvrXNames, v.BmcName)
	}
	for _, v := range currentPdsNodes {
		rvrXNames = append(rvrXNames, v.BmcName)
	}

	// gather the river passwords
	// NOTE: sometimes if vault hasn't been populated yet there may be no
	// return values - try again for a while in that case.
	passwords := getPasswordsWithRetries(rvrXNames, 15, 10)
	previousPasswords = passwords

	// Add River endpoints to the config file to be accessed by ipmi
	for _, nodeCi := range currentRvrNodes {
		// connect using ipmi
		creds, ok := passwords[nodeCi.BmcName]
		if !ok {
			log.Printf("No creds record returned for %s", nodeCi.BmcName)
		}
		log.Printf("console name=\"%s\" dev=\"ipmi:%s\" ipmiopts=\"U:%s,P:REDACTED,W:solpayloadsize\"\n",
			nodeCi.NodeName,
			nodeCi.BmcFqdn,
			creds.Username)
		// write the line to the config file
		output := fmt.Sprintf("console name=\"%s\" dev=\"ipmi:%s\" ipmiopts=\"U:%s,P:%s,W:solpayloadsize\"\n",
			nodeCi.NodeName,
			nodeCi.BmcFqdn,
			creds.Username,
			creds.Password)

		// write the output line if there is anything present
		if _, err = cf.WriteString(output); err != nil {
			// log the error then panic
			// TODO - maybe a little harsh to kill the entire process here?
			log.Panic(err)
		}

	}

	// Add Paradise endpoints to the config file to be accessed by ssh, but with passwords
	for _, nodeCi := range currentPdsNodes {
		// connect using ipmi
		creds, ok := passwords[nodeCi.BmcName]
		if !ok {
			log.Printf("No creds record returned for %s", nodeCi.BmcName)
		}
		log.Printf("console name=\"%s\" dev=\"/usr/bin/ssh-pwd-console %s %s REDACTED\"\n",
			nodeCi.NodeName,
			nodeCi.BmcFqdn,
			creds.Username)
		// write the line to the config file
		output := fmt.Sprintf("console name=\"%s\" dev=\"/usr/bin/ssh-pwd-console %s %s %s\"\n",
			nodeCi.NodeName,
			nodeCi.BmcFqdn,
			creds.Username,
			creds.Password)

		// write the output line if there is anything present
		if _, err = cf.WriteString(output); err != nil {
			// log the error then panic
			// TODO - maybe a little harsh to kill the entire process here?
			log.Panic(err)
		}

	}

	// Add Mountain endpoints to the config file
	for _, nodeCi := range currentMtnNodes {
		log.Printf("console name=\"%s\" dev=\"/usr/bin/ssh-key-console %s\"\n",
			nodeCi.NodeName,
			nodeCi.NodeName)
		// write the line to the config file
		output := fmt.Sprintf("console name=\"%s\" dev=\"/usr/bin/ssh-key-console %s\"\n",
			nodeCi.NodeName,
			nodeCi.NodeName)

		// write the output line if there is anything present
		if _, err = cf.WriteString(output); err != nil {
			// log the error then panic
			// TODO - maybe a little harsh to kill the entire process here?
			log.Panic(err)
		}

	}
}
