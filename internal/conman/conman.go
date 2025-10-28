// MIT License
// (C) Copyright 2025 Hewlett Packard Enterprise Development LP
//
// This file contains the interfaces and dependency injection points for conman management.

package conman

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"
	"sort"


	"github.com/Cray-HPE/hms-compcredentials"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
	"github.com/OpenCHAMI/remote-console/internal/types"
)

var conmanMutex = &sync.Mutex{}
var command *exec.Cmd = nil

type ConmanConfig struct {
	DebugOnly        bool
	BaseConfFilePath string
	ConfFilePath     string
	LogFilesPath      string
	PidFilePath      string
	ConsoleScriptsPath string
}

func DefaultConmanConfig() ConmanConfig {
	return ConmanConfig{
		DebugOnly: false,
		BaseConfFilePath: "/app/conman_base.conf.tmpl",
		ConfFilePath:     "/etc/conman.conf",
		LogFilesPath:      "/var/log/conman",
		PidFilePath:      "/var/run/conman.pid",
		ConsoleScriptsPath: "/usr/bin",
	}
}


func ConfigureConman(config ConmanConfig, nodes map[string]*types.NodeConsoleInfo, passwords  map[string]compcredentials.CompCredentials) (bool, error) {
	conmanMutex.Lock()
	defer conmanMutex.Unlock()

	return updateConfigFile(config, nodes, passwords, true)
}

func generateBaseConfig(config ConmanConfig) ([]byte, error) {

	// Read template file
	log.Printf("Opening base configuration file: %s", config.BaseConfFilePath)
    tmplContent, err := os.ReadFile(config.BaseConfFilePath)
    if err != nil {
        return nil, fmt.Errorf("error opening base config template: %s", err)
    }

    // Parse and execute template
    tmpl, err := template.New("conman").Parse(string(tmplContent))
    if err != nil {
        return nil, fmt.Errorf("error templating base config: %s", err)
    }

    var buf bytes.Buffer
    if err := tmpl.Execute(&buf, config); err != nil {
        return nil, fmt.Errorf("error templating base config: %s", err)
    }

	return buf.Bytes(), nil
}

func updateConfigFile(config ConmanConfig, nodes map[string]*types.NodeConsoleInfo, passwords  map[string]compcredentials.CompCredentials, forceUpdate bool) (bool, error) {
	log.Print("Updating the configuration file")
	
	bs, err := generateBaseConfig(config)
	if err != nil {
		return false, fmt.Errorf("Unable to template base config file: %v", err)
	}

	if !forceUpdate && !willUpdateConfig(bs) {
		log.Print("Skipping update due to base config file flag")
		return false, nil
	}

	log.Printf("Opening conman configuration file for output: %s", config.ConfFilePath)
	cf, err := os.OpenFile(config.ConfFilePath, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return false , fmt.Errorf("Unable to open config file to write: %v", err)
	}
	defer cf.Close()

	_, err = cf.Write(bs)
	if err != nil {
		return false, fmt.Errorf("Unable to write base config into file: %v", err)
	}

	log.Printf("Getting current nodes to populate conman configuration")

	consoles := make([]string, 0, len(nodes))

	for _, nci := range nodes {
		if nci.IsIPMI() {
			creds, ok := passwords[nci.BmcName]
			if !ok {
				log.Printf("No creds record returned for %s", nci.BmcName)
			}
			log.Printf("console name=\"%s\" dev=\"ipmi:%s\" ipmiopts=\"U:%s,P:REDACTED,W:solpayloadsize\"\n",
				nci.NodeName, nci.BmcFqdn, creds.Username)
			output := fmt.Sprintf("console name=\"%s\" dev=\"ipmi:%s\" ipmiopts=\"U:%s,P:%s,W:solpayloadsize\"\n",
				nci.NodeName, nci.BmcFqdn, creds.Username, creds.Password)
			consoles = append(consoles, output)
		} else if nci.IsPassSSH() {
			creds, ok := passwords[nci.BmcName]
			if !ok {
				log.Printf("No creds record returned for %s", nci.BmcName)
			}
			log.Printf("console name=\"%s\" dev=\"%s/ssh-pwd-console %s %s REDACTED\"\n",
				nci.NodeName, config.ConsoleScriptsPath, nci.BmcFqdn, creds.Username)
			output := fmt.Sprintf("console name=\"%s\" dev=\"%s/ssh-pwd-console %s %s %s\"\n",
				nci.NodeName, config.ConsoleScriptsPath, nci.BmcFqdn, creds.Username, creds.Password)
			consoles = append(consoles, output)
		} else if nci.IsKeySSH() {
			log.Printf("console name=\"%s\" dev=\"%s/ssh-key-console %s\"\n",
				nci.NodeName, config.ConsoleScriptsPath, nci.NodeName)
			output := fmt.Sprintf("console name=\"%s\" dev=\"%s/ssh-key-console %s\"\n",
				nci.NodeName, config.ConsoleScriptsPath, nci.NodeName)
			consoles = append(consoles, output)
		}
	}

	// Sort consoles for consistent output
	sort.Strings(consoles)
	for _, output := range consoles {
		if _, err = cf.WriteString(output); err != nil {
			return false, fmt.Errorf("Unable to write console entry into file: %v", err)
		}
	}

	return len(nodes) > 0, nil
}

