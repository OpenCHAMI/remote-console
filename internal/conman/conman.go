// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
// Copyright © 2025 Hewlett Packard Enterprise Development LP
//
// SPDX-License-Identifier: MIT

// This file contains the interfaces and dependency injection points for conman management.

package conman

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"text/template"

	"github.com/Cray-HPE/hms-compcredentials"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
)

type ConmanService struct {
	config  ConmanConfig
	mutex   sync.Mutex
	command *exec.Cmd
}

func NewConmanService(config ConmanConfig) *ConmanService {
	return &ConmanService{config: config}
}

func (cs *ConmanService) ConfigureConman(nodeMap map[string]*nodes.NodeConsoleInfo, passwords map[string]compcredentials.CompCredentials) (bool, error) {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()

	return cs.updateConfigFile(nodeMap, passwords, true)
}

func generateBaseConfig(config ConmanConfig) ([]byte, error) {

	// Read template file
	slog.Debug("Opening base configuration file", "path", config.BaseConfFilePath)
	tmplContent, err := os.ReadFile(config.BaseConfFilePath)
	if err != nil {
		return nil, fmt.Errorf("error opening base config template: %w", err)
	}

	// Parse and execute template
	tmpl, err := template.New("conman").Parse(string(tmplContent))
	if err != nil {
		return nil, fmt.Errorf("error templating base config: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, config); err != nil {
		return nil, fmt.Errorf("error templating base config: %w", err)
	}

	return buf.Bytes(), nil
}

func willUpdateConfig(baseConfig []byte) bool {
	// if the first line of the base configuration file has '# UPDATE_CONFIG=FALSE'
	// then bail on the update
	// NOTE: only reading first 50 bytes of file, should be at least that many
	//  present if this is a valid base configuration file and don't need to read more.
	const key = "UPDATE_CONFIG="
	configStr := string(baseConfig)

	keyPosition := strings.Index(configStr, key)
	if keyPosition == -1 {
		return false
	}

	valuePosition := keyPosition + len(key)
	if valuePosition >= len(configStr) {
		slog.Warn("Base configuration missing UPDATE_CONFIG value")
		return false
	}

	value := configStr[valuePosition]
	return value != 'F' && value != 'f'
}

func generateIPMIConsoleConfig(nci *nodes.NodeConsoleInfo, creds compcredentials.CompCredentials) string {
	slog.Debug("Configuring IPMI console", "nodeID", nci.ID, "host", nci.ConnectionHost, "username", creds.Username)
	return fmt.Sprintf("console name=\"%s\" dev=\"ipmi:%s\" ipmiopts=\"U:%s,P:%s,W:solpayloadsize\"\n",
		nci.ID, nci.ConnectionHost, creds.Username, creds.Password)
}

func (cs *ConmanService) updateConfigFile(nodeMap map[string]*nodes.NodeConsoleInfo, passwords map[string]compcredentials.CompCredentials, forceUpdate bool) (bool, error) {
	slog.Info("Updating conman configuration file")

	bs, err := generateBaseConfig(cs.config)
	if err != nil {
		return false, fmt.Errorf("unable to template base config file: %w", err)
	}

	if !forceUpdate && !willUpdateConfig(bs) {
		slog.Debug("Skipping update due to base config file flag")
		return false, nil
	}

	slog.Debug("Opening conman configuration file for output", "path", cs.config.ConfFilePath)
	cf, err := os.OpenFile(cs.config.ConfFilePath, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return false, fmt.Errorf("unable to open config file to write: %w", err)
	}
	defer func() {
		if err := cf.Close(); err != nil {
			slog.Warn("Failed to close conman config file", "path", cs.config.ConfFilePath, "error", err)
		}
	}()

	_, err = cf.Write(bs)
	if err != nil {
		return false, fmt.Errorf("unable to write base config into file: %w", err)
	}

	slog.Info("Populating conman configuration with nodes", "nodeCount", len(nodeMap))

	consoles := make([]string, 0, len(nodeMap))
	ipmiCount := 0

	for _, nci := range nodeMap {
		switch nci.ConnectionType {
		case nodes.IPMI:
			creds, ok := passwords[nci.ID]
			if !ok {
				// No entry for this node yet. Leaving it out beats writing a
				// console with empty credentials, which conmand would accept and
				// then fail to authenticate with over and over.
				slog.Warn("No credentials found for node; leaving it out of the conman config", "nodeID", nci.ID)
				continue
			}
			consoles = append(consoles, generateIPMIConsoleConfig(nci, creds))
			ipmiCount++

		case nodes.SSH:
			// Callers filter to IPMI nodes before calling, so reaching this is a
			// caller bug.
			slog.Error("SSH node passed to conman config; skipping", "nodeID", nci.ID)
		}
	}

	// Sort consoles for consistent output
	sort.Strings(consoles)
	for _, output := range consoles {
		if _, err = cf.WriteString(output); err != nil {
			return false, fmt.Errorf("unable to write console entry into file: %w", err)
		}
	}

	// Only report hasNodes=true for IPMI nodes that were actually written.
	// When that leaves nothing, runConman waits and tries again rather than
	// starting conmand on an empty config.
	return ipmiCount > 0, nil
}

// SignalConmanTERM sends SIGTERM to running conmand process
func (cs *ConmanService) SignalConmanTERM() error {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()

	if cs.command == nil || cs.command.Process == nil {
		slog.Debug("Conmand is not running, skipping SIGTERM")
		return nil
	}

	slog.Info("Signaling conman with SIGTERM")
	if err := cs.command.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("failed to signal conman with SIGTERM: %w", err)
	}
	return nil
}

