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

package logs

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"sync"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
	"github.com/OpenCHAMI/remote-console/internal/utils"
)

var logMutex = &sync.Mutex{}

// NodeInfo interface to avoid import cycles
type NodeInfo interface {
	GetNodeName() string
}

// NOTE: the backup directory is on the shared console-operator pvc
const logRotDir string = "/var/log/conman.old"

// The configuration and state files will be on local storage
// since they need to be specific for this pod, but do not need to
// be persisted through pod restarts.  They do need to be in locations
// that are writable by 'nobody' user
const logRotConfFile string = "./logrotate.conman"
const logRotStateFile string = "/tmp/rot_conman.state"

// Globals for log rotation parameters
// NOTE: eventually make these available to change through the REST api
var logRotEnabled bool = true
var logRotCheckFreqSec = 600
var logRotConFileSize string = "5M"  // size of the console log file to rotate
var logRotConNumRotate int = 2       // number of console log backup copies to keep
var logRotAggFileSize string = "20M" // size of the aggregation file to rotate
var logRotAggNumRotate int = 1       // number of aggregation backup copies to keep

// LogRotate initializes and starts log rotation
func InitLogRotate(logRotEnabled bool, logRotCheckFreqSec int, logRotConFileSize string, logRotConNumRotate int,  logRotAggFileSize string, logRotAggNumRotate int) {
	logMutex.Lock()
	defer logMutex.Unlock()


	logRotEnabled = logRotEnabled
	logRotCheckFreqSec = logRotCheckFreqSec
	logRotConFileSize = logRotConFileSize
	logRotConNumRotate = logRotConNumRotate
	logRotAggFileSize = logRotAggFileSize
	logRotAggNumRotate = logRotAggNumRotate

	// Set up the 'backups' directory for logrotation to use
	utils.EnsureDirPresent(logRotDir, 0755)

	// Create the log rotation configuration file
	UpdateLogRotateConf()
}


// UpdateLogRotateConf updates the log rotation configuration file
func UpdateLogRotateConf() {
	log.Printf("before log mutex")
	logMutex.Lock()
	defer logMutex.Unlock()
	log.Printf("LOG ROTATE: after")

	// Open the file for writing
	log.Printf("LOG ROTATE: Opening conman log rotation configuration fillle for output: %s", logRotConfFile)
	lrf, err := os.Create(logRotConfFile)
	if err != nil {
		log.Printf("Unable to open config file to write: %s", err)
		return
	}
	defer lrf.Close()

	log.Printf("LOG ROTATE: Writing log rotation configuration file")

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

	log.Printf("LOG ROTATE: CurrentNodes")
	// Add all nodes
	consoleLogBackupDir := "/var/log/conman.old"
	for _, cni := range nodes.CurrentNodes() {
		log.Printf("cni")
		xname := cni.NodeName
		fn := fmt.Sprintf("/var/log/conman/console.%s", xname)
		writeConfigEntry(lrf, fn, consoleLogBackupDir, logRotConNumRotate, logRotConFileSize)
	}

	fmt.Fprintln(lrf, "")

	log.Printf("LOG ROTATE: Completed writing log rotation configuration file")
}

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

func parseTimestamp(line string) (string, time.Time, bool, bool) {
	var nodeName string
	var fd time.Time
	isCon := false
	isAgg := false

	const filePrefix string = "/var/log/conman/console."
	timeStampStr := ""
	pos := strings.Index(line, filePrefix)
	nodeStPos := 0
	if pos != -1 {
		nodeStPos = pos + len(filePrefix)
		posQ2 := strings.Index(line[nodeStPos:], "\"")
		if posQ2 == -1 {
			log.Printf("  Unexpected file format - expected quote to close filename")
			return nodeName, fd, isCon, isAgg
		}
		posQ2 += nodeStPos
		nodeName = line[nodeStPos:posQ2]
		timeStampStr = line[posQ2+2:]
		isCon = true
	} else {
		pos = strings.Index(line, conAggLogFile)
		if pos == -1 {
			return nodeName, fd, isCon, isAgg
		}
		nodeName = "consoleAgg.log"
		isAgg = true
		timeStampStr = line[len(conAggLogFile)+pos+2:]
	}

	var year, month, day, hour, min, sec int
	_, err := fmt.Sscanf(timeStampStr, "%d-%d-%d-%d:%d:%d", &year, &month, &day, &hour, &min, &sec)
	if err != nil {
		log.Printf("Error parsing timestamp: %s, %s", timeStampStr, err)
		return nodeName, fd, false, false
	}
	fd = time.Date(year, time.Month(month), day, hour, min, sec, 0, time.Local)

	return nodeName, fd, isCon, isAgg
}

func readLogRotTimestamps(fileStamp map[string]time.Time) (conChanged, aggChanged bool) {
	log.Printf("LOG ROTATE: Reading log rotation timestamps")
	conChanged = false
	aggChanged = false

	sf, err := os.Open(logRotStateFile)
	if err != nil {
		log.Printf("Unable to open log rotation state file %s: %s", logRotStateFile, err)
		return false, false
	}
	defer sf.Close()

	er := bufio.NewReader(sf)
	for {
		line, err := er.ReadString('\n')
		if err != nil {
			break
		}

		if fileName, fd, isCon, isAgg := parseTimestamp(line); isCon || isAgg {
			if _, ok := fileStamp[fileName]; ok {
				if fileStamp[fileName] != fd {
					log.Printf("LOG ROTATE:  %s rotated", fileName)
					fileStamp[fileName] = fd
					if isCon {
						conChanged = true
					} else {
						aggChanged = true
					}
				}
			} else {
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

// LogRotate performs a log rotation check and rotation if needed
func LogRotate() bool{
	changed := false
	fileStamp := make(map[string]time.Time)
	readLogRotTimestamps(fileStamp)

	if logRotEnabled {
		changed = rotateLogsOnce(fileStamp)
	}
	
	return changed
}

func rotateLogsOnce(fileStamp map[string]time.Time) bool {
	conChanged := false
	log.Print("LOG ROTATE: Starting logrotate")
	cmd := exec.Command("logrotate", "-s", logRotStateFile, logRotConfFile)
	exitCode := -1
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ProcessState.ExitCode()
			log.Printf("Exit Error: %s", ee)
		}
	} else {
		exitCode = 0
	}
	log.Printf("LOG ROTATE: Log Rotation completed with exit code: %d", exitCode)

	if conChanged, aggChanged := readLogRotTimestamps(fileStamp); conChanged || aggChanged {
		time.Sleep(5 * time.Second)

		if aggChanged {
			RespinAggLog()
		}
	} else {
		log.Print("LOG ROTATE: No log files changed with logrotate")
	}

	return conChanged
}
