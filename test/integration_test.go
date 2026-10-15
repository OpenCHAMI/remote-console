// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/openchami/remote-console/internal/console"
	"github.com/openchami/remote-console/internal/nodes"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"
)

const (
	tailMessageTimeout = 2 * time.Minute
	dynamicTestXname   = "x0c0s8b9"
	defaultAuthConfig  = "ADMIN:ADMIN:Administrator;operator:operator_password:Operator;guest:guest_password:ReadOnly"
)

func makeUnique(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// IntegrationTestSuite is the test suite for remote-console integration tests
type IntegrationTestSuite struct {
	suite.Suite
	apiURL           string
	keycloakURL      string
	smdURL           string
	tokensmith       testcontainers.Container
	smdTokenSmithURL string
	containers       map[string]testcontainers.Container
	vaultContainer   testcontainers.Container
	rcsNetwork       *testcontainers.DockerNetwork
	rfNetwork        *testcontainers.DockerNetwork
	consoleNetwork   *testcontainers.DockerNetwork
}

// generateTempSSHKeyPair creates a temporary keypair for testing and returns the private key path and public key string.
func (s *IntegrationTestSuite) generateTempSSHKeyPair() (keyPath string, pubKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate ed25519 key: %w", err)
	}

	// Marshal private key to OpenSSH format
	privPEM, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", "", fmt.Errorf("marshal private key: %w", err)
	}

	// Create key file in temp directory (automatically cleaned up)
	tempDir := s.T().TempDir()
	keyPath = filepath.Join(tempDir, "ssh-test-key")
	privFile, err := os.Create(keyPath)
	if err != nil {
		return "", "", fmt.Errorf("create temp private key: %w", err)
	}
	defer func() {
		if cerr := privFile.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close private key file: %w", cerr)
		}
	}()

	if err := os.Chmod(keyPath, 0600); err != nil {
		return "", "", fmt.Errorf("chmod private key: %w", err)
	}

	// Write properly formatted SSH private key
	if err := pem.Encode(privFile, privPEM); err != nil {
		return "", "", fmt.Errorf("write private key: %w", err)
	}

	// Generate public key in authorized_keys format
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("create ssh public key: %w", err)
	}
	pubKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))

	return keyPath, pubKey, nil
}

