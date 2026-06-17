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

- `REMOTE_HOST`: default `43.132.204.177`.
- `REMOTE_SSH`: override full SSH target, for example `ubuntu@43.132.204.177`.
- `REMOTE_ROOT`: default `~/moox`.
- `CSV_DIR`: location of acceptance CSV files.
- `STORAGE_URL`: moox-storage Access Service HTTP endpoint for acceptance writes.

Deployment uploads binaries, docs, skills, build scripts, and sample CSV files when they exist. It then runs the CSV acceptance script on the remote host.
