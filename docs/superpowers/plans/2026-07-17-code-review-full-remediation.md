# MooX Code Review Full Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate every verified code-review issue by making Storage derived writes reliable, shipping Strategy through the standard runtime path, enforcing validation and repository quality gates, restoring documentation accuracy, splitting the four maintenance hotspots, and proving the result through real deployment and end-to-end tests.

**Architecture:** Preserve Pebble as the Storage fact source and JetStream as the durable delivery source. Make ViewBuilder handlers wait for batched DuckDB/Bleve completion before transport ACK. Ship Strategy as a first-class but Live-disabled module, connect its transactional outbox to JetStream, and make release/deploy activation depend on readiness. Establish read-only quality and documentation gates before responsibility-based backend and frontend splits.

**Tech Stack:** Go 1.24, tRPC-Go, NATS JetStream, Pebble, DuckDB, Bleve, SQLite/GORM, Python worker runtime, Vue 3, TypeScript, Vitest, Playwright, pnpm, Bash, VitePress.

## Global Constraints

- This is a new project. Do not preserve compatibility with incorrect data structures, routes, ports, scripts, or documentation.
- Storage uses at-least-once delivery: derived success precedes ACK; any derived error causes Nak and retry.
- JetStream delivery objects remain inside transport infrastructure and never enter `internal/core/eventbus` or ViewBuilder business interfaces.
- Derived writes and outbox publication must be idempotent under redelivery.
- Strategy uses ports `11430` for StrategyMgr, `11431` for signed readiness, and `12930` for Prometheus.
- Strategy ships in the default release and deploy profile. `--no-strategy` must disable its SysDeploy route.
- Strategy defaults to `live_enabled: false`; backend enforcement is authoritative and the UI must not offer Live operations.
- CI checks are read-only. Never run `--fix`, `--write`, or another worktree-mutating formatter from `make verify`.
- Metrics labels use fixed low-cardinality enums. Never use message ID, Space, Dataset, View ID, binding ID, run ID, or account ID as labels.
- Refactors preserve public RPCs, configuration formats, deployment paths, API request payloads, and existing user behavior unless this plan explicitly changes them.
- Every behavior change follows red-green-refactor. Every task ends with focused tests and a separate commit.
- Completion requires independent Agent review, module E2E, full `make verify`, actual build, standard deployment, live API/browser verification, commit, push, and `main == origin/main` proof.

## Plan Set

Execute these child plans in order. A child plan is not complete until its focused review passes.

1. `2026-07-17-storage-view-reliability.md`
2. `2026-07-17-strategy-contract.md`
3. `2026-07-17-strategy-release.md`
4. `2026-07-17-quality-and-docs.md`
5. `2026-07-17-backend-hotspot-refactor.md`
6. `2026-07-17-frontend-hotspot-refactor.md`

---

### Task 1: Storage View Reliability

**Files:** See `docs/superpowers/plans/2026-07-17-storage-view-reliability.md`.

**Interfaces:**
- Produces: ViewBuilder subscriber handlers that return only after all event rows finish derived writes.
- Produces: Configured SubscriberBus delivery concurrency, heartbeat, ACK/Nak logging, and Storage derived-write metrics.
- Produces: A real JetStream failure/retry E2E under `modules/storage/test`.

- [ ] **Step 1: Execute the Storage child plan with TDD**

Run each RED and GREEN command recorded in the child plan. Preserve the terminal evidence in the task report.

- [ ] **Step 2: Run the focused Storage verification**

```bash
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./internal/service/dataviewbuilder ./internal/infra/eventbus ./internal/bootstrap/eventbus ./internal/config ./test
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -race -count=1 ./internal/service/dataviewbuilder ./internal/infra/eventbus
```

Run from `modules/storage`. Expected: PASS with no leaked goroutines, early ACK, or swallowed derived-write error.

- [ ] **Step 3: Commit and task-review the Storage unit**

```bash
git add modules/storage packages/jetstream
git commit -m "fix(storage): acknowledge view events after materialization"
```

Generate a review package from the task base commit and dispatch a fresh task reviewer. Resolve every Critical and Important finding before continuing.

### Task 2: Strategy Contract and Capability Boundary

**Files:** See `docs/superpowers/plans/2026-07-17-strategy-contract.md`.

**Interfaces:**
- Produces: `parseTimeRange(*strategypb.TimeRange) (time.Time, time.Time, error)` shared by runs and performance queries.
- Produces: strict interval and performance-source validation.
- Produces: backend and frontend Live capability enforcement driven by `live_enabled`.

- [ ] **Step 1: Execute the Strategy contract child plan with TDD**

