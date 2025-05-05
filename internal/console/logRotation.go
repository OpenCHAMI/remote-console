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

// This file contains the code needed to handle log rotation inside the console pod.

package console

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// NOTE: the backup directory is on the shared console-operator pvc
const logRotDir string = "/var/log/conman.old"

// The configuration and state files will be on local storage
// since they need to be specific for this pod, but do not need to
// be persisted through pod restarts.  They do need to be in locations
// that are writable by 'nobody' user
const logRotConfFile string = "/app/logrotate.conman"
const logRotStateFile string = "/tmp/rot_conman.state"

// Globals for log rotation parameters
// NOTE: eventually make these available to change through the REST api
var logRotEnabled bool = true
var logRotCheckFreqSec = 600
var logRotConFileSize string = "5M"  // size of the console log file to rotate
var logRotConNumRotate int = 2       // number of console log backup copies to keep
var logRotAggFileSize string = "20M" // size of the aggregation file to rotate
var logRotAggNumRotate int = 1       // number of aggregation backup copies to keep

// Initialize and start log rotation
func LogRotate() {
	// Set up the 'backups' directory for logrotation to use
	EnsureDirPresent(logRotDir, 0755)

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
			log.Printf("Error converting log rotation freqency - expected an integer:%s", err)
		} else {
			logRotCheckFreqSec = envFreq
		}
	}
	if val := os.Getenv("LOG_ROTATE_NUM_KEEP"); val != "" {
		log.Printf("Found LOG_ROTATE_NUM_KEEP: %s", val)
		envNum, err := strconv.Atoi(val)
		if err != nil {
			log.Printf("Error converting log rotation freqency - expected an integer:%s", err)
		} else {
			logRotConNumRotate = envNum
		}
	}

	// log the log rotation parameters
	log.Printf("LOG ROTATE: Log rotation enabled: %v, Check Freq Sec: %d", logRotEnabled, logRotCheckFreqSec)
	log.Printf("LOG ROTATE: Log rotation console file size: %s, num rotate: %d", logRotConFileSize, logRotConNumRotate)
	log.Printf("LOG ROTATE: Log rotation aggregation file size: %s, num rotate: %d", logRotAggFileSize, logRotAggNumRotate)

	// Create the log rotation configuration file
	doInitialConfFileUpdate()

	// Start the log rotation thread
	go doLogRotate()
}

// All the ways a string could be interpreted as 'true'
func isTrue(str string) bool {
	// convert to lower case to remove capitalization as an issue
	lStr := strings.ToLower(str)

	// deal with one char possible values for true
	if len(lStr) == 1 && (lStr[0] == 't' || lStr[0] == '1') {
		return true
	}

	// deal with multiple char possible values for true
	if len(lStr) > 1 && lStr == "true" {
		return true
	}

	// treat everything else as false
	return false
}

// Do the initial log rotation file update in a thread safe manner
func doInitialConfFileUpdate() {
	// Make sure the initial log rotation file doesn't miss or overwrite
	// the initial batch of consoles being monitored.

	// put a lock on the current nodes while writing the file
	currNodesMutex.Lock()
	defer currNodesMutex.Unlock()

	// update the file now that it is safe to do so
	updateLogRotateConf()
}