// SetupSuite runs once before all tests in the suite
func (s *IntegrationTestSuite) SetupSuite() {
	if testing.Short() {
		s.T().Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	s.containers = make(map[string]testcontainers.Container)
	// Register first so provider logs and cleanup run before shared services and networks.
	s.T().Cleanup(s.cleanupContainers)

	// Create networks
	rcsNet, err := network.New(ctx)
	require.NoError(s.T(), err)
	s.rcsNetwork = rcsNet

	rcsRfNet, err := network.New(ctx)
	require.NoError(s.T(), err)
	s.rfNetwork = rcsRfNet

	rcsConsoleNet, err := network.New(ctx)
	require.NoError(s.T(), err)
	s.consoleNetwork = rcsConsoleNet

	// Start Vault
	s.T().Log("Starting Vault...")
	vaultContainer, err := startVault(ctx, s.rcsNetwork.Name, s.consoleNetwork.Name)
	require.NoError(s.T(), err)
	s.vaultContainer = vaultContainer
	s.containers["vault"] = vaultContainer

	// Enable KV store in Vault
	s.T().Log("Enabling KV store in Vault...")
	err = enableVaultKV(ctx, s.rcsNetwork.Name)
	require.NoError(s.T(), err)

	// Set initial console credentials for SSH key auth nodes
	s.T().Log("Setting initial console credentials in Vault...")
	err = setConsoleCredentials(ctx, s.vaultContainer, "x0c0s1b0", "ADMIN", "")
	require.NoError(s.T(), err)
	err = setConsoleCredentials(ctx, s.vaultContainer, "x0c0s1b0n0", "ADMIN", "")
	require.NoError(s.T(), err)

	// Load SSH keys into Vault (if available)
	s.T().Log("Loading SSH keys into Vault...")
	s.T().Log("Generating temporary SSH key pair for tests")
	sshKeyPath, publicKey, genErr := s.generateTempSSHKeyPair()
	require.NoError(s.T(), genErr)
	err = loadSSHKeysIntoVault(ctx, s.vaultContainer, sshKeyPath)
	require.NoError(s.T(), err)

	// Start Postgres
	s.T().Log("Starting Postgres...")
	postgresContainer, err := startPostgres(ctx, s.rcsNetwork.Name)
	require.NoError(s.T(), err)
	s.containers["postgres"] = postgresContainer

	// Initialize SMD database
	s.T().Log("Initializing SMD database...")
	err = initSMDDatabase(ctx, s.rcsNetwork.Name)
	require.NoError(s.T(), err)

	// Start SMD
	s.T().Log("Starting SMD...")
	smdContainer, err := startSMD(ctx, s.rcsNetwork.Name, s.rfNetwork.Name)
	require.NoError(s.T(), err)
	s.containers["smd"] = smdContainer

	sshPasswordFixture := consoleFixtures["ssh-password"]
	sshKeyFixture := consoleFixtures["ssh-key"]
	ipmiFixture := consoleFixtures["ipmi"]

	// Start Redfish Emulators
	s.T().Log("Starting Redfish emulators...")
	authConfig := defaultAuthConfig
	rfEmulator0, err := startRedfishEmulator(ctx, s.rfNetwork.Name, sshPasswordFixture.nodeID, "ssh", &authConfig)
	require.NoError(s.T(), err)
	s.containers[fmt.Sprintf("rf-%s", sshPasswordFixture.nodeID)] = rfEmulator0

	keyAuthConfig := "ADMIN::Administrator;operator:operator_password:Operator;guest:guest_password:ReadOnly"
	rfEmulator1, err := startRedfishEmulator(ctx, s.rfNetwork.Name, sshKeyFixture.nodeID, "ssh", &keyAuthConfig)
	require.NoError(s.T(), err)
	s.containers[fmt.Sprintf("rf-%s", sshKeyFixture.nodeID)] = rfEmulator1

	keyAuthConfig = "root:root_password:Administrator;operator:operator_password:Operator;guest:guest_password:ReadOnly"
	rfEmulator2, err := startRedfishEmulator(ctx, s.rfNetwork.Name, ipmiFixture.nodeID, "ipmi", &keyAuthConfig)
	require.NoError(s.T(), err)
	s.containers[fmt.Sprintf("rf-%s", ipmiFixture.nodeID)] = rfEmulator2

	// Load Redfish endpoints into SMD
	redfishEndpoints := []redfishEndpoint{
		{
			Host:     sshPasswordFixture.nodeID,
			Username: sshPasswordFixture.username,
			Password: sshPasswordFixture.password,
		},
		{
			Host:     sshKeyFixture.nodeID,
			Username: sshKeyFixture.username,
			Password: sshKeyFixture.password,
		},
		{
			Host:     ipmiFixture.nodeID,
			Username: ipmiFixture.username,
			Password: ipmiFixture.password,
		},
	}

	s.T().Log("Loading Redfish endpoints into SMD...")
	time.Sleep(5 * time.Second) // Give RF emulators time to fully start
	smdAPIURL, err := getSMDAPIURL(ctx, smdContainer)
	require.NoError(s.T(), err)

	err = loadRedfishEndpoints(s.T(), ctx, smdAPIURL, redfishEndpoints)
	require.NoError(s.T(), err)

	// Start SSH password server
	s.T().Log("Starting SSH password server...")
	sshPasswordServer, err := startSSHPasswordServer(ctx, s.consoleNetwork.Name, sshPasswordFixture.nodeID, sshPasswordFixture.username, sshPasswordFixture.password)
	require.NoError(s.T(), err)
	s.containers["ssh-password"] = sshPasswordServer

	// Start SSH key server
	s.T().Log("Starting SSH key server...")
	sshKeyServer, err := startSSHKeyServer(ctx, s.consoleNetwork.Name, sshKeyFixture.nodeID, sshKeyFixture.username, publicKey)
	require.NoError(s.T(), err)
	s.containers["ssh-key"] = sshKeyServer

	// Start IPMI server
	s.T().Log("Starting IPMI server...")
	ipmiServer, err := startIPMIServer(ctx, s.consoleNetwork.Name, ipmiFixture.nodeID)
	require.NoError(s.T(), err)
	s.containers["ipmi"] = ipmiServer

	// Build and start remote-console
	s.T().Log("Starting remote-console...")
	remoteConsole, err := startRemoteConsole(ctx, s.rcsNetwork.Name, s.consoleNetwork.Name)
	require.NoError(s.T(), err)
	s.containers["remote-console"] = remoteConsole

	// Optionally stream remote-console logs to a temp file for debugging
	if os.Getenv("STREAM_REMOTE_CONSOLE_LOGS") == "1" {
		logPath := filepath.Join(os.TempDir(), fmt.Sprintf("remote-console-%d.log", time.Now().UnixNano()))
		s.T().Logf("Streaming remote-console logs to %s", logPath)
		s.streamContainerLogs(ctx, "remote-console", remoteConsole, logPath)
	}

	// Get remote-console endpoint
	s.apiURL, err = s.getRemoteConsoleAPIURL(ctx, remoteConsole)
	require.NoError(s.T(), err)

	s.T().Logf("Remote console API available at: %s", s.apiURL)
	s.T().Log("Waiting for remote-console to discover consoles...")
	s.Require().NoError(s.waitForConsoles(5, 5*time.Minute), "remote-console did not discover expected consoles")
	s.setupAuthentication()
}

// cleanupContainers runs after provider log collection and cleanup.
func (s *IntegrationTestSuite) cleanupContainers() {
	// Create a context with timeout for cleanup
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Clean up containers
	for name, container := range s.containers {
		if err := container.Terminate(cleanupCtx); err != nil {
			s.T().Logf("Warning: failed to terminate container %s: %v", name, err)
		}
	}

	// Clean up networks
	for _, net := range []*testcontainers.DockerNetwork{s.consoleNetwork, s.rfNetwork, s.rcsNetwork} {
		if net == nil {
			continue
		}
		if err := net.Remove(cleanupCtx); err != nil {
			s.T().Logf("Warning: failed to remove network %s: %v", net.Name, err)
		}
	}
}

func (s *IntegrationTestSuite) TestHealthCheck() {
	var healthResponse console.HealthResponse

	resp, err := http.Get(s.apiURL + "/remote-console/health")
	s.Require().NoError(err)
	defer func() {
		if err := resp.Body.Close(); err != nil {
			s.T().Logf("Warning: failed to close response body: %v", err)
		}
	}()
	s.Equal(http.StatusOK, resp.StatusCode)

	err = json.NewDecoder(resp.Body).Decode(&healthResponse)
	s.Require().NoError(err)
	s.Equal("5", healthResponse.NumberConsoles)
}

func (s *IntegrationTestSuite) TestReadinessCheck() {
	resp, err := http.Get(s.apiURL + "/remote-console/readiness")
	s.Require().NoError(err)
	defer func() {
		if err := resp.Body.Close(); err != nil {
			s.T().Logf("Warning: failed to close response body: %v", err)
		}
	}()
	s.Equal(http.StatusNoContent, resp.StatusCode)
}

func (s *IntegrationTestSuite) TestLivenessCheck() {
	resp, err := http.Get(s.apiURL + "/remote-console/liveness")
	s.Require().NoError(err)
	defer func() {
		if err := resp.Body.Close(); err != nil {
			s.T().Logf("Warning: failed to close response body: %v", err)
		}
	}()
	s.Equal(http.StatusNoContent, resp.StatusCode)
}

// sortByID sorts a slice of any type that has an ID field
func sortByID[T any](slice []T, getID func(T) string) {
	sort.Slice(slice, func(i, j int) bool {
		return getID(slice[i]) < getID(slice[j])
	})
}

// TestConsoleNodesDiscovered verifies nodes are discovered from SMD
func (s *IntegrationTestSuite) TestConsoles() {
	resp, err := http.Get(s.apiURL + "/remote-console/consoles")
	s.Require().NoError(err)
	defer func() {
		if err := resp.Body.Close(); err != nil {
			s.T().Logf("Warning: failed to close response body: %v", err)
		}
	}()
	s.Equal(http.StatusOK, resp.StatusCode)

	var consolesResponse console.ConsolesResponse
	err = json.NewDecoder(resp.Body).Decode(&consolesResponse)
	s.Require().NoError(err)

	// 2 SSH password nodes, 2 SSH key nodes, 1 IPMI node
	s.Require().Equal(len(consolesResponse.Consoles), 5, "Expected 5 consoles")

	sshPasswordFixture := consoleFixtures["ssh-password"]
	sshKeyFixture := consoleFixtures["ssh-key"]
	ipmiFixture := consoleFixtures["ipmi"]
	entryCmd := "echo 'Hello n0' && /bin/sh"

	consoles := []nodes.NodeConsoleInfo{
		{
			ID:             sshPasswordFixture.nodeID,
			ConnectionType: "ssh",
			ConnectionHost: sshPasswordFixture.nodeID,
			ConnectionPort: 0,
		},
		{
			ID:                  sshPasswordFixture.nodeID + "n0",
			ConnectionType:      "ssh",
			ConnectionHost:      sshPasswordFixture.nodeID,
			ConnectionPort:      0,
			ConsoleEntryCommand: entryCmd,
		},
		{
			ID:             sshKeyFixture.nodeID,
			ConnectionType: "ssh",
			ConnectionHost: sshKeyFixture.nodeID,
			ConnectionPort: 0,
		},
		{
			ID:                  sshKeyFixture.nodeID + "n0",
			ConnectionType:      "ssh",
			ConnectionHost:      sshKeyFixture.nodeID,
			ConnectionPort:      0,
			ConsoleEntryCommand: entryCmd,
		},
		{
			ID:             ipmiFixture.nodeID,
			ConnectionType: "ipmi",
			ConnectionHost: ipmiFixture.nodeID,
			ConnectionPort: 0,
		},
	}

	// Sort both slices for comparison
	sortByID(consolesResponse.Consoles, func(n nodes.NodeConsoleInfo) string { return n.ID })
	sortByID(consoles, func(n nodes.NodeConsoleInfo) string { return n.ID })

	s.Equal(consoles, consolesResponse.Consoles, "Consoles do not match expected consoles")

}

// tailWebSocketURL constructs the WebSocket URL for tailing console output
func (s *IntegrationTestSuite) tailWebSocketURL(nodeID string, params url.Values) (url.URL, error) {
	parsedURL, err := url.Parse(s.apiURL)
	if err != nil {
		return url.URL{}, fmt.Errorf("failed to parse API URL: %w", err)
	}

	// Start with mode=tail, add any additional parameters
	if params == nil {
		params = url.Values{}
	}
	params.Set("mode", "tail")

	wsURL := url.URL{
		Scheme:   "ws",
		Host:     parsedURL.Host,
		Path:     fmt.Sprintf("/remote-console/consoles/%s", nodeID),
		RawQuery: params.Encode(),
	}

	return wsURL, nil
}

// consoleMessageLogger buffers partial WebSocket chunks and logs complete lines.
type consoleMessageLogger struct {
	t      *testing.T
	buffer string
}

func (s *IntegrationTestSuite) newConsoleMessageLogger() *consoleMessageLogger {
	return &consoleMessageLogger{t: s.T()}
}

func (l *consoleMessageLogger) LogChunk(chunk string) {
	l.buffer += chunk
	for {
		idx := strings.IndexByte(l.buffer, '\n')
		if idx == -1 {
			return
		}
		line := strings.TrimSuffix(l.buffer[:idx], "\r")
		l.t.Logf("Console: %s", line)
		l.buffer = l.buffer[idx+1:]
	}
}

// readWebSocketMessages reads messages from the WebSocket connection until it times out or encounters an error.
func (s *IntegrationTestSuite) readWebSocketMessages(wsConn *websocket.Conn, timeout time.Duration) (string, error) {
	if err := wsConn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", fmt.Errorf("set read deadline: %w", err)
	}

	var output strings.Builder
	logger := s.newConsoleMessageLogger()
	messageCount := 0
	start := time.Now()
	for {
		_, message, err := wsConn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return output.String(), nil
			}
			return output.String(), fmt.Errorf("websocket read ended after %d messages (%s elapsed): %w", messageCount, time.Since(start), err)
		}

		msgStr := string(message)
		logger.LogChunk(msgStr)
		output.WriteString(msgStr)
		messageCount++
	}
}