func willUpdateConfig(baseConfig []byte) bool {
	buff := make([]byte, 50)
	n := len(baseConfig)
	if n < 50 {
		log.Printf("Base configuration truncated")
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
func SignalConmanHUP(config ConmanConfig) {
	if command != nil {
		log.Print("Signaling conman with SIGHUP")
		command.Process.Signal(syscall.SIGHUP)
	} else {
		log.Print("Warning: Attempting to signal conman process when nil.")

		if config.DebugOnly && nodes.CurrentNodes() != nil  {
			log.Printf("Respinning current log test files...")
			for _, nci := range nodes.CurrentNodes() {
				go createTestLogFile(config, nci.NodeName, true)
			}
		}
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

func ExecuteConman(config ConmanConfig) error {
	log.Print("Starting a new instance of conmand")
	if command != nil {
		return fmt.Errorf("command not nil on entry to executeConman!!")
	}
	command = exec.Command("conmand", "-F", "-v", "-c", config.ConfFilePath)
	cmdStdErr, err := command.StderrPipe()
	if err != nil {
		return fmt.Errorf("Unable to connect to conmand stderr pipe: %s", err)
	}
	cmdStdOut, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("Unable to connect to conmand stdout pipe: %s", err)
	}
	go logPipeOutput(&cmdStdErr, "stderr")
	go logPipeOutput(&cmdStdOut, "stdout")
	log.Print("Starting conmand process")
	if err = command.Start(); err != nil {
		return fmt.Errorf("Unable to start the command: %s", err)
	}
	if err = command.Wait(); err != nil {
		log.Printf("Error from command wait: %s", err)
		time.Sleep(15 * time.Second)
	}
	command = nil
	log.Print("Conmand process has exited")

	return nil
}

// DEBUG Function to create and add to a fake log file
func createTestLogFile(config ConmanConfig, xname string, respin bool) {
	// NOTE: this function is only for use in a debug environment where there
	//  are no real console connections present.
	sleepTime := 1 * time.Second
	filename := fmt.Sprintf("%s/console.%s", config.LogFilesPath, xname)


	// Ff respin is true, only create if the file is not present - meant to
	// be used when a logrotation has moved the original file and we need to
	// create a new one back at the original location.  If the file is still there
	// we do not need to re-create.
	if respin {
		if _, err := os.Stat(filename); err == nil {
			log.Printf("Respinning log file %s, but it exists, so exiting", xname)
			return
		}
	}

	// create and start the log file
	log.Printf("Opening fake log file: %s", filename)
	file1, err := os.OpenFile(filename, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		log.Printf("Error creating file: %s", err)
	}
	log1 := log.New(file1, "", log.LstdFlags)

	// start a loop that runs forever to write to the log files
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
