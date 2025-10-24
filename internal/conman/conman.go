// MIT License
// (C) Copyright 2025 Hewlett Packard Enterprise Development LP
//
// This file contains the interfaces and dependency injection points for conman management.

package conman

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"bufio"
	"sync"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
	"github.com/OpenCHAMI/remote-console/internal/creds"
)

var conmanMutex = &sync.Mutex{}
var command *exec.Cmd = nil

const baseConfFile string = "/home/cjh/work/source/remote-console/scripts/conman.conf"
const confFile string = "/tmp/conman.conf"

// RunConman starts the conman management loop with injected dependencies.
func RunConman() {
	forceConfigUpdate := true
	for {
		hasNodes := configConman(forceConfigUpdate)
		forceConfigUpdate = false

		if nodes.DebugOnly {
			time.Sleep(25 * time.Second)
			log.Printf("Sleeping the executeConman process")
		} else if !hasNodes {
			log.Printf("No console nodes found - trying again")
			time.Sleep(30 * time.Second)
		} else {
			executeConman()
		}
		time.Sleep(10 * time.Second)
	}
}

func configConman(forceConfigUpdate bool) bool {
	conmanMutex.Lock()
	defer conmanMutex.Unlock()

	return updateConfigFile(forceConfigUpdate)
}

func updateConfigFile(forceUpdate bool) bool{
	log.Print("Updating the configuration file")
	log.Printf("Opening base configuration file: %s", baseConfFile)
	bf, err := os.Open(baseConfFile)
	if err != nil {
		log.Panicf("Unable to open base config file: %s", err)
	}
	defer bf.Close()

	log.Printf("forceUpdate=%v, willUpdateConfig=%v", forceUpdate, willUpdateConfig(bf))

	if !forceUpdate && !willUpdateConfig(bf) {
		log.Print("Skipping update due to base config file flag")
		return false
	}

	log.Printf("Opening conman configuration file for output: %s", confFile)
	cf, err := os.OpenFile(confFile, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		log.Panicf("Unable to open config file to write: %s", err)
	}
	defer cf.Close()

	_, err = io.Copy(cf, bf)
	if err != nil {
		log.Printf("Unable to copy base file into config: %s", err)
	}

	log.Printf("Getting current nodes to populate conman configuration")

	currentNodes := nodes.CurrentNodes()


	log.Printf("Current nodes length: %d", len(currentNodes))
	var ipmiXNames []string
	for _, nci := range currentNodes {
		ipmiXNames = append(ipmiXNames, nci.BmcName)
	}

	passwords := creds.GetPasswordsWithRetries(ipmiXNames, 15, 10)

	for _, nci := range currentNodes {
		if nci.IsIPMI() {
			ipmiXNames = append(ipmiXNames, nci.BmcName)
			creds, ok := passwords[nci.BmcName]
			if !ok {
				log.Printf("No creds record returned for %s", nci.BmcName)
			}
			log.Printf("console name=\"%s\" dev=\"ipmi:%s\" ipmiopts=\"U:%s,P:REDACTED,W:solpayloadsize\"\n",
				nci.NodeName, nci.BmcFqdn, creds.Username)
			output := fmt.Sprintf("console name=\"%s\" dev=\"ipmi:%s\" ipmiopts=\"U:%s,P:%s,W:solpayloadsize\"\n",
				nci.NodeName, nci.BmcFqdn, creds.Username, creds.Password)
			if _, err = cf.WriteString(output); err != nil {
				log.Panic(err)
			}
		} else if nci.IsPassSSH() {
			ipmiXNames = append(ipmiXNames, nci.BmcName)
			creds, ok := passwords[nci.BmcName]
			if !ok {
				log.Printf("No creds record returned for %s", nci.BmcName)
			}
			log.Printf("console name=\"%s\" dev=\"/usr/bin/ssh-pwd-console %s %s REDACTED\"\n",
				nci.NodeName, nci.BmcFqdn, creds.Username)
			output := fmt.Sprintf("console name=\"%s\" dev=\"/home/cjh/work/source/remote-console/scripts/ssh-pwd-console %s %s %s\"\n",
				nci.NodeName, nci.BmcFqdn, creds.Username, creds.Password)
			if _, err = cf.WriteString(output); err != nil {
				log.Panic(err)
			}
		} else if nci.IsKeySSH() {
			log.Printf("console name=\"%s\" dev=\"/usr/bin/ssh-key-console %s\"\n",
				nci.NodeName, nci.NodeName)
			// TODO revert these paths
			output := fmt.Sprintf("console name=\"%s\" dev=\"/home/cjh/work/source/remote-console/scripts/ssh-key-console %s\"\n",
				nci.NodeName, nci.NodeName)
			if _, err = cf.WriteString(output); err != nil {
				log.Panic(err)
			}
		}
	}

	return len(currentNodes) > 0
}

