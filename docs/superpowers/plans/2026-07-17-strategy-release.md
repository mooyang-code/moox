# Strategy Runtime, Release, and Deployment Implementation Plan

> **Worker requirement:** Use test-driven-development. Keep runtime, credential, packaging, and deployment commits independently reviewable.

**Goal:** Make Strategy a first-class standard module: publish its transactional outbox through JetStream, report truthful readiness, build and package all runtime assets, deploy with a dedicated credential and Python environment, activate routing only after readiness, and fully disable it with `--no-strategy`.

**Fixed contract:** RPC `11430`, readiness `11431`, metrics `12930`; deployed database `data/strategy/strategy.sqlite`; Live remains disabled; ordinary publishers receive only business-topic publish and reply-inbox subscribe permissions, never JetStream management permissions.

---

### Task 1: Publish the transactional outbox reliably

**Files:**
- Modify: `modules/strategy/internal/bus/outbox.go`
- Create: `modules/strategy/internal/bus/publisher.go`
- Modify/Create tests: `modules/strategy/internal/bus/*_test.go`
- Modify: `modules/strategy/internal/store/outbox.go`
- Modify: `modules/strategy/internal/store/outbox_test.go`
- Modify: `modules/strategy/go.mod`, `modules/strategy/go.sum`

- [ ] Add RED tests for stable message ID, `messagepb.MooxMessage` envelope metadata, exact business topic, successful mark-published, publish failure release, retry, cancellation, pending count, and oldest pending age.
- [ ] Adapt `packages/jetstream.Client.Publish` behind a narrow publisher interface. Use the outbox row ID as the stable deduplication ID; never publish the raw Strategy JSON without the shared envelope.
- [ ] Add a cancellable relay loop with bounded batch size and configurable interval. Claim atomically, publish, mark on success, release on failure, and preserve rows across restart.
- [ ] Run:

```bash
cd modules/strategy
go test -race ./internal/bus ./internal/store -count=1
cd ../../packages/jetstream
go test ./... -count=1
```

### Task 2: Wire runtime lifecycle and truthful health

**Files:**
- Modify: `modules/strategy/internal/bootstrap/config.go`
- Modify: `modules/strategy/internal/bootstrap/bootstrap.go`
- Modify: `modules/strategy/internal/bootstrap/health.go`
- Modify/Create: `modules/strategy/internal/bootstrap/*_test.go`
- Modify: `modules/strategy/internal/engine/**`
- Modify: `modules/strategy/config/app.yaml`

- [ ] Add RED tests for engine worker handshake, unavailable EventBus startup, reconnect, relay catch-up, broker loss, readiness transitions, outbox details, and deterministic shutdown.
- [ ] Add eventbus URLs, credential path, relay interval, batch size, and reconnect settings to typed validated config.
- [ ] Add an explicit Python worker smoke/probe handshake. `eng != nil` or `os.Stat(worker)` is not readiness.
- [ ] Bootstrap database/schema and engine first, start a reconnecting JetStream/relay lifecycle, then register RPC/health. EventBus loss must keep read-only RPC serving but set readiness false.
- [ ] Health details must include fixed fields `eventbus_connected`, `outbox_pending_count`, and `oldest_outbox_age_seconds`; readiness requires database, worker handshake, and EventBus.
- [ ] Shutdown order: stop intake, cancel relay/reconnect, wait goroutines, close JetStream, engine, then DB. Prove no goroutine leak/deadlock with race tests.
- [ ] Run:

```bash
cd modules/strategy
go test -race ./internal/bootstrap ./internal/engine ./internal/bus ./test -count=1
```

### Task 3: Add a real JetStream integration test

**Files:**
- Modify: `modules/strategy/test/e2e_test.go`
- Create: `modules/strategy/test/outbox_jetstream_e2e_test.go`

- [ ] Reuse the repository's embedded NATS Server pattern to boot a real JetStream stream for Strategy topics.
- [ ] Commit a real Strategy decision, verify a `moox.strategy.action.accepted.v1` envelope with stable `Nats-Msg-Id`, and verify the outbox converges to published.
- [ ] Stop the broker, verify readiness false and pending retention, restart it, then verify reconnect and catch-up without duplicates.
- [ ] Run twice and under race:

```bash
cd modules/strategy
go test ./test -run 'Test.*(Outbox|JetStream)' -count=2
go test -race ./test -run 'Test.*(Outbox|JetStream)' -count=1
```

### Task 4: Provision least-privilege Strategy credentials

**Files:**
- Modify: `modules/admin/cmd/cli/eventbus_credentials.go`
- Modify: `modules/admin/cmd/cli/eventbus_credentials_test.go`
- Modify: `scripts/deploy/deploy-moox.sh`
- Modify: `scripts/test/contract/test-deploy-moox-eventbus.sh`

