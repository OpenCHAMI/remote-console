<!--
Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
SPDX-License-Identifier: MIT
-->

# TokenSmith authentication and authorization

Remote-console supports TokenSmith v0.4.2 for inbound JWT authentication and
Casbin authorization, and for obtaining its own service tokens for SMD.
The two providers are independent. Existing deployments keep their defaults:
`jwks` inbound and `oauth2` outbound. An empty `RCS_JWKS_URL` still disables
inbound authentication; OAuth2 is optional when all its settings are empty.

This follows power-control's [inbound integration](https://github.com/OpenCHAMI/power-control/pull/176)
and [outbound integration](https://github.com/OpenCHAMI/power-control/pull/177).

To enable inbound TokenSmith support, export these settings before starting the
service, or set them in the container environment:

```sh
RCS_AUTH_PROVIDER=tokensmith
RCS_TOKENSMITH_ISSUER=https://tokensmith.example
RCS_TOKENSMITH_JWKS_URL=https://tokensmith.example/.well-known/jwks.json
RCS_TOKENSMITH_AUDIENCE=remote-console
RCS_TOKENSMITH_AUTHZ_MODE=enforce
RCS_TOKENSMITH_AUTHZ_POLICY_PATH=/app/configs/authz/policy.csv
RCS_TOKENSMITH_AUTHZ_GROUPING_PATH=/app/configs/authz/grouping.csv
```

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--auth-provider` | `RCS_AUTH_PROVIDER` | `jwks` |
| `--tokensmith-jwks-url` | `RCS_TOKENSMITH_JWKS_URL` | empty (required for TokenSmith) |
| `--tokensmith-issuer` | `RCS_TOKENSMITH_ISSUER` | empty (required for TokenSmith) |
| `--tokensmith-audience` | `RCS_TOKENSMITH_AUDIENCE` | `remote-console` |
| `--tokensmith-authz-mode` | `RCS_TOKENSMITH_AUTHZ_MODE` | `enforce` |
| `--tokensmith-authz-model-path` | `RCS_TOKENSMITH_AUTHZ_MODEL_PATH` | TokenSmith RBAC path model |
| `--tokensmith-authz-policy-path` | `RCS_TOKENSMITH_AUTHZ_POLICY_PATH` | empty (required in enforce/shadow) |
| `--tokensmith-authz-grouping-path` | `RCS_TOKENSMITH_AUTHZ_GROUPING_PATH` | empty |
| `--smd-auth-provider` | `RCS_SMD_AUTH_PROVIDER` | `oauth2` |
| `--smd-tokensmith-url` | `RCS_SMD_TOKENSMITH_URL` | empty (required for outbound TokenSmith) |
| `--smd-tokensmith-ca-file` | `RCS_SMD_TOKENSMITH_CA_FILE` | system CA certificates |

Flags override their environment variables. The existing `--jwks-url` /
`RCS_JWKS_URL` and `--jwks-fetch-interval` / `RCS_JWKS_FETCH_INTERVAL` apply to
the `jwks` provider. TokenSmith uses its dedicated JWKS setting. The existing
`--oauth2-*` / `RCS_OAUTH2_*` settings apply to the outbound `oauth2` provider.
Settings for inactive providers are ignored. Outbound setup logs a warning when
provider-specific settings are ignored or TokenSmith uses plaintext HTTP.

TokenSmith validates the signature, issuer, audience, timestamps, and TokenSmith
claims. Callers must send `Authorization: Bearer <token>`; tokens from other issuers
must be exchanged through TokenSmith first or use the `jwks` provider.
Missing or invalid tokens return `401` on protected routes in every mode:

- `enforce` returns `403` for policy denials.
- `shadow` evaluates and logs policy decisions while allowing authenticated requests.
- `off` skips authorization while retaining authentication.

GET `/remote-console/liveness`, `/remote-console/readiness`, and
`/remote-console/health` remain public. Both `/remote-console/consoles` and
`/remote-console/consoles/{nodeID}` require authentication. Authorization runs
before the WebSocket handshake for both tail and interactive access.

Policies match normalized URL paths and actions. Console listing and tail
handshakes use `read`. Interactive handshakes use `write`, including requests
where `mode` is empty or omitted. Other HTTP methods retain the REST mapping:
POST/PUT/PATCH use `write`, and DELETE uses `delete`.

Roles come from TokenSmith's `scope` array, without a `role:` prefix in the token.
The bundled policy lets `remote-console-reader` list and tail consoles.
`remote-console-operator` inherits reader access and can open interactive sessions
to type commands. For node-specific access, replace the wildcard policy with
explicit node paths and the appropriate `read` or `write` action.

Authorization runs before the WebSocket upgrade and is checked at connection
time. Changing policy or expiring a token does not terminate an established
connection.

Custom models accept `sub, obj, act`. Policy and grouping paths accept files or
directories of CSV fragments. Restart the service to reload changes. Invalid
configuration, unreadable policies, and unusable JWKS prevent startup before
background workers start. The JWKS provider retains six startup fetch
attempts with the configured retry interval.

## Outbound SMD authentication

To obtain service tokens using a bootstrap credential:

```sh
RCS_SMD_AUTH_PROVIDER=tokensmith
RCS_SMD_TOKENSMITH_URL=https://tokensmith.example
TOKENSMITH_BOOTSTRAP_TOKEN=<single-use-bootstrap-token>
# RCS_SMD_TOKENSMITH_CA_FILE=/certs/ca.pem  # additional CA, when needed
```

Provision the bootstrap credential for subject `remote-console`, audience `smd`,
and the scopes required by SMD. Remote-console authenticates with its own service
identity; incoming caller tokens are not forwarded. SMD must trust TokenSmith's
JWKS. Service token initialization happens at startup and fails closed on error.

For mTLS identity, mount the identity files and use these TokenSmith SDK settings:

```sh
TOKENSMITH_SERVICE_IDENTITY_CERT=/certs/remote-console.pem
TOKENSMITH_SERVICE_IDENTITY_KEY=/certs/remote-console-key.pem
```

These credential environment names intentionally follow the TokenSmith SDK;
all service flags retain the `RCS_` convention. Both identity files must be
configured together, and mTLS requires an HTTPS TokenSmith URL. Identity files
take precedence over a bootstrap token. Missing or invalid identity files cause
failure rather than falling back to bootstrap credentials.

Refreshes are serialized so concurrent SMD requests share a rotated token.
When an mTLS refresh session expires, remote-console reloads the current identity
files and creates a new session, supporting certificate rotation. Bootstrap
exchange makes one attempt because the credential is single-use. A bootstrap-only
deployment needs a fresh credential on restart or after its refresh session
expires. Refresh failures fail outbound requests; discovery retries on its normal
polling interval. The readiness probe retains its existing process-level behavior.
