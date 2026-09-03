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
- `scripts/deploy/deploy-moox.sh --target user@host`: preferred explicit deploy target.
- `REMOTE_ROOT`: default `/data/moox`.
- `MOOX_COLLECTOR_ADMIN_GATEWAY_URL`: optional same-host service-directory override; current deployment contract uses the independent Gateway at `http://127.0.0.1:11002`.
- `MOOX_GATEWAY_NODE_ID`, `MOOX_GATEWAY_SERVICE_KEY_ID`, and `MOOX_GATEWAY_SERVICE_SECRET_KEY`: node-scoped HMAC identity for machine calls to `/api/service/*`. Deployment persists it in mode-`0600` `secrets/gateway-service.env`.
- `MOOX_GATEWAY_CA_FILE`: CA bundle path for host processes. Serverless runtimes receive the same public certificates as base64 in `MOOX_GATEWAY_CA_PEM_B64`; file and material inputs are mutually exclusive.

`make release` creates a binary archive with binaries, docs, configs, schemas, and examples.

`make deploy` creates or syncs a runnable deployment directory with binaries, configs, schemas, examples, and runtime helper scripts such as `start.sh`, `stop.sh`, and `status.sh`.

仅发布或替换远端单个服务时，不必重新发布整个控制面。使用
[`service-release.md`](service-release.md) 中的服务 ZIP 包流程，通过
`moox-cli setup deploy-service` 从用户维护的 `moox.toml` 内部读取凭据，上传、解压并
校验整个服务包。

Public deployments require `--public-host`. Deployment installs pinned, checksum-verified, rootless Caddy `v2.11.4` automatically before acceptance; package-manager Caddy is not a prerequisite. `--tls-mode auto` uses Let's Encrypt `shortlived` certificates for public IP/DNS hosts and requires public TCP 80 for HTTP-01 issuance and renewal. Browser HTTPS `9527` exposes the site and `/api/admin/*`; service HTTPS `11001` is restricted to `/api/service/*`. Public diagnostics return `404`.

Browser control requests use a 24-hour JWT/session plus per-request HMAC. Deployment generates and persists the JWT signing secret in mode-`0600` `secrets/admin-jwt.env`. Backend and SCF calls use the independent Gateway's nonce-protected service HMAC. Dedicated health endpoints use a separate health HMAC; deployment writes its generated credential to `secrets/health-auth.env` and unsigned probes fail with `401`.

Public-mode browsers and backends use the operating-system trust store and receive no private service-edge CA variables. Caddy data persists across ordinary redeployments and data resets; Caddy uses ACME ARI to renew automatically, and the generated healthcheck restarts it after failure or reboot. `skills/moox/scripts/caddy-ca.sh`, `--target-ca`, `--local-ca`, `MOOX_SERVICE_GATEWAY_CA_FILE`, and `MOOX_SERVICE_GATEWAY_CA_PEM_B64` apply only to explicit internal mode. The separate `MOOX_GATEWAY_CA_FILE` peer bundle remains required in both modes. Never export `root.key` or disable TLS verification. See `references/caddy-https.md` and `docs/运维/管理台HTTPS与证书.md`.

Runtime data can be deleted and rebuilt from `examples/` and service flows after deployment.

Source-only developer assets such as build/release/boundary-check scripts, SCF package builders, and repository skills stay in the source tree. They are not copied into binary release packages because they require repository context.

For the independent Linux host agent, use `scripts/hostagent-release.sh`. It emits a
credential-free archive containing both binaries, example config, a user-systemd
unit, and `SHA256SUMS`; use `scripts/hostagent-deploy.sh user@host archive.tar.gz`
to install it under the remote user's home directory.

After the admin plane is reachable, write and update service host/port/base URL rows through SysDeploy (`t_service_deployments`).