- [ ] Add failing tests for a ninth secret, exported `strategy-eventbus.yaml` mode `0600`, publish allow-list for the three registered Strategy topics, `_INBOX.>` subscription, forbidden unrelated topics, and rotation.
- [ ] Add the `strategy-eventbus` role/key/user/export wiring. Do not grant `$JS.API.>`.
- [ ] Stage the credential at `~/.config/moox/eventbus/strategy-eventbus.yaml` and rewrite Strategy config to that stable deployed path.
- [ ] Run:

```bash
cd modules/admin
go test -gcflags=all=-l -ldflags=-s=false ./cmd/cli -run EventBus -count=1
cd ../..
bash scripts/test/contract/test-deploy-moox-eventbus.sh
```

### Task 5: Add workspace, proto, build, and release contracts

**Files:**
- Modify: `go.work`, `go.work.sum`
- Modify: `Makefile`
- Modify: `scripts/build/build.sh`
- Modify: `scripts/release/release.sh`
- Modify: `scripts/test/contract/test-release-contract.sh`
- Delete tracked Python/cache/database artifacts under `modules/strategy` if present

- [ ] First extend `scripts/test/contract/test-release-contract.sh` so it fails unless the archive contains both Strategy executables, config, worker, requirements, Python SDK, and example, with executable modes where required; it must also reject `__pycache__`, `.pytest_cache`, `*.pyc`, `*.sqlite`, and `*.db`.
- [ ] Add `modules/strategy/proto/strategygen` to `go.work` and Strategy generation to `make proto`.
- [ ] Add `strategy` and `strategy-cli` targets plus both to `all`. Package only runtime-owned assets and clean generated caches from the archive.
- [ ] Run:

```bash
make proto
./scripts/build/build.sh strategy
./scripts/build/build.sh strategy-cli
bash scripts/test/contract/test-release-contract.sh
bash -n scripts/build/build.sh scripts/release/release.sh scripts/deploy/deploy-moox.sh
```

### Task 6: Deploy, monitor, and route Strategy safely

**Files:**
- Modify: `scripts/deploy/deploy-moox.sh`
- Create: `scripts/test/contract/test-deploy-moox-strategy.sh`
- Modify: `scripts/test/contract/test-deploy-moox-preserve-disabled.sh`
- Modify applicable deploy profile tests under `scripts/test-deploy-moox-*.sh`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `modules/monitor/internal/sysdeploy/sync_test.go`

- [ ] Add shell RED tests covering default inclusion, `--no-strategy`, stable config paths, database path, isolated venv and dependency install, worker handshake before start, PID/start/stop order, signed readiness, SysDeploy activation after readiness, disabled route when omitted, and preservation of an operator-disabled component.
- [ ] Make fresh SysDeploy defaults non-routable until a successful deploy activation; after Strategy is shipped, set `monitor_enabled=true`. Never expose a dead route merely because the default row exists.
- [ ] Add deploy orchestration patterned after existing modules: build/stage/rsync, stable dirs, venv, config rewrite, process lifecycle, health. Start after EventBus/Storage/metadata and before dependents that query it.
- [ ] Implement idempotent SysDeploy state sync: successful readiness activates/monitors/routes Strategy; `--no-strategy` disables route and process without deleting operator data. Preserve explicit operator-disabled state.
- [ ] Prove Monitor automatically creates its check for active+monitor-enabled Strategy; no Strategy-specific Monitor branch.
- [ ] Run all deploy contract tests, not only the new one:

```bash
bash scripts/test/contract/test-deploy-moox-strategy.sh
bash scripts/test/contract/test-deploy-moox-eventbus.sh
bash scripts/test/contract/test-deploy-moox-preserve-disabled.sh
for test_script in scripts/test-deploy-moox-*.sh; do bash "$test_script"; done
cd modules/admin
go test -gcflags=all=-l -ldflags=-s=false ./internal/service/sysdeploy ./cmd/cli -count=1
cd ../monitor
go test ./internal/sysdeploy -count=1
```

### Task 7: Commit, review, and local acceptance

- [ ] Commit runtime, credentials, and packaging/deployment as separate units.
- [ ] Review the whole Task 1-6 range with a fresh read-only Agent. Fix all Critical and Important findings.
- [ ] Run the complete module and script suite plus a package inspection:

```bash
go test ./modules/strategy/... ./modules/admin/... ./modules/monitor/...
./scripts/build/build.sh all
VERSION=strategy-plan-check ./scripts/release/release.sh
tar -tf dist/moox-strategy-plan-check.tar.gz
```

```bash
git commit -m "feat(strategy): connect runtime outbox and readiness"
git commit -m "feat(admin): provision strategy eventbus identity"
git commit -m "feat(strategy): ship standard release and deployment"
```
