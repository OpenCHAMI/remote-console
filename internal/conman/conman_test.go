package conman

import (
	"os"
	"path/filepath"
	"testing"
	"strings"

	"github.com/Cray-HPE/hms-compcredentials"
	"github.com/stretchr/testify/require"

	"github.com/OpenCHAMI/remote-console/internal/types"
)


func TestGenerateBaseConfig(t *testing.T) {
	baseDir := "/tmp/conman_test"
	
	config := DefaultConmanConfig()
	config.BaseConfFilePath = "../../scripts/conman.conf.tmpl"
	config.ConfFilePath = filepath.Join(baseDir, "conman.conf")
	config.LogFilesPath = filepath.Join(baseDir, "logs")
	config.PidFilePath = filepath.Join(baseDir, "conman.pid")
	
	baseConfig, err := generateBaseConfig(config)
	require.NoError(t, err)
	require.NotEmpty(t, baseConfig)
	
	expected := `# UPDATE_CONFIG=TRUE
SERVER keepalive=ON
SERVER logdir="/tmp/conman_test/logs"
SERVER logfile="conman.log"
SERVER loopback=ON
SERVER pidfile="/tmp/conman_test/conman.pid"
SERVER resetcmd="powerman -0 %N; sleep 3; powerman -1 %N"
SERVER timestamp=1h
GLOBAL seropts="115200,8n1"
GLOBAL log="conman/console.%N"
GLOBAL logopts="sanitize,timestamp"
`

	require.Equal(t, []byte(expected), baseConfig)
}

func TestConfigureConman(t *testing.T) {
	tempDir := t.TempDir()
	
	config := DefaultConmanConfig()
	config.BaseConfFilePath = "../../scripts/conman.conf.tmpl"
	config.ConfFilePath = filepath.Join(tempDir, "conman.conf")
	config.LogFilesPath = filepath.Join(tempDir, "logs")
	config.PidFilePath = filepath.Join(tempDir, "conman.pid")
	
	nodes := map[string]*types.NodeConsoleInfo{
		"x0c0s1b0": &types.NodeConsoleInfo{
			NodeName:  "x0c0s1b0",
			BmcName:  "x0c0s1b0",
			BmcFqdn:  "x0c0s1b0",
			Class:    "River",
			NID:      0,    
			Role:     "x0c0s1b0",
		},
		"x0c0s2b0": &types.NodeConsoleInfo{
			NodeName:  "x0c0s2b0",
			BmcName:  "x0c0s2b0",
			BmcFqdn:  "x0c0s2b0",
			Class:    "Mountain",
			NID:      1,    
			Role:     "x0c0s2b0",
		},
		"x0c0s3b0": &types.NodeConsoleInfo{
			NodeName:  "x0c0s3b0",
			BmcName:  "x0c0s3b0",
			BmcFqdn:  "x0c0s3b0",
			Class:    "Paradise",
			NID:      2,    
			Role:     "x0c0s3b0",		
		},
	}

	passwords := map[string]compcredentials.CompCredentials{
		"x0c0s1b0": compcredentials.CompCredentials{
			Username: "admin",
			Password: "password1",
		},
		"x0c0s3b0": compcredentials.CompCredentials{
			Username: "admin",
			Password: "password3",
		},
	}

	// First call should create the config file
	updated := ConfigureConman(config, nodes, passwords)
	require.True(t, updated)
	
	// Read the generated config file
	generatedConfig, err := os.ReadFile(config.ConfFilePath)
	require.NoError(t, err)
	require.NotEmpty(t, generatedConfig)

	expected := `# UPDATE_CONFIG=TRUE
SERVER keepalive=ON
SERVER logdir="/logs"
SERVER logfile="conman.log"
SERVER loopback=ON
SERVER pidfile="/conman.pid"
SERVER resetcmd="powerman -0 %N; sleep 3; powerman -1 %N"
SERVER timestamp=1h
GLOBAL seropts="115200,8n1"
GLOBAL log="conman/console.%N"
GLOBAL logopts="sanitize,timestamp"
console name="x0c0s1b0" dev="ipmi:x0c0s1b0" ipmiopts="U:admin,P:password1,W:solpayloadsize"
console name="x0c0s2b0" dev="/usr/bin/ssh-key-console x0c0s2b0"
console name="x0c0s3b0" dev="/usr/bin/ssh-pwd-console x0c0s3b0 admin password3"
`
	// Remove temporary directory path from generated config for comparison
	generatedConfigStr := string(generatedConfig)
	generatedConfigStr = strings.ReplaceAll(generatedConfigStr, tempDir, "")

	require.Equal(t, expected, generatedConfigStr)
}

