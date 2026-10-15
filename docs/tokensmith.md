<!--
Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
SPDX-License-Identifier: MIT
-->

# TokenSmith authentication and authorization

Remote-console supports TokenSmith v0.4.2 for inbound JWT authentication and
Casbin authorization to protect console endpoints.
Existing deployments keep their defaults:
`jwks` inbound and `oauth2` outbound. An empty `RCS_JWKS_URL` still disables
inbound authentication; OAuth2 is optional when all its settings are empty.

This follows power-control's [inbound integration](https://github.com/OpenCHAMI/power-control/pull/176).

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

Flags override their environment variables. The existing `--jwks-url` /
`RCS_JWKS_URL` and `--jwks-fetch-interval` / `RCS_JWKS_FETCH_INTERVAL` apply to
the `jwks` provider. TokenSmith uses its dedicated JWKS setting. The existing
`--oauth2-*` / `RCS_OAUTH2_*` settings apply to the outbound `oauth2` provider.
Settings for inactive providers are ignored.

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