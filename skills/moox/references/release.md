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

- `REMOTE_HOST`: 部署目标机 IP，默认从 `infra/infra.local.yaml` 的 `remote.host` 读取（见 `scripts/infra-env.sh`）。
- `REMOTE_SSH`: override full SSH target, 默认从 `infra/infra.local.yaml` 的 `remote.ssh` 读取。
- `REMOTE_ROOT`: default `~/moox`.
- `CSV_DIR`: location of acceptance CSV files.
- `STORAGE_URL`: moox-storage Access Service HTTP endpoint for acceptance writes, 默认从 `infra/infra.local.yaml` 的 `services.storage_access` 读取。

Deployment uploads binaries, docs, skills, build scripts, and sample CSV files when they exist. It then runs the CSV acceptance script on the remote host.