// readWebSocketUntil reads messages from the WebSocket connection until the specified search string is found or it times out.
func (s *IntegrationTestSuite) readWebSocketUntil(wsConn *websocket.Conn, searchString string, timeout time.Duration) (string, error) {
	if err := wsConn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", fmt.Errorf("set read deadline: %w", err)
	}

	var output strings.Builder
	logger := s.newConsoleMessageLogger()
	for {
		_, message, err := wsConn.ReadMessage()
		if err != nil {
			s.T().Logf("WebSocket read ended: %v", err)
			return output.String(), fmt.Errorf("websocket read error while searching for %q: %w", searchString, err)
		}

		msgStr := string(message)
		logger.LogChunk(msgStr)
		output.WriteString(msgStr)
		if strings.Contains(output.String(), searchString) {
			return output.String(), nil
		}
	}
}

// readNWebSocketMessages reads a specific number of messages from the WebSocket connection.
func (s *IntegrationTestSuite) readNWebSocketMessages(wsConn *websocket.Conn, count int, timeout time.Duration) (string, error) {
	if err := wsConn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", fmt.Errorf("set read deadline: %w", err)
	}

	var output strings.Builder
	for i := range count {
		_, message, err := wsConn.ReadMessage()
		if err != nil {
			s.T().Logf("WebSocket read ended after %d of %d messages: %v", i, count, err)
			return output.String(), fmt.Errorf("failed to read message %d of %d: %w", i+1, count, err)
		}
		output.WriteString(string(message))
	}

	return output.String(), nil
}