Cover invalid RFC3339Nano, reversed/equal bounds, empty/one-sided bounds, unsupported intervals, unsupported sources, and disabled Live mode.

- [ ] **Step 2: Run Strategy RPC and Web tests**

```bash
env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/rpc ./internal/bootstrap ./test
pnpm --dir web test -- src/api/strategy.test.ts src/views/strategy
```

Expected: PASS; an invalid range never reaches repository queries and Live is unavailable when the server capability is false.

- [ ] **Step 3: Commit and task-review the Strategy contract**

```bash
git add modules/strategy web/src/api web/src/store/modules/strategy.ts web/src/views/strategy
git commit -m "fix(strategy): enforce query and execution capabilities"
```

### Task 3: Strategy Runtime, Release, and Deployment

**Files:** See `docs/superpowers/plans/2026-07-17-strategy-release.md`.

**Interfaces:**
- Consumes: strict capability behavior from Task 2.
- Produces: `strategy-eventbus` credential, shared JetStream outbox publisher, relay lifecycle, readiness details, standard artifacts, deploy orchestration, and route activation semantics.

- [ ] **Step 1: Execute the Strategy release child plan with TDD**

Implement runtime wiring before packaging. Then add build, release, deploy, SysDeploy, monitoring, and `--no-strategy` contracts.

- [ ] **Step 2: Run local integration and contract tests**

```bash
env GOCACHE=/tmp/moox-gocache go test -count=1 ./modules/strategy/...
env GOCACHE=/tmp/moox-gocache go test -count=1 ./modules/eventbus/... ./modules/admin/...
bash -n scripts/build.sh scripts/release.sh scripts/deploy-moox.sh scripts/test-release-contract.sh
bash scripts/test-release-contract.sh
bash scripts/test-deploy-moox-strategy.sh
```

Expected: PASS; default artifacts include Strategy, while `--no-strategy` produces no binary, process, or active route.

- [ ] **Step 3: Build Strategy artifacts**

```bash
./scripts/build.sh strategy
test -x bin/moox-strategy
test -x bin/moox-strategy-cli
```

- [ ] **Step 4: Commit runtime and packaging in separate reviewable commits**

```bash
git add modules/strategy modules/eventbus modules/admin
git commit -m "feat(strategy): connect runtime outbox and readiness"
git add go.work Makefile scripts README.md modules/strategy
git commit -m "feat(strategy): ship standard release and deployment"
```

Review the entire Task 3 range, not only its final commit.

### Task 4: Quality Baseline and Documentation Truth

**Files:** See `docs/superpowers/plans/2026-07-17-quality-and-docs.md`.

**Interfaces:**
- Consumes: final ports, subjects, artifact paths, and module list from Tasks 1-3.
- Produces: clean format/lint baseline, five read-only check scripts, `make verify` integration, accurate architecture documentation, and executable consistency checks.

- [ ] **Step 1: Apply the mechanical baseline in an isolated commit**

Run gofmt and Prettier only as an explicit developer action. Remove all five ESLint warnings. Confirm no semantic changes are mixed into this commit.

- [ ] **Step 2: Add read-only gates and documentation checks with failure-first script tests**

Each checker test must first create a temporary invalid fixture and prove a non-zero exit, then prove the repository passes.

- [ ] **Step 3: Run all quality gates**

```bash
./scripts/check-module-boundaries.sh
./scripts/check-package-boundaries.sh
./scripts/check-go-format.sh
./scripts/check-web-format.sh
./scripts/check-web-lint.sh
./scripts/check-doc-consistency.sh
pnpm docs:build
```

Expected: all commands exit 0 and `git status --short` is unchanged by the checks.

- [ ] **Step 4: Commit and review mechanical, gate, and documentation changes separately**

```bash
git commit -m "style: normalize repository formatting"
git commit -m "ci: enforce package format lint and docs checks"
git commit -m "docs: align architecture release and module facts"
```

### Task 5: Backend Hotspot Refactors

**Files:** See `docs/superpowers/plans/2026-07-17-backend-hotspot-refactor.md`.

**Interfaces:**
- Consumes: final Storage and Strategy runtime contracts.
- Produces: thin Storage `cmd/server`, focused Storage bootstrap units, and focused Monitor background-runtime units without behavior changes.

- [ ] **Step 1: Add characterization tests before moving code**

Lock flags, role selection, health registration, timer registration, goroutine shutdown, and Monitor startup behavior.

- [ ] **Step 2: Move one responsibility at a time and keep tests green**

Do not add an unstructured `utils.go`. Every new file must match the responsibilities named in the child plan.

- [ ] **Step 3: Verify and commit Storage and Monitor separately**

