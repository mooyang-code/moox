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

- `REMOTE_HOST`: 部署目标机 IP；新部署应由初始化流程询问获得。
- `REMOTE_SSH`: override full SSH target；也可以直接使用 `scripts/deploy-moox.sh --target user@host`。
- `REMOTE_ROOT`: default `~/moox`.
- `CSV_DIR`: location of acceptance CSV files.
- `STORAGE_URL`: moox-storage Access Service HTTP endpoint for acceptance writes；运行时服务部署信息以 `t_service_deployments` 为准。

Deployment uploads binaries, docs, skills, build scripts, and sample CSV files when they exist. It then runs the CSV acceptance script on the remote host.

`infra/infra.local.yaml` is not the source of service deployment topology. After the admin plane is reachable, write and update service host/port/base URL rows through SysDeploy (`t_service_deployments`).
