// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/OpenCHAMI/remote-console/internal/conman"
	"github.com/OpenCHAMI/remote-console/internal/creds"
	"github.com/OpenCHAMI/remote-console/internal/logs"
)

type OAuth2Config struct {
	ClientID     string   `desc:"OAuth2 client ID for SMD authentication"`
	ClientSecret string   `desc:"OAuth2 client secret for SMD authentication"`
	TokenURL     string   `desc:"OAuth2 token endpoint URL for SMD authentication"`
	Scopes       []string `desc:"OAuth2 scopes for SMD authentication"`
}

type remoteConsoleConfig struct {
	Log                  logs.LogConfig `flag:"-"`
	Conman               conman.ConmanConfig
	Creds                creds.CredsConfig
	HttpListen           string `desc:"HTTP listen address"`
	NewNodeLookup        int    `desc:"Interval in seconds to look for new nodes"`
	CredsMonitorInterval int    `desc:"Interval in seconds to monitor credential updates"`
	SmdURL               string `desc:"URL for the SMD service"`
	JwksURL              string `desc:"JWKS URL for fetching public keys for JWT validation (optional)"`
	JwksFetchInterval    int    `desc:"Interval in seconds to retry fetching JWKS on failure"`
	Oauth2               OAuth2Config
}

func DefaultRemoteConsoleConfig() remoteConsoleConfig {
	return remoteConsoleConfig{
		Log:                  logs.DefaultLogConfig(),
		Conman:               conman.DefaultConmanConfig(),
		Creds:                creds.DefaultCredsConfig(),
		HttpListen:           "0.0.0.0:26776",
		NewNodeLookup:        120,
		CredsMonitorInterval: 30,
		SmdURL:               "http://cray-smd/",
		JwksURL:              "",
		JwksFetchInterval:    5,
		// Note: Oauth2 vs OAuth2 so the sflags generate the correct flag name.
		Oauth2: OAuth2Config{},
	}
}

func ensureTrailingSlash(url string) string {
	if url != "" && !strings.HasSuffix(url, "/") {
		return url + "/"
	}
	return url
}

