# Repository Quality and Documentation Truth Plan

> **Worker requirement:** Establish the mechanical baseline first. Every CI checker must be read-only and have a failure-path test.

**Goal:** Normalize the current repository once, enforce package boundaries/Go format/Web format/Web lint/document consistency through `make verify`, and make all architecture/release documentation describe the implemented repository.

---

### Task 1: Create a mechanical-only formatting baseline

**Files:** All files reported by the workspace-aware Go formatter and `web/src` Prettier checks; the three Vue files currently producing ESLint warnings.

- [ ] Capture the exact pre-change lists in the task report. Enumerate Go modules using `go work edit -json`; do not scan `.worktrees`, vendored code, build output, or arbitrary parent directories.
- [ ] Run `gofmt -w` only on reported Go files and Prettier `--write` only on `web/src`. Fix the five warnings in `cloud-account-manage.vue`, `function-package-manage.vue`, and `ssh-file-manager.vue` without behavior changes.
- [ ] Update every statik generation path (`web-host/Makefile`, `scripts/release/release.sh`, `scripts/deploy/deploy-moox.sh`) to immediately `gofmt -w web-host/internal/statik/statik.go`; cover this in `scripts/test/contract/test-release-contract.sh`.
- [ ] Review the diff for formatting-only semantics and commit it before any refactor:

```bash
git commit -am "style: normalize repository formatting"
```

### Task 2: Add read-only quality gates

**Files:**
- Create: `scripts/check/check-go-format.sh`
- Create: `scripts/check/check-web-format.sh`
- Create: `scripts/check/check-web-lint.sh`
- Create focused shell tests: `scripts/test/contract/test-check-go-format.sh`, `scripts/test/contract/test-check-web-quality.sh`
- Modify: `web/package.json`
- Modify: `Makefile`
- Modify if necessary: `.github/workflows/ci.yml`

- [ ] Write shell RED tests that copy a minimal valid fixture to a temp dir, make one invalid formatting/lint change, assert non-zero, restore it, then assert zero. The scripts must not change fixture or repository content.
- [ ] Implement `check-go-format.sh` using `go work edit -json` module directories and `gofmt -l`; print every offender and exit once with a clear summary.
- [ ] Add Web scripts `format:check` and `lint:check`; the latter uses ESLint `--max-warnings=0`. The shell wrappers must use `--check`/read-only modes only.
- [ ] Make `check-boundaries` run module then package boundaries. Make `verify` explicitly include boundaries, format, lint, Go test/vet, Web unit/build, release/gateway/Caddy contracts, docs consistency/build. CI should call `make verify`, not duplicate the graph.
- [ ] Prove every checker leaves `git status --porcelain` unchanged when invoked alone.

### Task 3: Make documentation mechanically consistent

**Files:**
- Create: `scripts/check-doc-consistency.mjs`
- Create: `scripts/check/check-doc-consistency.sh`
- Create: `scripts/test/contract/test-check-doc-consistency.sh`
- Modify: `README.md`, `modules/README.md`, `modules/storage/README.md`
- Create: `modules/strategy/README.md`
- Modify: `docs/架构总览.md`, `docs/大仓架构.md`, `docs/存储引擎架构.md`, `docs/策略模块架构设计.md`, `docs/策略模块Python策略接入手册.md`
- Modify applicable operations/deployment docs discovered by `rg`

- [ ] Add a managed workspace-module block to `docs/架构总览.md`. Parse `go work edit -json`, not a hard-coded module count or loose grep.
- [ ] Add RED fixture tests for a missing module, duplicate module, non-workspace entry, and the false claim that all modules live under `modules/`.
- [ ] Update Admin/Gateway descriptions, Factor status, Storage `rows_updated.v1` subject, derived-write ACK semantics, Strategy ports/runtime/assets/credentials/default Live state, and deploy/disable behavior. Search all docs for old `rows_changed.v1`, `11408`, `11418`, placeholder claims, and direct Admin proxy claims.
- [ ] Run:

```bash
bash scripts/test/contract/test-check-doc-consistency.sh
./scripts/check/check-doc-consistency.sh
pnpm docs:build
```

### Task 4: Verify, commit, and review

- [ ] Run each new checker twice and confirm `git diff` does not change.
- [ ] Run `make verify`; if the worktree-clean assertion is part of final CI rather than local development, test it in a clean temporary checkout so it does not reject the intended task diff.
- [ ] Commit gates and docs separately, then request a fresh task review for correctness, portability, and false-negative paths.

```bash
git commit -m "ci: enforce package format lint and docs checks"
git commit -m "docs: align architecture release and module facts"
```
