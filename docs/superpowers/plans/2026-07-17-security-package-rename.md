# Security Package Rename Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Atomically rename the shared Go security-primitives module from `packages/crypto` to `packages/security` without changing its API, data formats, or runtime behavior.

**Architecture:** Move the module as one unit, change its Go package declaration and module identity, then update every workspace consumer and active document. Do not retain an old-path forwarding module or compatibility alias. Historical plans remain unchanged and are excluded from the old-name acceptance scan.

**Tech Stack:** Go 1.24 workspaces, Go modules, bcrypt, AES-GCM, JWT, repository quality gates.

---

## File Map

- Rename `packages/crypto/` to `packages/security/`; preserve all implementation and test behavior.
- Modify `packages/security/go.mod` and `packages/security/crypto.go` for the new module and package identities.
- Modify `go.work` and every affected workspace `go.mod` to use `packages/security`.
- Modify all Go import sites to use the new path and the `mooxsecurity` alias.
- Modify `docs/架构总览.md` to publish the new shared-module name.

### Task 1: Rename The Shared Module

**Files:**
- Rename: `packages/crypto/` -> `packages/security/`
- Modify: `packages/security/go.mod`
- Modify: `packages/security/crypto.go`
- Modify: `packages/security/crypto_test.go`
- Modify: `go.work`

- [x] **Step 1: Lock the new package identity in the existing tests**

Move the existing tests with the implementation and change their declaration to:

```go
package security
```

The test cases and expected ciphertext, hash, JWT, random, password, and masking behavior must remain byte-for-byte unchanged.

- [x] **Step 2: Move the directory and update identities**

Rename the directory, then set:

```go
// Package security contains MooX's shared security primitives.
package security
```

and:

```go
module github.com/mooyang-code/moox/packages/security
```

Replace `./packages/crypto` with `./packages/security` in `go.work`. Do not leave a `packages/crypto` directory.

- [x] **Step 3: Run the renamed package tests**

```bash
cd packages/security
env GOCACHE=/tmp/moox-gocache go test -count=1 ./...
env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...
```

Expected: both commands pass and the Race Detector reports no race.

### Task 2: Migrate All Consumers And Module Graphs

**Files:**
- Modify: every non-historical Go file importing `github.com/mooyang-code/moox/packages/crypto`
- Modify: `modules/{admin,cli,collector,gateway,hostagent,monitor,strategy,trade}/go.mod`
- Modify: `packages/{cloudruntime,gatewayauth,healthz,requestauth}/go.mod`

- [x] **Step 1: Update source imports and identifiers**

Replace every source import with:

```go
mooxsecurity "github.com/mooyang-code/moox/packages/security"
```

Replace each `mooxcrypto.` selector with `mooxsecurity.`. Do not rename business values such as the `crypto` market, dataset subjects, or metadata filenames.

- [x] **Step 2: Update direct and transitive module references**

Replace module requirements and local replacements with the new module path and directory. Relative replacements must continue to point at the same physical workspace level, for example:

```go
replace github.com/mooyang-code/moox/packages/security => ../../packages/security
```

or, for sibling packages:

```go
replace github.com/mooyang-code/moox/packages/security => ../security
```

- [x] **Step 3: Normalize affected modules**

Run `go mod tidy` in `packages/security`, direct consumers, and workspace modules that carried the old module transitively. For independently consumable packages, run it with `GOWORK=off` and verify `go test -mod=readonly` also succeeds with `GOWORK=off`. The command must not reintroduce `packages/crypto`. Preserve the workspace's effective post-split `google.golang.org/genproto` selection in `go.work`, rather than relying on an otherwise unnecessary requirement in one leaf module.

- [x] **Step 4: Run focused consumer tests**

```bash
(cd packages/gatewayauth && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd packages/requestauth && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/admin && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/cli && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/trade && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
```

Expected: all commands pass.

### Task 3: Update Active Documentation And Static Contracts

**Files:**
- Modify: `docs/架构总览.md`

- [x] **Step 1: Publish the new module name**

Change the active architecture map entry to:

```markdown
| `packages/security` | 共享安全基础能力 |
```

Keep historical files under `docs/superpowers/plans/` unchanged.

- [x] **Step 2: Run the old-name acceptance scan**

```bash
if rg -n 'github.com/mooyang-code/moox/packages/crypto|mooxcrypto|packages/crypto' . \
  --glob '!docs/superpowers/plans/**' \
  --glob '!docs/superpowers/specs/2026-07-17-security-package-rename-design.md' \
  --glob '!docs/superpowers/plans/2026-07-17-security-package-rename.md'; then
  exit 1
fi
test ! -e packages/crypto
```

Expected: no matches and the old directory does not exist.

- [x] **Step 3: Run documentation and architecture contracts**

```bash
bash scripts/test-docs-architecture.sh
git diff --check
```

Expected: both commands pass.

### Task 4: Full Verification, Independent Review, And Delivery

- [x] **Step 1: Run the complete repository gate**

```bash
make verify
```

Expected: all Go tests/vet, frontend tests/build, formatting, lint, release/deployment contracts, and documentation build pass.

- [x] **Step 2: Start a new independent review Agent**

Ask the reviewer to compare the implementation with the design and this plan, prioritizing missed old-path references, package-boundary drift, module-graph errors, behavior changes, and missing tests. The reviewer must not edit files.

- [x] **Step 3: Remediate every actionable finding**

Apply fixes, rerun the affected focused tests, and request a final read-only confirmation from the reviewer. Do not dismiss findings solely because `make verify` passes.

- [x] **Step 4: Commit and push all changes**

```bash
git add -A
git commit -m "refactor: rename crypto package to security"
git push
git status --short
git rev-parse HEAD
git rev-parse origin/main
```

Expected: the push succeeds, the worktree is clean, and `HEAD` equals `origin/main`.

## Acceptance Checklist

- [x] `packages/security` is the only shared security-primitives module.
- [x] `packages/crypto` no longer exists and no compatibility module remains.
- [x] Package declaration, module path, imports, aliases, requirements, replacements, workspace entry, and active docs use the new name.
- [x] Security public APIs and persisted formats are unchanged.
- [x] Cryptocurrency-market terminology and business identifiers are untouched.
- [x] Focused tests, Race tests, static scans, architecture contracts, and `make verify` pass.
- [x] A newly started Agent reviews the completed implementation and all actionable findings are fixed.
- [x] Task changes are committed and pushed, and `HEAD == origin/main`; unrelated concurrent release-matrix work remains untouched in the shared worktree.
