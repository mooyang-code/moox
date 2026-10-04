# Managed Caddy HTTPS

MooX pins Caddy `v2.11.4` and installs it without root privileges below the deployment root. The normal deployment flow runs the managed prerequisite automatically; do not install package-manager Caddy as a prerequisite.

On a clean target, `caddy-prerequisite.sh ensure` installs and verifies the binary but does not start an edge without a Caddyfile or upstreams. `deploy-moox.sh` supplies `Caddyfile.next`, validates it before replacing the active configuration, starts the upstreams, and then starts or reloads Caddy.

```bash
skills/moox/scripts/caddy-prerequisite.sh ensure --target user@host --deploy-dir /home/user/moox
skills/moox/scripts/caddy-prerequisite.sh status --target user@host --deploy-dir /home/user/moox
```

The browser edge listens on `9527` and exposes the site plus `/api/admin/*`. The service edge listens on `11001` and exposes only `/api/service/*`. Their HTTP upstreams (`9528`, `11000`, and `11002`) bind to loopback. An unrelated listener causes deployment to fail; MooX never takes over a system Caddy.

Caddy state lives under `data/caddy` and survives ordinary deploys and data resets. `--tls-mode auto` selects public ACME for public IP/DNS hosts and internal CA for private, loopback, and `.localhost` hosts.

Public mode uses the Let's Encrypt `shortlived` profile and HTTP-01. TCP 80 must remain publicly reachable for issuance and renewal. Browsers and backends use the operating-system trust store; no MooX root certificate is generated or installed. Caddy renews automatically from ACME ARI while it remains running, and the generated `healthcheck.sh` restarts an unhealthy managed Caddy.

Internal mode copies its public root to `certs/caddy/root.crt`; `root.key` must never leave the target. Fetch and install that root on each browser machine:

```bash
skills/moox/scripts/caddy-ca.sh fetch --target user@host --deploy-dir /home/user/moox --output ~/.moox/certs/moox-caddy-root-<public-host>.crt
skills/moox/scripts/caddy-ca.sh inspect --ca-file ~/.moox/certs/moox-caddy-root-<public-host>.crt
skills/moox/scripts/caddy-ca.sh install --ca-file ~/.moox/certs/moox-caddy-root-<public-host>.crt
```

Install the published CA directly on the web-host target when preparing it outside the normal deploy command:

```bash
skills/moox/scripts/caddy-ca.sh install-target --target user@host --deploy-dir /home/user/moox
```

Only internal mode uses `--target-ca`, `--local-ca`, `MOOX_SERVICE_GATEWAY_CA_FILE`, or `MOOX_SERVICE_GATEWAY_CA_PEM_B64`. Never inject the internal root into public-mode callers and never use insecure TLS verification.
