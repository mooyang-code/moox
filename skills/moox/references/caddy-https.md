# Managed Caddy HTTPS

MooX pins Caddy `v2.11.4` and installs it without root privileges below the deployment root. The normal deployment flow runs the managed prerequisite automatically; do not install package-manager Caddy as a prerequisite.

On a clean target, `caddy-prerequisite.sh ensure` installs and verifies the binary but does not start an edge without a Caddyfile or upstreams. `deploy-moox.sh` supplies `Caddyfile.next`, validates it before replacing the active configuration, starts the upstreams, and then starts or reloads Caddy.

```bash
skills/moox/scripts/caddy-prerequisite.sh ensure --target user@host --deploy-dir /home/user/moox
skills/moox/scripts/caddy-prerequisite.sh status --target user@host --deploy-dir /home/user/moox
```

The browser edge listens on `9527` and exposes the site plus `/api/admin/*`. The service edge listens on `11001` and exposes only `/api/service/*`. Their HTTP upstreams (`9528`, `11000`, and `11002`) bind to loopback. An unrelated listener causes deployment to fail; MooX never takes over a system Caddy.

Caddy state lives under `data/caddy` and survives ordinary deploys and data resets. The public root is copied to `certs/caddy/root.crt`; `root.key` must never leave the target. A changed root fingerprint fails closed and requires an explicit CA rotation procedure.

Fetch and install trust on each browser machine:

```bash
skills/moox/scripts/caddy-ca.sh fetch --target user@host --deploy-dir /home/user/moox --output ~/.moox/certs/moox-root.crt
skills/moox/scripts/caddy-ca.sh inspect --ca-file ~/.moox/certs/moox-root.crt
skills/moox/scripts/caddy-ca.sh install --ca-file ~/.moox/certs/moox-root.crt
```

Interactive deployment may offer to install trust on the operator machine once. Other browser machines must run the helper locally. Backend processes receive `MOOX_SERVICE_GATEWAY_CA_FILE`; SCF configuration uses the equivalent base64 PEM. Never use insecure TLS verification. Caddy renews leaf certificates automatically while its data directory persists.