// dialWebSocket dials a WebSocket connection to the specified URL.
func (s *IntegrationTestSuite) dialWebSocket(wsURL url.URL) (*websocket.Conn, *http.Response, error) {
	wsConn, resp, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
	if err != nil {
		return nil, resp, err
	}
	return wsConn, resp, nil
}

// fileLogConsumer is a LogConsumer that writes logs to a file
type fileLogConsumer struct {
	writer io.Writer
	mu     sync.Mutex
}

// Accept writes the log content to the file
func (c *fileLogConsumer) Accept(log testcontainers.Log) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writer != nil {
		if _, err := c.writer.Write(log.Content); err != nil {
			// We cannot use s.T().Logf from here, so best effort print to stderr
			fmt.Fprintf(os.Stderr, "fileLogConsumer write error: %v\n", err)
		}
	}
}

// streamContainerLogs streams the logs of the specified container to a file at destPath, used for debugging
func (s *IntegrationTestSuite) streamContainerLogs(ctx context.Context, name string, container testcontainers.Container, destPath string) {
	go func() {
		file, err := os.Create(destPath)
		if err != nil {
			s.T().Logf("Warning: unable to create log file %s: %v", destPath, err)
			return
		}

		logConsumer := &fileLogConsumer{writer: file}
		container.FollowOutput(logConsumer)

		if err := container.StartLogProducer(ctx); err != nil {
			s.T().Logf("Warning: unable to start log producer for %s: %v", name, err)
			if closeErr := file.Close(); closeErr != nil {
				s.T().Logf("Warning: failed to close log file %s: %v", destPath, closeErr)
			}
			return
		}

		if errCh := container.GetLogProductionErrorChannel(); errCh != nil {
			if err := <-errCh; err != nil && err != context.Canceled && err != context.DeadlineExceeded {
				s.T().Logf("Warning: log producer for %s stopped with error: %v", name, err)
			}
		}

		if closeErr := file.Close(); closeErr != nil {
			s.T().Logf("Warning: failed to close log file %s: %v", destPath, closeErr)
		}
	}()
}