```bash
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./modules/storage/cmd/server ./modules/storage/internal/bootstrap/...
env GOCACHE=/tmp/moox-gocache go test -count=1 ./modules/monitor/internal/bootstrap ./modules/monitor/...
env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./modules/monitor/internal/bootstrap
git commit -m "refactor(storage): move server assembly into bootstrap"
git commit -m "refactor(monitor): split runtime bootstrap responsibilities"
```

### Task 6: Frontend Hotspot Refactors

**Files:** See `docs/superpowers/plans/2026-07-17-frontend-hotspot-refactor.md`.

**Interfaces:**
- Produces: focused CloudNode and View Browse components/composables with unchanged API payloads and visible behavior.

- [ ] **Step 1: Add characterization tests for payload and interaction contracts**

Cover filtering, paging, sorting, selection, dialogs, error state, and detail navigation before extracting code.

- [ ] **Step 2: Extract composables before presentation components**

Keep page-level route/tab coordination in the page entry and domain state in named composables.

- [ ] **Step 3: Run frontend tests, build, and Playwright visual acceptance**

```bash
pnpm --dir web test
pnpm --dir web build:prod
pnpm --dir web exec playwright test
```

Expected: PASS at desktop and mobile viewports with no blank screens, overlap, or request-shape regression.

- [ ] **Step 4: Commit and task-review each page refactor**

```bash
git commit -m "refactor(web): split cloud node workbench responsibilities"
git commit -m "refactor(web): split view browse query responsibilities"
```

### Task 7: Broad Independent Code Review

**Files:** Review every commit after this plan's merge base.

- [ ] **Step 1: Generate one whole-range review package**

Use the recorded base commit, not `HEAD~1`.

```bash
/Users/mooyang/.codex/plugins/cache/superpowers-marketplace/superpowers/6.1.1/skills/subagent-driven-development/scripts/review-package <base-sha> HEAD
```

- [ ] **Step 2: Dispatch a fresh high-judgment Agent**

Require the reviewer to inspect Storage delivery semantics, shutdown, Strategy artifacts/runtime/routes, read-only gates, documentation claims, and refactor behavior. The reviewer must be read-only.

- [ ] **Step 3: Fix every Critical and Important finding and re-review**

Use one fix task for the complete findings list. Re-run the covering tests and append evidence to the review report before re-review.

### Task 8: End-to-End Build, Deployment, and Runtime Acceptance

**Files:**
- Create or modify: `modules/storage/test/view_delivery_e2e_test.go`
- Create or modify: `modules/strategy/test/deployment_e2e_test.go`
- Create: `scripts/test-deploy-moox-strategy.sh`
- Modify: Playwright specifications under `web/e2e/` or the repository's existing browser test location.

- [ ] **Step 1: Run the complete local gate**

```bash
make verify
git status --short
```

Expected: `make verify` exits 0 and status contains no generated or formatter changes.

- [ ] **Step 2: Build the complete release**

```bash
./scripts/build.sh all
VERSION=review-remediation ./scripts/release.sh
```

Expected: all binaries compile; the release contains Strategy runtime assets and no Python/cache/database artifacts.

- [ ] **Step 3: Deploy using the standard script**

Use the real target and existing approved MooX deployment inputs. Do not manually copy missing files. Capture the deploy log and signed readiness results.

- [ ] **Step 4: Run Storage failure recovery acceptance**

Inject one deterministic DuckDB failure and one Bleve failure in the E2E harness. Verify Nak/redelivery, final ACK, and View/Pebble reconciliation.

- [ ] **Step 5: Run Strategy runtime acceptance**

Verify signed readiness, Gateway `ListRunningStrategies`, CLI validate/run-once, stable-ID outbox publication, and published-state convergence.

- [ ] **Step 6: Run browser acceptance**

Verify Strategy, CloudNode, and View Browse on desktop and mobile. Confirm no API errors, blank canvases, overlap, or hidden unavailable capability.

### Task 9: Final Commit, Push, and Completion Audit

- [ ] **Step 1: Audit every design requirement against authoritative evidence**

Use `docs/superpowers/specs/2026-07-17-code-review-full-remediation-design.md` as the checklist. Record the exact test, file, runtime response, or screenshot that proves each requirement.

- [ ] **Step 2: Commit any final acceptance-only changes**

```bash
git add -A
git commit -m "test: complete remediation end to end acceptance"
```

- [ ] **Step 3: Push and prove branch synchronization**

```bash
git push origin main
git rev-parse HEAD
git rev-parse origin/main
git status --short --branch
```

Expected: the two revisions are identical and the worktree is clean.