func applyRemoteConsoleEnv(config *remoteConsoleConfig) error {
	setString := func(key string, target *string) {
		if value := os.Getenv(key); value != "" {
			*target = value
		}
	}
	setStringSlice := func(key string, target *[]string) {
		if value := os.Getenv(key); value != "" {
			*target = strings.Split(value, ",")
		}
	}
	setInt := func(key string, target *int) error {
		value := os.Getenv(key)
		if value == "" {
			return nil
		}
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid %s value %q: %w", key, value, err)
		}
		*target = parsed
		return nil
	}
	setBool := func(key string, target *bool) error {
		value := os.Getenv(key)
		if value == "" {
			return nil
		}
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid %s value %q: %w", key, value, err)
		}
		*target = parsed
		return nil
	}

	setString("RCS_HTTP_LISTEN", &config.HttpListen)
	setString("RCS_SMD_URL", &config.SmdURL)
	setString("RCS_JWKS_URL", &config.JwksURL)
	setString("RCS_OAUTH2_CLIENT_ID", &config.Oauth2.ClientID)
	setString("RCS_OAUTH2_CLIENT_SECRET", &config.Oauth2.ClientSecret)
	setString("RCS_OAUTH2_TOKEN_URL", &config.Oauth2.TokenURL)
	setStringSlice("RCS_OAUTH2_SCOPES", &config.Oauth2.Scopes)

	setString("RCS_CONMAN_BASE_CONF_FILE_PATH", &config.Conman.BaseConfFilePath)
	setString("RCS_CONMAN_CONF_FILE_PATH", &config.Conman.ConfFilePath)
	setString("RCS_CONMAN_LOGS_PATH", &config.Conman.LogsPath)
	setString("RCS_CONMAN_PID_FILE_PATH", &config.Conman.PidFilePath)
	setString("RCS_CONMAN_CONSOLE_SCRIPTS_PATH", &config.Conman.ConsoleScriptsPath)

	setString("RCS_CREDS_SSH_CONSOLE_KEY_PATH", &config.Creds.SshConsoleKeyPath)
	if value := os.Getenv("RCS_CREDS_SECURE_STORAGE_ADAPTER"); value != "" {
		config.Creds.SecureStorageAdapter = creds.StorageAdapter(value)
	}
	setString("RCS_CREDS_VAULT_BASE_PATH", &config.Creds.VaultBasePath)
	setString("RCS_CREDS_VAULT_ROLE", &config.Creds.VaultRole)
	setString("RCS_CREDS_LOCAL_STORE_FILE_PATH", &config.Creds.LocalStoreFilePath)
	setString("RCS_CREDS_LOCAL_STORE_KEY", &config.Creds.LocalStoreKey)
	setString("RCS_CREDS_SECURE_STORAGE_SSH_KEYS_PATH", &config.Creds.SecureStorageSshKeysPath)
	setString("RCS_CREDS_SECURE_STORAGE_PASSWORDS_PATH", &config.Creds.SecureStoragePasswordsPath)

	setString("RCS_CONSOLE_LOGS_FILE_SIZE", &config.Log.ConsoleLogsFileSize)
	setString("RCS_CONSOLE_LOGS_BACKUP_PATH", &config.Log.ConsoleLogsBackupPath)
	setString("RCS_AGG_LOGS_FILE_SIZE", &config.Log.AggLogsFileSize)
	setString("RCS_AGG_LOGS_PATH", &config.Log.AggLogsPath)
	setString("RCS_LOG_ROTATE_FILE_PATH", &config.Log.LogRotateFilePath)
	setString("RCS_LOG_ROTATE_STATE_FILE_PATH", &config.Log.LogRotateStateFilePath)

	if err := setInt("RCS_NEW_NODE_LOOKUP", &config.NewNodeLookup); err != nil {
		return err
	}
	if err := setInt("RCS_CREDS_MONITOR_INTERVAL", &config.CredsMonitorInterval); err != nil {
		return err
	}
	if err := setInt("RCS_JWKS_FETCH_INTERVAL", &config.JwksFetchInterval); err != nil {
		return err
	}
	if err := setInt("RCS_CONSOLE_LOGS_NUM_ROTATE", &config.Log.ConsoleLogsNumRotate); err != nil {
		return err
	}
	if err := setInt("RCS_AGG_LOGS_NUM_ROTATE", &config.Log.AggLogsNumRotate); err != nil {
		return err
	}
	if err := setInt("RCS_LOG_ROTATE_CHECK_FREQUENCY", &config.Log.LogRotateCheckFrequency); err != nil {
		return err
	}
	if err := setBool("RCS_LOG_ROTATE_ENABLED", &config.Log.LogRotateEnabled); err != nil {
		return err
	}

	config.SmdURL = ensureTrailingSlash(config.SmdURL)
	return validateConfig(config)
}

func validateCredsConfig(config *remoteConsoleConfig) error {
	credConfig := config.Creds

	if credConfig.SecureStorageAdapter != "" {
		_, err := creds.NewStorageAdapter(string(credConfig.SecureStorageAdapter))
		if err != nil {
			return fmt.Errorf("invalid secure storage adapter: %s, valid values are (vault or local)", credConfig.SecureStorageAdapter)
		}

		if credConfig.SecureStorageAdapter == creds.StorageAdapterLocal {
			if credConfig.LocalStoreFilePath == "" {
				return fmt.Errorf("a local storage path must be set when using the local secure storage adapter")
			}

			if credConfig.LocalStoreKey == "" {
				return fmt.Errorf("a local storage key must be set when using the local secure storage adapter")
			}
		}
	}

	return nil
}

func validateConfig(config *remoteConsoleConfig) error {
	if err := validateCredsConfig(config); err != nil {
		return err
	}

	// Validate OAuth2 configuration - either all or nothing.
	oauth2 := config.Oauth2

	oauth2Provided := []bool{
		oauth2.ClientID != "",
		oauth2.ClientSecret != "",
		oauth2.TokenURL != "",
		len(oauth2.Scopes) > 0,
	}

	allProvided := !slices.Contains(oauth2Provided, false)
	noneProvided := !slices.Contains(oauth2Provided, true)

	if !allProvided && !noneProvided {
		return fmt.Errorf("incomplete OAuth2 configuration: all fields (oauth2-client-id, oauth2-client-secret, oauth2-token-url and oauth2-scopes) must be provided")
	}

	return nil
}
