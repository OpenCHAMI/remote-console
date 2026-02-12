// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// Copyright © 2021-2022 Hewlett Packard Enterprise Development LP
//
// SPDX-License-Identifier: MIT
//

// This file contains the code needed to handle zombie processes

package conman

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// NOTE:  Any time a container is started with a particular application running
//  as pid 1, that process is required to handle any zombie processes that are
//  orphaned in the pod.  This is a process running in the background that will
//  find zombie processes and terminate them cleanly.

// WatchForZombies scans the process table for zombie processes and cleans them up.
func WatchForZombies() {
	for {
		// get the process information from the system
		zombies := findZombies()
		// look for zombies and terminate them
		for _, zombie := range zombies {
			// kill each zombie in a separate thread
			go killZombie(zombie)
		}
		// wait for a bit before looking again
		time.Sleep(30 * time.Second)
	}
}

// findZombies finds all the current zombie processes
func findZombies() []int {
	var zombies []int = nil
	var outBuf bytes.Buffer
	// Use a 'ps -eo' style command as the basis to search for zombie processes
	// and put the output in outBuf.
	cmd := exec.Command("ps", "-eo", "pid,stat")
	cmd.Stderr = &outBuf
	cmd.Stdout = &outBuf
	err := cmd.Run()
	if err != nil {
		slog.Error("Error getting current processes", "error", err)
	}
	// process the output buffer to find zombies
	var readLine string
	for {
		// pull off a line of output and
		if readLine, err = outBuf.ReadString('\n'); err == io.EOF {
			break
		} else if err != nil {
			slog.Error("Error reading current process output", "error", err)
			break
		}
		// NOTE: a 'STATUS' of "Z" denotes a zombie process
		cols := strings.Fields(readLine)
		if len(cols) >= 2 && cols[1] == "Z" {
			// found a zombie
			zPid, err := strconv.Atoi(cols[0])
			if err == nil {
				slog.Info("Found a zombie process", "pid", zPid)
				zombies = append(zombies, zPid)
			} else {
				// atoi did not like our process "number"
				slog.Warn("Thought we had a zombie, couldn't get pid", "line", readLine)
			}
		}
	}
	return zombies
}

// killZombie kills (waits for) the zombie process with the given pid
func killZombie(pid int) {
	slog.Info("Killing zombie process", "pid", pid)
	p, err := os.FindProcess(pid)
	if err != nil {
		slog.Error("Error attaching to zombie process", "pid", pid, "error", err)
		return
	}
	// should just need to get the exit state to clean up process
	_, err = p.Wait()
	if err != nil {
		slog.Error("Error waiting for zombie process", "pid", pid, "error", err)
		return
	}
	slog.Info("Cleaned up zombie process", "pid", pid)
}