func willUpdateConfig(fp *os.File) bool {
	buff := make([]byte, 50)
	n, err := fp.Read(buff)
	if err != nil || n < 50 {
		log.Printf("Read of base configuration failed. Bytes read: %d, error:%s", n, err)
		return false
	}

	s := string(buff[:n])
	retVal := false
	ss := "UPDATE_CONFIG="
	pos := strings.Index(s, ss)
	if pos > 0 {
		valPos := pos + len(ss)
		retVal = s[valPos] != 'F' && s[valPos] != 'f'
	}
	_, err = fp.Seek(0, 0)
	if err != nil {
		log.Printf("Reset of file pointer to beginning of file failed:%s", err)
	}
	return retVal
}

// SignalConmanTERM sends SIGTERM to running conmand process
func SignalConmanTERM() {
	if command != nil {
		log.Print("Signaling conman with SIGTERM")
		command.Process.Signal(syscall.SIGTERM)
	} else {
		log.Print("Warning: Attempting to signal conman process when nil.")
	}
}

// SignalConmanHUP sends SIGHUP to running conmand process
func SignalConmanHUP() {
	if command != nil {
		log.Print("Signaling conman with SIGHUP")
		command.Process.Signal(syscall.SIGHUP)
	} else {
		log.Print("Warning: Attempting to signal conman process when nil.")

		// TODO fix this up
		// if globalDeps != nil && nodes.DebugOnly && nodes.CurrentNodes != nil && globalDeps.CreateTestLogFile != nil {
		// 	log.Printf("Respinning current log test files...")
		// 	for _, nci := range nodes.CurrentNodes() {
		// 		if nci.IsKeySSH() || nci.IsIPMI() {
		// 			go globalDeps.CreateTestLogFile(nci.NodeName, true)
		// 		}
		// 	}
		// }
	}
}

// LogPipeOutput takes the output of a pipe and logs it
func logPipeOutput(readPipe *io.ReadCloser, desc string) {
	log.Printf("Starting log of conmand %s output", desc)
	er := bufio.NewReader(*readPipe)
	for {
		// read the next line
		line, err := er.ReadString('\n')
		if err != nil {
			log.Printf("Ending %s logging from error:%s", desc, err)
			break
		}
		log.Print(line)
	}
}


func executeConman() {
	log.Print("Starting a new instance of conmand")
	if command != nil {
		log.Print("ERROR: command not nil on entry to executeConman!!")
	}
	command = exec.Command("conmand", "-F", "-v", "-c", confFile)
	cmdStdErr, err := command.StderrPipe()
	if err != nil {
		log.Panicf("Unable to connect to conmand stderr pipe: %s", err)
	}
	cmdStdOut, err := command.StdoutPipe()
	if err != nil {
		log.Panicf("Unable to connect to conmand stdout pipe: %s", err)
	}
	go logPipeOutput(&cmdStdErr, "stderr")
	go logPipeOutput(&cmdStdOut, "stdout")
	log.Print("Starting conmand process")
	if err = command.Start(); err != nil {
		log.Panicf("Unable to start the command: %s", err)
	}
	if err = command.Wait(); err != nil {
		log.Printf("Error from command wait: %s", err)
		time.Sleep(15 * time.Second)
	}
	command = nil
	log.Print("Conmand process has exited")
}

// createTestLogFile is used in debug mode to create and respin fake log files for test nodes.
func createTestLogFile(xname string, respin bool) {
	sleepTime := 1 * time.Second
	filename := fmt.Sprintf("/var/log/conman/console.%s", xname)

	if respin {
		if _, err := os.Stat(filename); err == nil {
			log.Printf("Respinning log file %s, but it exists, so exiting", xname)
			return
		}
	}

	log.Printf("Opening fake log file: %s", filename)
	file1, err := os.OpenFile(filename, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		log.Printf("Error creating file: %s", err)
	}
	log1 := log.New(file1, "", log.LstdFlags)

	var lineCnt int64 = 0
	for {
		log1.Print("Start new write:")
		for i := 0; i < 10; i++ {
			log1.Printf("%s, %d: ASAS:LDL:KJFSADSDfDSLKJYUIYHIUNMNKJHSDFKJHDSLKJDFHLKJDSFHASKAJUHSDAASDLKJFHLKJHADSLKJDSHFLKJDHFSD:OUISDFLKDJFHASLJKFHDKJFH", xname, lineCnt)
			lineCnt++
		}
		time.Sleep(sleepTime)
	}
}