// waitForConsoles waits until the expected number of consoles are discovered or times out
func (s *IntegrationTestSuite) waitForConsoles(expected int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.apiURL + "/remote-console/consoles")
		if err == nil {
			var consolesResp console.ConsolesResponse
			if decodeErr := json.NewDecoder(resp.Body).Decode(&consolesResp); decodeErr == nil {
				if len(consolesResp.Consoles) == expected {
					if err := resp.Body.Close(); err != nil {
						s.T().Logf("Warning: failed to close response body: %v", err)
					}
					s.T().Logf("remote-console discovered %d consoles", expected)
					return nil
				}
			}
			if err := resp.Body.Close(); err != nil {
				s.T().Logf("Warning: failed to close response body: %v", err)
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %d consoles", expected)
}

// waitForConsoleID waits until a console with the specified nodeID is discovered or times out
func (s *IntegrationTestSuite) waitForConsoleID(nodeID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.apiURL + "/remote-console/consoles")
		if err == nil {
			var consolesResp console.ConsolesResponse
			if decodeErr := json.NewDecoder(resp.Body).Decode(&consolesResp); decodeErr == nil {
				for _, consoleInfo := range consolesResp.Consoles {
					if consoleInfo.ID == nodeID {
						if err := resp.Body.Close(); err != nil {
							s.T().Logf("Warning: failed to close response body: %v", err)
						}
						return nil
					}
				}
			}
			if err := resp.Body.Close(); err != nil {
				s.T().Logf("Warning: failed to close response body: %v", err)
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for console %s", nodeID)
}

// waitForConsoleRemoval waits until a console with the specified nodeID is removed or times out
func (s *IntegrationTestSuite) waitForConsoleRemoval(nodeID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.apiURL + "/remote-console/consoles")
		if err == nil {
			var consolesResp console.ConsolesResponse
			if decodeErr := json.NewDecoder(resp.Body).Decode(&consolesResp); decodeErr == nil {
				found := false

				for _, consoleInfo := range consolesResp.Consoles {
					if consoleInfo.ID == nodeID {
						found = true
						break
					}
				}
				if !found {
					if err := resp.Body.Close(); err != nil {
						s.T().Logf("Warning: failed to close response body: %v", err)
					}
					return nil
				}
			}
			if err := resp.Body.Close(); err != nil {
				s.T().Logf("Warning: failed to close response body: %v", err)
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for console %s removal", nodeID)
}

// getRemoteConsoleAPIURL constructs the API URL for a container exposing port 26776
func (s *IntegrationTestSuite) getRemoteConsoleAPIURL(ctx context.Context, container testcontainers.Container) (string, error) {
	host, err := container.Host(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get container host: %w", err)
	}
	port, err := container.MappedPort(ctx, "26776")
	if err != nil {
		return "", fmt.Errorf("failed to get container mapped port: %w", err)
	}
	return fmt.Sprintf("http://%s:%s", host, port.Port()), nil
}

func (s *IntegrationTestSuite) TestDynamicConsoleDiscovery() {
	ctx := context.Background()
	newNodeID := dynamicTestXname

	// Start new Redfish emulator and SSH console
	s.T().Logf("Starting dynamic Redfish emulator and SSH console for %s", newNodeID)
	authConfig := defaultAuthConfig
	rfContainer, err := startRedfishEmulator(ctx, s.rfNetwork.Name, newNodeID, "ssh", &authConfig)
	s.Require().NoError(err)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := rfContainer.Terminate(cleanupCtx); err != nil {
			s.T().Logf("Warning: failed to terminate dynamic Redfish emulator %s: %v", newNodeID, err)
		}
	}()

	sshContainer, err := startSSHPasswordServer(ctx, s.consoleNetwork.Name, newNodeID, "ADMIN", "ADMIN")
	s.Require().NoError(err)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := sshContainer.Terminate(cleanupCtx); err != nil {
			s.T().Logf("Warning: failed to terminate dynamic SSH container %s: %v", newNodeID, err)
		}
	}()

	smdAPIURL, err := getSMDAPIURL(ctx, s.containers["smd"])
	s.Require().NoError(err)

	// Register new Redfish endpoint in SMD
	err = loadRedfishEndpoint(s.T(), ctx, smdAPIURL, redfishEndpoint{
		Host:     newNodeID,
		Username: "ADMIN",
		Password: "ADMIN",
	})
	s.Require().NoError(err, "failed to register dynamic Redfish endpoint")

	// Wait for remote-console to discover new console
	s.Require().NoError(s.waitForConsoleID(newNodeID, 3*time.Minute), "remote-console did not detect new console")

	// Connect to new console and verify we can run commands
	wsConn, resp, err := s.connectInteractiveConsole(newNodeID, ":~$ ", 90*time.Second)
	s.Require().NoError(err)
	defer func() {
		if err := resp.Body.Close(); err != nil {
			s.T().Logf("Warning: failed to close response body: %v", err)
		}
		if err := wsConn.Close(); err != nil {
			s.T().Logf("Warning: failed to close websocket: %v", err)
		}
	}()

	cmd := "hostname\r"
	err = wsConn.WriteMessage(websocket.TextMessage, []byte(cmd))
	s.Require().NoError(err, "Error sending test message to console")

	expectedHostLine := newNodeID + "\r\n"
	hostnameOutput, err := s.readWebSocketUntil(wsConn, expectedHostLine, 90*time.Second)
	s.Require().NoError(err, "Expected hostname output from console")
	s.Require().True(strings.Contains(hostnameOutput, expectedHostLine),
		"Expected hostname command output in console output; got %q", hostnameOutput)

	s.T().Log("Removing dynamic console registration")
	smdAPIURL, err = getSMDAPIURL(ctx, s.containers["smd"])
	s.Require().NoError(err)
	err = deleteRedfishEndpoint(s.T(), ctx, smdAPIURL, newNodeID)
	s.Require().NoError(err, "failed to remove dynamic Redfish endpoint")
	s.Require().NoError(s.waitForConsoleRemoval(newNodeID, 3*time.Minute), "remote-console did not drop dynamic console")

	// Try to connect again, should fail immediately (no retries needed)
	wsURL, err := s.tailWebSocketURL(newNodeID, nil)
	s.Require().NoError(err)

	_, resp, err = s.dialWebSocket(wsURL)
	s.Require().Error(err, "Expected error connecting to removed console")
	if resp != nil {
		s.T().Logf("Console removal connection response status: %d", resp.StatusCode)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				s.T().Logf("Warning: failed to close response body: %v", err)
			}
		}()
	}

}

