package main

import (
	"fmt"

	"github.com/OpenCHAMI/remote-console/internal/creds"
	"github.com/OpenCHAMI/remote-console/internal/conman"
	"github.com/OpenCHAMI/remote-console/internal/logs"
)

type config struct {
	Log logs.LogConfig `flag:"-"`
	Conman conman.ConmanConfig
	Creds creds.CredsConfig
	HttpListen string `desc:"HTTP listen address"`
	NewNodeLookup int `desc:"Interval in seconds to look for new nodes"`
	CredsMonitorInterval int `desc:"Interval in seconds to monitor credential updates"`
	SmdURL string `desc:"URL for the SMD service"`
	DebugOnly bool `flag:"-"`
}

func DefaultConfig() config {
	return config{
		Log: logs.DefaultLogConfig(), 
		Conman: conman.DefaultConmanConfig(),
		HttpListen:  "0.0.0.0:8080",
		NewNodeLookup: 120,
		CredsMonitorInterval: 30,
		DebugOnly: false,
		SmdURL: "http://cray-smd/",
	}
}

func validateCredsConfig(config creds.CredsConfig) error {

	if config.SecureStorageAdapter != "" {
		_, err := creds.NewStorageAdapter(string(config.SecureStorageAdapter))
		if err != nil {
			return fmt.Errorf("invalid secure storage adapter: %s, valid values are (vault or local)", config.SecureStorageAdapter)
		}

		if config.LocalStoreFilePath == "" {
			return fmt.Errorf("a local storage path must be set when using the local secure storage adapter")
		}
		
		if config.LocalStoreKey == "" {
			return fmt.Errorf("a local storage key must be set when using the local secure storage adapter")
		}
	}

	return nil
}

func validateConfig(cfg *config) error {
	return validateCredsConfig(cfg.Creds)
}


