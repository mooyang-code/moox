# Strategy Contract and Capability Implementation Plan

> **Worker requirement:** Use test-driven-development. Do not edit production code until the covering test fails for the expected reason.

**Goal:** Reject invalid Strategy query parameters before repository access, expose an authoritative server capability for Live execution, and remove unavailable Live actions from the UI while preserving historical Live data queries.

**Scope boundary:** This plan changes RPC/API contracts and UI capability handling only. JetStream, packaging, release, and deployment belong to `2026-07-17-strategy-release.md`.

---

### Task 1: Lock strict query semantics

**Files:**
- Modify: `modules/strategy/internal/rpc/frontend_service_test.go`
- Modify: `modules/strategy/internal/rpc/frontend_service.go`
- Modify if needed: `modules/strategy/internal/domain/performance.go`

- [ ] Add a spy repository that counts run/performance calls and table tests for malformed `from`, malformed `to`, equal bounds, reversed bounds, empty bounds, one-sided bounds, non-UTC offsets, `interval` values `""`, `auto`, `daily`, and invalid values, plus valid and invalid `performance_source` values.
- [ ] Assert every invalid request returns the same stable invalid-argument `RetInfo`, returns no partial data, and never calls the repository.
- [ ] Run RED:

```bash
cd modules/strategy
go test ./internal/rpc -run 'Test(ListStrategyRuns|GetStrategyPerformance).*Invalid|TestStrictTimeRange' -count=1
```

- [ ] Implement one `parseStrictTimeRange` helper: empty is unbounded; non-empty uses RFC3339Nano; normalize valid values to UTC; when both exist require `from < to`.
- [ ] Validate interval in the RPC boundary and reuse `domain.ValidPerformanceSource` for source validation.
- [ ] Normalize pagination before repository access and return normalized page values rather than echoing invalid input.
- [ ] Run GREEN and the adjacent domain/store suite:

```bash
go test ./internal/rpc ./internal/domain ./internal/store -count=1
```

### Task 2: Add an authoritative capability contract

**Files:**
- Modify: `modules/strategy/proto/strategy.proto`
- Regenerate: `modules/strategy/proto/strategygen/**`
- Modify: `modules/strategy/internal/rpc/service.go`
- Modify: `modules/strategy/internal/rpc/frontend_service.go`
- Modify: `modules/strategy/internal/rpc/frontend_service_test.go`
- Modify: `modules/strategy/internal/bootstrap/config.go`
- Modify: `modules/strategy/internal/bootstrap/config_test.go`
- Modify: `modules/strategy/internal/bootstrap/bootstrap.go`
- Modify: `modules/strategy/config/app.yaml`

- [ ] Add a backward-free capability response to the Strategy frontend API containing at least `live_execution_enabled`; do not infer capability from UI constants or health text.
- [ ] Add RED tests proving `live_enabled: false` rejects `SetExecutionMode(live)` before any repository mutation, while observe/paper still work and historical `performance_source=live` remains queryable.
- [ ] Add config tests proving Live defaults false and explicit true is parsed and injected into `rpc.Service`.
- [ ] Regenerate the proto and implement the service capability method/field. Keep the rejection message stable and capability-specific.
- [ ] Run:

```bash
make proto
cd modules/strategy
go test ./internal/rpc ./internal/bootstrap ./test -count=1
```

### Task 3: Drive the UI from the capability

**Files:**
- Modify: `web/src/api/modules/strategy.ts`
- Modify: `web/src/store/modules/strategy.ts`
- Modify: `web/src/views/strategy/components/strategy-operation-panel.vue`
- Modify if required: `web/src/views/strategy/index.vue`
- Create or modify focused tests under `web/src/views/strategy/**/__tests__/` and `web/src/api/modules/`

- [ ] Add RED component/store tests proving the default/loading/error capability state never offers a Live command, false hides it, true offers it, and historical Live source filters remain visible.
- [ ] Fetch capability through the existing Strategy API/store lifecycle. Fail closed on network or parse errors.
- [ ] Render execution modes from the capability without deleting historical Live display/query support.
- [ ] Run the repository's real Vitest commands discovered from `web/package.json`, then the full frontend unit suite:

```bash
cd web
pnpm exec vitest run --config vitest.config.ts src/views/strategy src/api/modules
pnpm test
pnpm build:prod
```

### Task 4: Focused review and commit

- [ ] Run `gofmt` only on touched Go files and Prettier only on touched Web files.
- [ ] Record the task base SHA, commit all contract changes, generate a review package for the whole range, and request a fresh read-only task review.
- [ ] Fix all Critical and Important findings, rerun the commands above, and update `.superpowers/sdd/progress.md`.

```bash
git add modules/strategy web/src
git commit -m "fix(strategy): enforce query and execution capabilities"
```