// TestIntegrationSuite runs the integration test suite
func TestIntegrationSuite(t *testing.T) {
	suite.Run(t, new(IntegrationTestSuite))
}

const (
	oidcPath      = "/realms/rcs-auth-test/protocol/openid-connect"
	jwksURL       = "http://keycloak:8080" + oidcPath + "/certs"
	tokenURL      = "http://keycloak:8080" + oidcPath + "/token"
	inventoryPath = "/hsm/v2/Inventory/ComponentEndpoints"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

// newContainer collects failure logs before its single cleanup owner terminates it.
func newContainer(t *testing.T, req testcontainers.ContainerRequest) testcontainers.Container {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	testcontainers.CleanupContainer(t, ctr)
	if ctr != nil {
		t.Cleanup(func() {
			if t.Failed() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				logs, err := ctr.Logs(ctx)
				if err == nil {
					defer func() { _ = logs.Close() }()
					data, _ := io.ReadAll(logs)
					t.Logf("%s logs:\n%s", req.Image, data)
				}
			}
		})
	}
	require.NoError(t, err)
	return ctr
}

func endpoint(t *testing.T, ctr testcontainers.Container, port string) string {
	t.Helper()
	result, err := ctr.PortEndpoint(context.Background(), port, "http")
	require.NoError(t, err)
	return result
}

