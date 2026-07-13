# Release And Deploy

Create a release package:

```bash
make release
```

Default remote deployment:

```bash
make deploy
```

Environment variables:

- `MOOX_DEV_SSH_TARGET`: optional SSH target for developer scripts.
- `scripts/deploy-moox.sh --target user@host`: preferred explicit deploy target.
- `REMOTE_ROOT`: default `~/moox`.
- `MOOX_COLLECTOR_ADMIN_GATEWAY_URL`: optional same-host control-plane override; current deployment contract uses loopback Admin control `http://127.0.0.1:11000`.
- `MOOX_SERVICE_AUTH_ACCESS_KEY` / `MOOX_SERVICE_AUTH_SECRET_KEY`: backend service auth used by Admin, Collector, Factor, Monitor, CLI, and SCF calls to `/api/service/*`. Deployment generates and persists the shared credential in mode-`0600` `secrets/service-auth.env`; repository configs contain no default secret.
- `MOOX_SERVICE_GATEWAY_CA_FILE`: public Caddy root CA for backend callers; SCF uses the equivalent `MOOX_SERVICE_GATEWAY_CA_PEM_B64` contract.

`make release` creates a binary archive with binaries, docs, configs, schemas, and examples.

`make deploy` creates or syncs a runnable deployment directory with binaries, configs, schemas, examples, and runtime helper scripts such as `start.sh`, `stop.sh`, and `status.sh`.

Public deployments require `--public-host`. Deployment installs pinned, checksum-verified, rootless Caddy `v2.11.4` automatically before acceptance; package-manager Caddy is not a prerequisite. It starts the loopback upstreams at web-host `127.0.0.1:9528`, Admin control `127.0.0.1:11000`, and Admin service `127.0.0.1:11002`, then starts or reloads the MooX-managed edge without touching unrelated processes. Browser HTTPS `9527` exposes the site and `/api/admin/*`; service HTTPS `11001` is restricted to `/api/service/*`. Public diagnostics return `404`.

Browser control requests use a 24-hour JWT/session plus per-request HMAC. Deployment generates and persists the JWT signing secret in mode-`0600` `secrets/admin-jwt.env`. Backend and SCF calls use nonce-protected service HMAC and must trust the Caddy CA. Dedicated health endpoints use a separate health HMAC; deployment writes its generated credential to `secrets/health-auth.env` and unsigned probes fail with `401`.

Use `skills/moox/scripts/caddy-ca.sh` to fetch, inspect, and explicitly install trust on each browser machine. Same-host backend configuration receives `MOOX_SERVICE_GATEWAY_CA_FILE`; SCF receives CA PEM in configuration. Caddy data and its CA persist across ordinary redeployments and data resets, leaf certificates renew automatically, and unrelated Caddy processes or occupied edge ports fail closed. Never export `root.key` or disable TLS verification. See `references/caddy-https.md` and `docs/运维/管理台HTTPS与证书.md`.

Runtime data can be deleted and rebuilt from `examples/` and service flows after deployment.

Source-only developer assets such as build/release/boundary-check scripts, SCF package builders, and repository skills stay in the source tree. They are not copied into binary release packages because they require repository context.

For the independent Linux host agent, use `scripts/hostagent-release.sh`. It emits a
credential-free archive containing both binaries, example config, a user-systemd
unit, and `SHA256SUMS`; use `scripts/hostagent-deploy.sh user@host archive.tar.gz`
to install it under the remote user's home directory.

After the admin plane is reachable, write and update service host/port/base URL rows through SysDeploy (`t_service_deployments`).
