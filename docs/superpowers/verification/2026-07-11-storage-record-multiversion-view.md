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
is absent (`vue-tsc: command not found`). Remote deployment remains a separate
release gate and has not been claimed by this local verification record.