func (s *IntegrationTestSuite) setupAuthentication() {
	t := s.T()
	kc := newContainer(t, testcontainers.ContainerRequest{
		Image: "quay.io/keycloak/keycloak:26.7.4", Cmd: []string{"start-dev", "--import-realm"},
		Env:      map[string]string{"KC_HOSTNAME": "http://keycloak:8080"},
		Files:    []testcontainers.ContainerFile{{HostFilePath: "testdata/realm.json", ContainerFilePath: "/opt/keycloak/data/import/realm.json", FileMode: 0644}},
		Networks: []string{s.rcsNetwork.Name}, NetworkAliases: map[string][]string{s.rcsNetwork.Name: {"keycloak"}},
		ExposedPorts: []string{"8080/tcp"}, WaitingFor: wait.ForHTTP(oidcPath + "/certs").WithPort("8080/tcp").WithStartupTimeout(5 * time.Minute),
	})
	s.keycloakURL = endpoint(t, kc, "8080/tcp")

	s.smdURL = s.startSMD("smd-auth", jwksURL)
	s.startTokenSmith()
	s.startTokenSmithSMD()
}

func smdEnv() map[string]string {
	return map[string]string{"SMD_DBHOST": "postgres", "SMD_DBPORT": "5432", "SMD_DBNAME": "hmsds", "SMD_DBUSER": "postgres", "SMD_DBPASS": "postgres", "SMD_DBOPTS": "sslmode=disable", "SMD_WVAULT": "false", "SMD_RVAULT": "false", "SMD_SLS_HOST": "", "SMD_HBTD_HOST": "", "ENABLE_DISCOVERY": "false"}
}

func (s *IntegrationTestSuite) startSMD(alias, jwks string) string {
	env := smdEnv()
	env["SMD_JWKS_URL"] = jwks
	ctr := newContainer(s.T(), testcontainers.ContainerRequest{Image: smdImage, Env: env, Networks: []string{s.rcsNetwork.Name}, NetworkAliases: map[string][]string{s.rcsNetwork.Name: {alias}}, ExposedPorts: []string{"27779/tcp"}, WaitingFor: wait.ForHTTP("/hsm/v2/service/ready").WithPort("27779/tcp").WithStartupTimeout(time.Minute)})
	return endpoint(s.T(), ctr, "27779/tcp")
}

