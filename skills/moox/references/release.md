# Release And Deploy

Create a release package:

```bash
make release
```

Create all public platform archives and SHA-256 files:

```bash
VERSION=v0.1.0 make release-matrix
```

The default matrix is Linux amd64/arm64, macOS amd64/arm64, and Windows
amd64. Pushing a `v*` tag runs the GitHub Actions and CNB pipelines, which
create the release and upload the same archives on both platforms. Set
`RELEASE_PLATFORMS` to a comma-separated list to select a smaller matrix.

Matrix cross-builds use Storage's no-CGO fallback unless the target is the
current host or `STORAGE_CGO_ENABLED=1` is supplied. The full DuckDB-backed
Storage build requires a target C toolchain when CGO is enabled.

Default remote deployment:

```bash
make deploy
```

Environment variables:

- `MOOX_DEV_SSH_TARGET`: optional SSH target for developer scripts.
- `scripts/deploy-moox.sh --target user@host`: preferred explicit deploy target.
- `REMOTE_ROOT`: default `~/moox`.
- `MOOX_COLLECTOR_ADMIN_GATEWAY_URL`: optional same-host service-directory override; current deployment contract uses the independent Gateway at `http://127.0.0.1:11002`.
- `MOOX_GATEWAY_NODE_ID`, `MOOX_GATEWAY_SERVICE_KEY_ID`, and `MOOX_GATEWAY_SERVICE_SECRET_KEY`: node-scoped HMAC identity for machine calls to `/api/service/*`. Deployment persists it in mode-`0600` `secrets/gateway-service.env`.
- `MOOX_GATEWAY_CA_FILE`: CA bundle path for host processes. Serverless runtimes receive the same public certificates as base64 in `MOOX_GATEWAY_CA_PEM_B64`; file and material inputs are mutually exclusive.

`make release` creates a binary archive with binaries, docs, configs, schemas, and examples.

`make deploy` creates or syncs a runnable deployment directory with binaries, configs, schemas, examples, and runtime helper scripts such as `start.sh`, `stop.sh`, and `status.sh`.

Public deployments require `--public-host`. Deployment installs pinned, checksum-verified, rootless Caddy `v2.11.4` automatically before acceptance; package-manager Caddy is not a prerequisite. It rejects non-loopback upstream overrides, starts web-host `127.0.0.1:9528`, Admin control `127.0.0.1:11000`, and Admin service `127.0.0.1:11002`, then starts or reloads the MooX-managed edge without touching unrelated processes. `--target-ca auto` attempts target trust-store installation and downgrades only a recognized lack of elevated permission; other installation failures stop deployment. Browser HTTPS `9527` exposes the site and `/api/admin/*`; service HTTPS `11001` is restricted to `/api/service/*`. Public diagnostics return `404`.

Browser control requests use a 24-hour JWT/session plus per-request HMAC. Deployment generates and persists the JWT signing secret in mode-`0600` `secrets/admin-jwt.env`. Backend and SCF calls use the independent Gateway's nonce-protected service HMAC. Dedicated health endpoints use a separate health HMAC; deployment writes its generated credential to `secrets/health-auth.env` and unsigned probes fail with `401`.

Use `skills/moox/scripts/caddy-ca.sh` to fetch, inspect, or explicitly install trust on each browser machine. Public deployment also checks the operator trust store every time and installs the missing CA automatically unless `--local-ca skip` is explicitly selected. The default local filename is `~/.moox/certs/moox-caddy-root-<public-host>.crt`, including the target IP/DNS for migration and multi-server identification. Same-host Gateway callers receive `MOOX_GATEWAY_CA_FILE`; SCF receives `MOOX_GATEWAY_CA_PEM_B64`. Caddy data and its CA persist across ordinary redeployments and data resets, leaf certificates renew automatically, and unrelated Caddy processes or occupied edge ports fail closed. Never export `root.key` or disable TLS verification. See `references/caddy-https.md` and `docs/运维/管理台HTTPS与证书.md`.

Runtime data can be deleted and rebuilt from `examples/` and service flows after deployment.

Source-only developer assets such as build/release/boundary-check scripts, SCF package builders, and repository skills stay in the source tree. They are not copied into binary release packages because they require repository context.

For the independent Linux host agent, use `scripts/hostagent-release.sh`. It emits a
credential-free archive containing both binaries, example config, a user-systemd
unit, and `SHA256SUMS`; use `scripts/hostagent-deploy.sh user@host archive.tar.gz`
to install it under the remote user's home directory.

After the admin plane is reachable, write and update service host/port/base URL rows through SysDeploy (`t_service_deployments`).