// SignalConmanHUP sends SIGHUP to running conmand process
func (cs *ConmanService) SignalConmanHUP() error {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()

	if cs.command == nil || cs.command.Process == nil {
		slog.Debug("Conmand is not running, skipping SIGHUP")
		return nil
	}

	slog.Info("Signaling conman with SIGHUP")
	if err := cs.command.Process.Signal(syscall.SIGHUP); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("failed to signal conman with SIGHUP: %w", err)
	}
	return nil
}

// logPipeOutput takes the output of a pipe and logs it
func logPipeOutput(readPipe io.ReadCloser, desc string) {
	slog.Debug("Starting conmand pipe logging", "pipe", desc)
	er := bufio.NewReader(readPipe)
	for {
		// read the next line
		line, err := er.ReadString('\n')
		if err != nil {
			slog.Debug("Ending pipe logging", "pipe", desc, "error", err)
			break
		}
		slog.Debug("conmand output", "pipe", desc, "output", line)
	}
}

func (cs *ConmanService) startConman() (*exec.Cmd, io.ReadCloser, io.ReadCloser, error) {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()

	if cs.command != nil {
		return nil, nil, nil, fmt.Errorf("command not nil on entry to executeConman")
	}

	command := exec.Command("conmand", "-F", "-v", "-c", cs.config.ConfFilePath)
	cmdStdErr, err := command.StderrPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to connect to conmand stderr pipe: %w", err)
	}
	cmdStdOut, err := command.StdoutPipe()
	if err != nil {
		_ = cmdStdErr.Close()
		return nil, nil, nil, fmt.Errorf("unable to connect to conmand stdout pipe: %w", err)
	}

	slog.Info("Starting conmand process")
	if err = command.Start(); err != nil {
		_ = cmdStdErr.Close()
		_ = cmdStdOut.Close()
		return nil, nil, nil, fmt.Errorf("unable to start command: %w", err)
	}

	cs.command = command
	return command, cmdStdErr, cmdStdOut, nil
}

// ExecuteConman starts conmand and waits for it to exit
func (cs *ConmanService) ExecuteConman() error {
	slog.Info("Starting new instance of conmand")

	command, cmdStdErr, cmdStdOut, err := cs.startConman()
	if err != nil {
		return err
	}

	go logPipeOutput(cmdStdErr, "stderr")
	go logPipeOutput(cmdStdOut, "stdout")

	if err = command.Wait(); err != nil {
		slog.Error("Conmand process exited with error", "error", err)
	}

	cs.mutex.Lock()
	if cs.command == command {
		cs.command = nil
	}
	cs.mutex.Unlock()
	slog.Info("Conmand process has exited")

	return nil
}