func (s *IntegrationTestSuite) addConsoleEndpoint(xname string) {
	t := s.T()
	ctx := context.Background()
	authConfig := defaultAuthConfig
	ctr, err := startRedfishEmulator(ctx, s.rfNetwork.Name, xname, "ssh", &authConfig)
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err)
	smdAPIURL, err := getSMDAPIURL(ctx, s.containers["smd"])
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, deleteRedfishEndpoint(t, ctx, smdAPIURL, xname))
		require.NoError(t, s.waitForConsoleRemoval(xname, time.Minute))
		require.NoError(t, s.waitForConsoleRemoval(xname+"n0", time.Minute))
	})
	require.NoError(t, loadRedfishEndpoint(t, ctx, smdAPIURL, redfishEndpoint{
		Host: xname, Username: "ADMIN", Password: "ADMIN",
	}))
}

func oauthEnv() map[string]string {
	return map[string]string{"RCS_OAUTH2_CLIENT_ID": "rcs-service", "RCS_OAUTH2_CLIENT_SECRET": "rcs-service-test-secret", "RCS_OAUTH2_TOKEN_URL": tokenURL, "RCS_OAUTH2_SCOPES": "rcs-test"}
}

// consoleImage follows replacements made by the log-rotation test.
func (s *IntegrationTestSuite) consoleImage() string {
	info, err := s.containers["remote-console"].Inspect(context.Background())
	require.NoError(s.T(), err)
	return info.Config.Image
}

func (s *IntegrationTestSuite) startConsole(extra map[string]string, args ...string) string {
	env := map[string]string{
		"RCS_JWKS_URL": jwksURL, "RCS_SMD_URL": "http://smd-auth:27779", "RCS_NEW_NODE_LOOKUP": "1", "RCS_CREDS_MONITOR_INTERVAL": "3600",
		"RCS_CREDS_SECURE_STORAGE_ADAPTER": "local", "RCS_CREDS_LOCAL_STORE_FILE_PATH": "/tmp/creds", "RCS_CREDS_LOCAL_STORE_KEY": "test-only",
		"RCS_CONSOLE_LOGS_BASE_PATH": "/tmp/logs", "RCS_CONSOLE_LOGS_BACKUP_PATH": "/tmp/old", "RCS_CONMAN_PID_FILE_PATH": "/tmp/conman.pid",
	}
	maps.Copy(env, oauthEnv())
	maps.Copy(env, extra)
	ctr := newContainer(s.T(), testcontainers.ContainerRequest{Image: s.consoleImage(), Env: env, Cmd: args, Networks: []string{s.rcsNetwork.Name}, ExposedPorts: []string{"26776/tcp"}, WaitingFor: wait.ForHTTP("/remote-console/liveness").WithPort("26776/tcp").WithStatusCodeMatcher(func(status int) bool { return status == 204 }).WithStartupTimeout(time.Minute)})
	return endpoint(s.T(), ctr, "26776/tcp")
}

func request(t *testing.T, method, address, token string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, address, bytes.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, data
}

func (s *IntegrationTestSuite) fetchToken(client string) string {
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {"rcs-test"}}
	req, err := http.NewRequest(http.MethodPost, s.keycloakURL+oidcPath+"/token", strings.NewReader(form.Encode()))
	require.NoError(s.T(), err)
	secret := client + "-secret"
	if client == "rcs-service" {
		secret = "rcs-service-test-secret"
	}
	req.SetBasicAuth(client, secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	require.NoError(s.T(), err)
	defer func() { require.NoError(s.T(), resp.Body.Close()) }()
	require.Equal(s.T(), 200, resp.StatusCode)
	var token struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(s.T(), json.NewDecoder(resp.Body).Decode(&token))
	require.NotEmpty(s.T(), token.AccessToken)
	return token.AccessToken
}

func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("timed out waiting for authentication state change")
}

func (s *IntegrationTestSuite) assertStartupFailure(env map[string]string, message string) {
	t := s.T()
	t.Helper()
	ctr := newContainer(t, testcontainers.ContainerRequest{
		Image: s.consoleImage(), Env: env, Networks: []string{s.rcsNetwork.Name},
		WaitingFor: wait.ForExit().WithExitTimeout(30 * time.Second),
	})
	state, err := ctr.State(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, state.ExitCode)
	logs, err := ctr.Logs(context.Background())
	require.NoError(t, err)
	defer func() { require.NoError(t, logs.Close()) }()
	data, err := io.ReadAll(logs)
	require.NoError(t, err)
	require.Contains(t, string(data), message)
}
