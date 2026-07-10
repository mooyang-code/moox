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
- `MOOX_COLLECTOR_ADMIN_GATEWAY_URL`: optional override for Collector SysDeploy dependency discovery; deploy defaults to `http://127.0.0.1:11000`.
- `MOOX_SERVICE_AUTH_ACCESS_KEY` / `MOOX_SERVICE_AUTH_SECRET_KEY`: backend service auth used by Collector when calling `/api/service/sysdeploy/*`; deploy defaults mirror `modules/admin/config/gateway.yaml` development values unless overridden.

`make release` creates a binary archive with binaries, docs, configs, schemas, and examples.

`make deploy` creates or syncs a runnable deployment directory with binaries, configs, schemas, examples, and runtime helper scripts such as `start.sh`, `stop.sh`, and `status.sh`.

Runtime data can be deleted and rebuilt from `examples/` and service flows after deployment.

Source-only developer assets such as build/release/boundary-check scripts, SCF package builders, and repository skills stay in the source tree. They are not copied into binary release packages because they require repository context.

For the independent Linux host agent, use `scripts/hostagent-release.sh`. It emits a
credential-free archive containing both binaries, example config, a user-systemd
unit, and `SHA256SUMS`; use `scripts/hostagent-deploy.sh user@host archive.tar.gz`
to install it under the remote user's home directory.

After the admin plane is reachable, write and update service host/port/base URL rows through SysDeploy (`t_service_deployments`).