// Create the log rotation configuration file
func updateLogRotateConf() {
	// NOTE: calling function needs to ensure current node maps are
	//  thread protected
	// NOTE: in doGetNewNodes thread
	// NOTE: also in initial configuration

	// This is the default format supplied by the install of
	// the conman package.
	// NOTE: conmand needs the '-HUP' signal to reconnect to
	//  log files after they have been moved/removed.  We will
	//  do that ourselves so are removing it from the conf file.
	/*
		# /var/log/conman/* {
		#   compress
		#   missingok
		#   nocopytruncate
		#   nocreate
		#   nodelaycompress
		#   nomail
		#   notifempty
		#   olddir /var/log/conman.old/
		#   rotate 4
		#   sharedscripts
		#   size=5M
		#   weekly
		#   postrotate
		#     /usr/bin/killall -HUP conmand
		#   endscript
		# }
	*/

	// Open the file for writing
	log.Printf("LOG ROTATE: Opening conman log rotation configuration file for output: %s", logRotConfFile)
	lrf, err := os.Create(logRotConfFile)
	if err != nil {
		// log the problem and panic
		log.Printf("Unable to open config file to write: %s", err)
	}
	defer lrf.Close()

	// We need to do log rotation ONLY for the logs this pod is
	//  actively managing.  Each log file needs to be given a separate
	//  entry in the file.

	// Write out the contents of the file
	fmt.Fprintln(lrf, "# Auto-generated conman log rotation configuration file.")

	// Add the aggregation file
	if conAggLogFile != "" {
		conAggLogDir := filepath.Dir(conAggLogFile)
		if len(conAggLogDir) > 0 {
			writeConfigEntry(lrf, conAggLogFile, conAggLogDir, logRotAggNumRotate, logRotAggFileSize)
		} else {
			log.Printf("Invalid aggregation file name/dir, not added to log rotation: %s, %s", conAggLogFile, conAggLogDir)
		}
	}

	// Add all nodes
	consoleLogBackupDir := "/var/log/conman.old"
	for _, cni := range currentNodes {
		xname := cni.NodeName
		fn := fmt.Sprintf("/var/log/conman/console.%s", xname)
		writeConfigEntry(lrf, fn, consoleLogBackupDir, logRotConNumRotate, logRotConFileSize)
	}

	fmt.Fprintln(lrf, "")
}

// helper function to write out a single entry in the config file
func writeConfigEntry(lrf *os.File, fileName string, oldDir string, numRotate int, fileSize string) {
	fmt.Fprintf(lrf, "%s { \n", fileName)
	fmt.Fprintln(lrf, "  nocompress")
	fmt.Fprintln(lrf, "  missingok")
	fmt.Fprintln(lrf, "  nocopytruncate")
	fmt.Fprintln(lrf, "  nocreate")
	fmt.Fprintln(lrf, "  nodelaycompress")
	fmt.Fprintln(lrf, "  nomail")
	fmt.Fprintln(lrf, "  notifempty")
	fmt.Fprintf(lrf, "  olddir %s\n", oldDir)
	fmt.Fprintf(lrf, "  rotate %d\n", numRotate)
	fmt.Fprintf(lrf, "  size=%s\n", fileSize)
	fmt.Fprintln(lrf, "}")
}

// Parse the timestamp from the input line
func parseTimestamp(line string) (string, time.Time, bool, bool) {
	// NOTE: we are expecting a line in the format of:
	//  "/var/log/conman/console.xname" YYYY-MM-DD-HH-MM-SS
	var nodeName string
	var fd time.Time
	isCon := false
	isAgg := false

	// if the line does not have a valid console log name, skip
	const filePrefix string = "/var/log/conman/console."
	timeStampStr := ""
	pos := strings.Index(line, filePrefix)
	nodeStPos := 0
	if pos != -1 {
		// found a node log file - pull out the node name and time stamp string
		nodeStPos = pos + len(filePrefix)

		// pull out the node name
		posQ2 := strings.Index(line[nodeStPos:], "\"")
		if posQ2 == -1 {
			// unexpected - should be a " char at the end of the filename
			log.Printf("  Unexpected file format - expected quote to close filename")
			return nodeName, fd, isCon, isAgg
		}

		// reindex for position in entire line and split
		posQ2 += nodeStPos
		nodeName = line[nodeStPos:posQ2]
		timeStampStr = line[posQ2+2:]
		isCon = true
	} else {
		// see if this is the console aggregation log file
		pos = strings.Index(line, conAggLogFile)
		if pos == -1 {
			// no log files on this line
			return nodeName, fd, isCon, isAgg
		}

		// we are dealing with the console aggregation log
		nodeName = "consoleAgg.log"
		isAgg = true

		// pull out the position of the timestamp
		timeStampStr = line[len(conAggLogFile)+pos+2:]
	}

	// process the line
	var year, month, day, hour, min, sec int
	_, err := fmt.Sscanf(timeStampStr, "%d-%d-%d-%d:%d:%d", &year, &month, &day, &hour, &min, &sec)
	if err != nil {
		// log the error and skip processing this line
		log.Printf("Error parsing timestamp: %s, %s", timeStampStr, err)
		return nodeName, fd, false, false
	}
	// current timestamp of this log rotation entry
	fd = time.Date(year, time.Month(month), day, hour, min, sec, 0, time.Local)

	return nodeName, fd, isCon, isAgg
}

