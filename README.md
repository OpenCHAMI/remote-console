# Remote Console

Remote Console is an OpenCHAMI service for remote console access.

The service is a wrapper around conman which manages console access. The service
generates the appropriate configuation to drive conman.  The service
layer keeps conman behind a narrower interface so clients do not need to invoke
conman directly and the implementation will likely evolve over time.

## History

This codebase is a combination of ideas and code from the earlier
[Cray-HPE/console-node](https://github.com/Cray-HPE/console-node) and
[Cray-HPE/console-data](https://github.com/Cray-HPE/console-data) services.
These projects split console connection handling, console logging, and console
state tracking across multiple services.

Notable changes since the original import include:

- OpenCHAMI module naming and GitHub/GHCR build automation.
- Splitting functionality into well defined packages each with their own configuration.
  Providing better separation of concerns and testability.
- Vendor-agnostic console discovery using Redfish data provided by SMD instead
  of HPE-specific node type checks.
- WebSocket console access for interactive sessions and log tail sessions.
- Unit tests for the new internal packages.
- Integration tests using testcontainers, an IPMI simulator, Redfish mocks, and
  SSH containers that act as mock consoles.

## Runtime Behavior

On startup the service:

1. Loads configuration from flags and `RCS_` environment variables.
2. Fetches console-capable nodes from SMD.
3. Retrieves console credentials from secure storage.
4. Writes a generated conman configuration using `scripts/conman.conf.tmpl` template.
5. Runs `conmand`.
6. Serves HTTP health, console inventory, and WebSocket console endpoints.
7. Watches SMD and credential state for changes and restarts or signals conman
   when needed.
8. Manages conman log rotation and aggregate console logs.

## API

All routes are under `/remote-console`.

| Route | Description |
| --- | --- |
| `GET /liveness` | Kubernetes-style liveness check. Returns `204` when alive. |
| `GET /readiness` | Kubernetes-style readiness check. Returns `204` when ready. |
| `GET /health` | Returns console count and last hardware update time. |
| `GET /consoles` | Returns the current console inventory. |
| `GET /consoles/{nodeID}` | WebSocket interactive console session. |
| `GET /consoles/{nodeID}?mode=tail` | WebSocket console log tail session. |

The console endpoints are protected by JWT middleware when `--jwks-url` is set.
If no JWKS URL is configured, console endpoints are left unprotected and the
service logs a warning.

Tail mode supports:

| Query parameter | Description |
| --- | --- |
| `mode=tail` | Selects console log tail mode instead of interactive mode. |
| `follow=true` | Continues streaming new log lines after existing content. |
| `lines=N` | Sends the last `N` lines before optionally following. |

## Build and Test

Build the container image:

```sh
make image
```

Override the image tag:

```sh
make image DOCKER_VERSION=dev
```

Run lint:

```sh
make lint
```

Run tests:

```sh
make test
```

## Configuration

Configuration is exposed as command-line flags and matching environment
variables. Environment variables use the `RCS_` prefix. Command-line flags take
precedence over environment variables.

| Flag | Environment variable | Default | Description |
| --- | --- | --- | --- |
| `--conman-base-conf-file-path` | `RCS_CONMAN_BASE_CONF_FILE_PATH` | `/app/conman.conf.tmpl` | Path to the base conman configuration template file. |
| `--conman-conf-file-path` | `RCS_CONMAN_CONF_FILE_PATH` | `/app/conman.conf` | Path to the generated conman configuration file. |
| `--conman-logs-path` | `RCS_CONMAN_LOGS_PATH` | `/var/log/conman` | Path to conman log files. |
| `--conman-pid-file-path` | `RCS_CONMAN_PID_FILE_PATH` | `/var/run/conman.pid` | Path to the conman PID file. |
| `--conman-console-scripts-path` | `RCS_CONMAN_CONSOLE_SCRIPTS_PATH` | `/usr/bin` | Path to console helper scripts. |
| `--creds-ssh-console-key-path` | `RCS_CREDS_SSH_CONSOLE_KEY_PATH` | `/app/conman.key` | Path where the SSH private key file for console access is written. |
| `--creds-vault-base-path` | `RCS_CREDS_VAULT_BASE_PATH` | empty | Base path in Vault where credentials are stored. |
| `--creds-vault-role` | `RCS_CREDS_VAULT_ROLE` | empty | Vault role to use when authenticating to Vault. |
| `--creds-local-store-file-path` | `RCS_CREDS_LOCAL_STORE_FILE_PATH` | empty | Path to local secure storage file. |
| `--creds-local-store-key` | `RCS_CREDS_LOCAL_STORE_KEY` | empty | Key to use for local secure storage decryption. |
| `--creds-secure-storage-ssh-keys-path` | `RCS_CREDS_SECURE_STORAGE_SSH_KEYS_PATH` | empty | Path where SSH keys can be found in secure storage. Leave empty to skip SSH key management. |
| `--creds-secure-storage-passwords-path` | `RCS_CREDS_SECURE_STORAGE_PASSWORDS_PATH` | `hms-creds` | Path where console access credentials can be found in secure storage. |
| `--http-listen` | `RCS_HTTP_LISTEN` | `0.0.0.0:26776` | HTTP listen address. |
| `--new-node-lookup` | `RCS_NEW_NODE_LOOKUP` | `120` | Interval in seconds to look for new nodes. |
| `--creds-monitor-interval` | `RCS_CREDS_MONITOR_INTERVAL` | `30` | Interval in seconds to monitor credential updates. |
| `--smd-url` | `RCS_SMD_URL` | `http://cray-smd/` | URL for the SMD service. A trailing slash is added automatically. |
| `--jwks-url` | `RCS_JWKS_URL` | empty | JWKS URL for fetching public keys for JWT validation. |
| `--jwks-fetch-interval` | `RCS_JWKS_FETCH_INTERVAL` | `5` | Interval in seconds to retry fetching JWKS on failure. |
| `--oauth2-client-id` | `RCS_OAUTH2_CLIENT_ID` | empty | OAuth2 client ID for SMD authentication. |
| `--oauth2-client-secret` | `RCS_OAUTH2_CLIENT_SECRET` | empty | OAuth2 client secret for SMD authentication. |
| `--oauth2-token-url` | `RCS_OAUTH2_TOKEN_URL` | empty | OAuth2 token endpoint URL for SMD authentication. |
| `--oauth2-scopes` | `RCS_OAUTH2_SCOPES` | `[]` | OAuth2 scopes for SMD authentication. |
| `--console-logs-file-size` | `RCS_CONSOLE_LOGS_FILE_SIZE` | `5M` | Maximum size of console log files before rotation. |
| `--console-logs-num-rotate` | `RCS_CONSOLE_LOGS_NUM_ROTATE` | `2` | Number of rotated console log files to keep. |
| `--console-logs-backup-path` | `RCS_CONSOLE_LOGS_BACKUP_PATH` | `/var/log/conman.old` | Path to rotated console log files. |
| `--agg-logs-file-size` | `RCS_AGG_LOGS_FILE_SIZE` | `20M` | Maximum size of aggregation log file before rotation. |
| `--agg-logs-num-rotate` | `RCS_AGG_LOGS_NUM_ROTATE` | `1` | Number of rotated aggregation log files to keep. |
| `--agg-logs-path` | `RCS_AGG_LOGS_PATH` | `/tmp/consoleAgg` | Path to aggregation log files. |
| `--log-rotate-enabled` | `RCS_LOG_ROTATE_ENABLED` | `true` | Enable log rotation. |
| `--log-rotate-check-frequency` | `RCS_LOG_ROTATE_CHECK_FREQUENCY` | `600` | Frequency in seconds to check for log rotation. |
| `--log-rotate-file-path` | `RCS_LOG_ROTATE_FILE_PATH` | `/tmp/logrotate.conman` | Path to generated logrotate configuration file. |
| `--log-rotate-state-file-path` | `RCS_LOG_ROTATE_STATE_FILE_PATH` | `/tmp/rot_conman.state` | Path to logrotate state file. |

OAuth2 settings are all-or-nothing. If any OAuth2 field is set, all of
`--oauth2-client-id`, `--oauth2-client-secret`, `--oauth2-token-url`, and
`--oauth2-scopes` must be provided.

Logging is configured separately with these process environment variables:

| Environment variable | Default | Description |
| --- | --- | --- |
| `LOG_LEVEL` | `INFO` | Any level accepted by Go `slog`, such as `DEBUG`, `INFO`, `WARN`, or `ERROR`. |
| `LOG_FORMAT` | `text` | Set to `json` for structured JSON logs. Any other value uses text logs. |

## License

This project is licensed under the MIT license. See [LICENSE](LICENSE) for
details.
