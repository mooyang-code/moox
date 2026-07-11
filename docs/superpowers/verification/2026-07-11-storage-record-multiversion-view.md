# Storage Record Multiversion Verification

## Local Evidence

- `go test -count=1 ./...` in `modules/storage`: passed.
- `go test -count=1 ./...` in `modules/collector`: passed.
- `go test -count=1 ./...` in `modules/cli`: passed.
- `bash scripts/test-storage-record-multiversion-e2e.sh`: passed. The bounded
  check runs race tests for Pebble, Primary, Access, View, ViewBuilder, and
  Bleve, then collector coverage and the guarded reset test.
- `bash scripts/test-reset-storage-data.sh`: passed. It rejects missing
  confirmation, a storage-root-as-deploy-root escape, and a live managed PID.
- `git diff --check`: passed.

The web production build was not runnable in this checkout because `web/node_modules`
is absent (`vue-tsc: command not found`). The remote `moox-web-host` binary was
rebuilt and restarted from the reviewed source tree; the embedded frontend asset
build was not regenerated locally.

## Remote Evidence

- Reviewed branch `fix/storage-record-multiversion-view` was pushed at commits
  `7eaa46b` and `a337694`.
- Remote Storage access/view, Collector, and web-host were rebuilt for
  `linux/amd64`, installed, and restarted. All services reported running in
  `status.sh`; the guarded reset removed the exact `data/storage` tree, and the
  stale deployment `storage/var/storage` metadata state was also rebuilt so both
  Storage roles use the current schema.
- Metadata seed import completed for platform, crypto, and the spot 1m view.
  The history view was created through the direct storage seed path because the
  legacy HTTP gateway rejected the non-default enum before the metadata hydration
  fix; subsequent `--if-not-exists` import completed successfully.
- Bounded remote probes through `http://106.53.107.122:11000` succeeded: CURRENT
  returned only revision `2`; HISTORY returned revisions `2,1` for the dedicated
  `PLAN-VERIFY3` record. Access CURRENT/HISTORY reads returned the same revisions
  and commit sequences `5,6`.
- Remote filesystem free space after rebuild/reset was approximately `4.7G`.