// Function to collect most recent log rotation timestamps
func readLogRotTimestamps(fileStamp map[string]time.Time) (conChanged, aggChanged bool) {
	// read the timestamps from the log rotation state file
	log.Printf("LOG ROTATE: Reading log rotation timestamps")

	// return true if something has changed, may need to restart conmand or aggregation log
	conChanged = false
	aggChanged = false

	// open the state file
	sf, err := os.Open(logRotStateFile)
	if err != nil {
		log.Printf("Unable to open log rotation state file %s: %s", logRotStateFile, err)
		return false, false
	}
	defer sf.Close()

	// process the lines in the file
	// NOTE: we will only look for files with console.xname
	er := bufio.NewReader(sf)
	for {
		// read the next line
		line, err := er.ReadString('\n')
		if err != nil {
			// done reading file
			break
		}

		// parse this file timestamp
		if fileName, fd, isCon, isAgg := parseTimestamp(line); isCon || isAgg {
			// see if this file already is in the map
			if _, ok := fileStamp[fileName]; ok {
				// entry present, check for timestamp equality
				if fileStamp[fileName] != fd {
					log.Printf("LOG ROTATE:  %s rotated", fileName)
					// update and mark change
					fileStamp[fileName] = fd
					if isCon {
						conChanged = true
					} else {
						aggChanged = true
					}
				}
			} else {
				// not already present in the map so add it and mark change
				log.Printf("LOG ROTATE:  %s new file - added to map", fileName)
				fileStamp[fileName] = fd
				if isCon {
					conChanged = true
				} else {
					aggChanged = true
				}
			}
		}
	}

	return conChanged, aggChanged
}

// Function to periodically do the log rotation
func doLogRotate() {
	// put an initial delay into starting log rotation to allow things to come up
	time.Sleep(120 * time.Second)

	// turn the check frequency into a valid time duration
	sleepSecs := time.Duration(300) * time.Second
	if logRotCheckFreqSec > 0 {
		// make sure we have a valid number before converting
		sleepSecs = time.Duration(logRotCheckFreqSec) * time.Second
	} else {
		log.Printf("Log rotation freqency invalid, defaulting to 5 min. Input value:%d", logRotCheckFreqSec)
	}

	// keep track of last rotate time for all log files - need to kick
	// conmand if any log files changed.
	fileStamp := make(map[string]time.Time)
	readLogRotTimestamps(fileStamp)

	// loop forever waiting the correct period between checking for log rotations
	for {
		// if log rotation is enabled, do the check
		if logRotEnabled {
			rotateLogsOnce(fileStamp)
		}

		// sleep until the next check time
		time.Sleep(sleepSecs)
	}
}

func rotateLogsOnce(fileStamp map[string]time.Time) {
	// kick off the log rotation command
	// NOTE: using explicit state file to insure it is on pvc storage and
	//  to be able to parse it after completion.
	log.Print("LOG ROTATE: Starting logrotate")
	cmd := exec.Command("logrotate", "-s", logRotStateFile, logRotConfFile)
	exitCode := -1
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ProcessState.ExitCode()
			log.Printf("Exit Errro: %s", ee)
		}
	} else {
		exitCode = 0
	}
	log.Printf("LOG ROTATE: Log Rotation completed with exit code: %d", exitCode)

	// see if files were actually rotated - kick conmand if needed
	if conChanged, aggChanged := readLogRotTimestamps(fileStamp); conChanged || aggChanged {
		// Give a slight pause to let the system catch up
		time.Sleep(5 * time.Second)

		// conman must be signaled to reconnect to moved log files
		if conChanged {
			log.Print("LOG ROTATE: Log files rotated, signaling conmand")
			signalConmanHUP()
		}

		// the aggregation log must be restarted for moved file
		if aggChanged {
			respinAggLog()
		}
	} else {
		log.Print("LOG ROTATE: No log files changed with logrotate")
	}

}
