# MooX Crypto And Style Unification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Unify the shared encryption implementation and remove the main code-organization inconsistencies identified in the MooX review, without preserving the old encryption format or old import paths.

**Architecture:** Make `packages/crypto` the single security utility package for MooX: AES-GCM encryption, bcrypt password hashing, HS256 JWT signing/verification, cryptographically random salts, and secret masking. Keep business authorization decisions and claim semantics inside admin, while all cryptographic mechanics and security formatting live in the shared package. Standardize directory names and generated-proto module paths by changing source, `.proto` `go_package`, Makefiles, imports, and generated output together.

**Tech Stack:** Go 1.24 workspace, tRPC generated Go/protobuf code, AES-256-GCM, bcrypt, HS256 JWT, Vue/WebCrypto password transport, Cobra, `go test`, `gofmt`, and repository boundary scripts.

---

## Conventions To Enforce

- Shared primitives live under `packages/<name>` and must not import `modules/*`.
- All cryptographic/security helpers live in `packages/crypto`; modules provide configuration and business policy only.
- `Encrypt`/`Decrypt` mean reversible AES-GCM encryption; `HashPassword`/`VerifyPassword` mean one-way password hashing; `SignToken`/`ParseToken` mean JWT signing and verification. These APIs must not be conflated.
- Business service directories use singular `internal/service`.
- Module-level end-to-end tests use `test/`; unit tests remain beside the package under test.
- Context helpers use `internal/spacecontext`.
- Generated proto directories use `<module>gen`; generated Go package names use `<module>pb`.
- Provider directories use Go-style names without hyphens; persisted provider values such as `tencent-scf` remain unchanged.
- `cmd/<binary>/main.go` is an entry point; reusable CLI command code belongs under `internal/command`.
- `internal/health` remains a module adapter. Common HTTP/readiness behavior stays in `packages/healthz`; module-specific metrics and gateway routing stay local.

## Task 1: Freeze The Baseline And Add The Shared Crypto Contract

**Files:**
- Create: `packages/crypto/go.mod`
- Create: `packages/crypto/crypto.go`
- Create: `packages/crypto/crypto_test.go`
- Modify: `go.work`

- [x] **Step 1: Record the current callers and define the new API.**

  The package exposes this small, unified security API:

  ```go
  func Encrypt(plaintext, secret string) (string, error)
  func Decrypt(ciphertext, secret string) (string, error)
  func HashPassword(password string) (string, error)
  func VerifyPassword(password, encodedHash string) bool
  func SignToken(claims map[string]any, secret, issuer string, ttl time.Duration) (string, error)
  func ParseToken(token, secret string) (map[string]any, error)
  func NewSalt() (string, error)
  func MaskSecret(value string, prefix, suffix int) string
  ```

  `Encrypt` and `Decrypt` derive a fixed 32-byte AES key with SHA-256 over the supplied secret, then use AES-GCM. The encoded payload is `nonce || ciphertext || tag` in standard base64. The browser password flow uses the same API with `secret = salt + decimal(timestamp)`, so no raw-key API is needed. `HashPassword` uses bcrypt, which embeds its own salt. `SignToken` and `ParseToken` use HS256 only and return generic claims; admin remains responsible for checking `token_type`, role, and user identity. `MaskSecret` is a shared presentation helper for secrets, not an encryption primitive.

- [x] **Step 2: Add the package module and workspace entry.**

  Use module path `github.com/mooyang-code/moox/packages/crypto`, requiring `github.com/golang-jwt/jwt/v5` and `golang.org/x/crypto/bcrypt`. Add `./packages/crypto` to `go.work`.

- [x] **Step 3: Write failing tests for the contract.**

  Cover round trips for arbitrary-length secrets, random nonce output, wrong-key failure, malformed base64, truncated ciphertext, empty plaintext rejection, bcrypt password verification and wrong-password rejection, HS256 signing and tampered-token rejection, random salt generation, and masking edge cases.

- [x] **Step 4: Implement AES-GCM once.**

  Keep key normalization and payload parsing private. Return errors that distinguish invalid input, truncated ciphertext, and authentication failure. Do not copy either existing module implementation into the new package unchanged.

- [x] **Step 5: Run the package tests.**

  Run:

  ```bash
  go test -count=1 ./packages/crypto
  ```

  Expected: all new crypto contract tests pass before any module caller is migrated.

## Task 2: Migrate Admin And Trade Security Callers

**Files:**
- Modify: `modules/admin/internal/service/secret/dao/secret.go`
- Modify: `modules/admin/internal/service/ssh/dao/ssh_host.go`
- Modify: `modules/admin/internal/gateway/authorize.go`
- Modify: `modules/admin/internal/service/auth/impl/login.go`
- Modify: `modules/admin/internal/service/auth/impl/user.go`
- Modify: `modules/admin/internal/service/auth/impl/password.go`
- Modify: `modules/admin/internal/service/auth/dao/user.go`
- Modify: `modules/admin/internal/service/auth/model/user.go`
- Modify: `modules/admin/schema/admin.sql`
- Create: `web/src/utils/crypto.test.ts`
- Delete: `modules/admin/internal/common/crypto/aes.go`
- Delete: `modules/admin/internal/common/crypto/key.go`
- Move tests: `modules/admin/internal/common/crypto/aes_test.go` and `key_test.go` to the new owning packages
- Modify: `modules/trade/internal/service/dao/api_key.go`
- Delete: `modules/trade/internal/common/crypto/aes.go`
- Move tests: `modules/trade/internal/common/crypto/aes_test.go` to `modules/trade/internal/service/dao`
- Modify: `web/src/utils/crypto.ts`

- [x] **Step 1: Replace all reversible encryption callers.**

  Change admin secret/SSH DAO and trade API-key DAO to import `packages/crypto` and call `crypto.Encrypt`/`crypto.Decrypt`. The same AES-GCM format is used for all stored secrets; module-specific environment loading remains outside the package.

- [x] **Step 2: Simplify the browser-password encryption flow.**

  Change the admin auth implementation to call `crypto.Decrypt(ciphertext, salt+strconv.FormatInt(timestamp, 10))`. Remove `DeriveEncryptionKey`, `AESDecryptWithKey`, and the module-local `DecryptPassword` crypto implementation. Keep salt existence, timestamp expiry, login-attempt limits, and auth error handling in `modules/admin/internal/service/auth/impl`.

- [x] **Step 3: Make the browser implementation use the same single encryption rule.**

  In `web/src/utils/crypto.ts`, derive AES-256-GCM from `salt + timestamp` exactly once and use the same `nonce || ciphertext || tag` base64 payload as Go. Remove the separate raw-key and fallback implementations. Add `web/src/utils/crypto.test.ts` and a Go test vector covering one fixed password/salt/timestamp payload.

- [x] **Step 4: Replace admin password storage with the shared password API.**

  Change registration and password-change code to call `crypto.HashPassword(password)` and login validation to call `crypto.VerifyPassword(decryptedPassword, user.PasswordHash)`. Remove the persistent `User.Salt` field and `c_salt` schema column; the login/change-password salt remains the short-lived cache value used only for browser transport. No old SHA-256 password hashes or old ciphertexts are supported because this is a new project.

- [x] **Step 5: Use generic JWT signing and secret masking from the shared package.**

  Replace admin-specific `GenerateAccessToken`/`ParseToken` mechanics with `crypto.SignToken`/`crypto.ParseToken` using generic claim maps. Keep admin validation of `token_type`, role, user ID, issuer, and authorization in admin code. Replace trade `MaskAPIKey` with `crypto.MaskSecret(apiKey, 4, 4)` and remove the module-local masking implementation.

- [x] **Step 6: Keep configuration and business policy out of `packages/crypto`.**

  Keep `GetEncryptionKey` in an admin security/config package because it reads `MOOX_ADMIN_ENCRYPTION_KEY`. Keep JWT claim construction, role checks, password transport validation, and API-key field selection in their owning modules. `packages/crypto` contains algorithms and generic data formatting only.

- [x] **Step 7: Update module dependencies and delete obsolete local crypto files.**

  Add the `packages/crypto` requirement and local `replace` to `modules/admin/go.mod` and `modules/trade/go.mod`. Delete `modules/admin/internal/common/crypto` and `modules/trade/internal/common/crypto` after all callers and tests import the shared package. Do not add compatibility aliases for old import paths or old ciphertext formats.

- [x] **Step 8: Verify the unified security API.**

  Run:

  ```bash
  go test -count=1 ./packages/crypto ./modules/admin/internal/service/auth/... ./modules/admin/internal/service/secret/... ./modules/admin/internal/service/ssh/... ./modules/trade/internal/service/dao/...
  cd web && pnpm test:unit -- src/utils/crypto.test.ts
  cd ..
  ```

  Expected: admin login, password change, secret DAO, SSH DAO, and trade API-key round trips all pass with the new format.

## Task 3: Extract The Eight Identical Metrics Reporters

**Files:**
- Create: `packages/report/go.mod`
- Create: `packages/report/config.go`
- Create: `packages/report/handler.go`
- Create: `packages/report/handler_test.go`
- Modify: `go.work`
- Modify: `modules/admin/internal/bootstrap/bootstrap.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/eventbus/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `modules/storage/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`

- [x] **Step 1: Move the exact common implementation into `packages/report`.**

  Move `Config`, `DefaultConfig`, `Publisher`, `Connector`, `Handler`, snapshot building, gzip limits, and JetStream publication into the package. Keep the package dependent only on `packages/jetstream`, `packages/messagepb`, `packages/metricspb`, Prometheus, protobuf, and tRPC logging.

- [x] **Step 2: Keep module bootstraps thin.**

  Each bootstrap constructs `report.NewHandler(report.DefaultConfig("<service-name>"))`. Module-specific service naming remains at the call site; no module imports another module's report package.

- [x] **Step 3: Add package-level tests before deleting copies.**

  Port the existing handler tests once to `packages/report`, covering snapshot limits, gzip output, sequence/message IDs, publisher reuse, and publisher errors. Add one compile-only migration test per module if needed to prove each bootstrap uses the shared package.

- [x] **Step 4: Remove all eight `modules/*/internal/report` copies and update module dependencies.**

  Remove the duplicate `config.go`, `handler.go`, and tests after all callers compile. Add `packages/report` to each affected module's `go.mod` and local replacement.

- [x] **Step 5: Verify the extraction.**

  Run:

  ```bash
  go test -count=1 ./packages/report ./modules/admin/... ./modules/cloudnode/... ./modules/collector/... ./modules/eventbus/... ./modules/factor/... ./modules/monitor/... ./modules/storage/... ./modules/trade/...
  rg --files modules | rg '/internal/report/(config|handler)(\.go|_test\.go)$'
  ```

  Expected: the second command returns no module-local report implementation files.

## Task 4: Standardize Ordinary Directory Names And Small Utility Packages

**Files:**
- Rename: `modules/collector/internal/sources/exchangetypes` -> `modules/collector/internal/sources/exchange`
- Rename: `modules/cloudnode/internal/providers/tencent-scf` -> `modules/cloudnode/internal/providers/tencentscf`
- Rename: `modules/admin/internal/gateway/spacecontext` -> `modules/admin/internal/spacecontext`
- Rename: `modules/storage/internal/services` -> `modules/storage/internal/service`
- Rename: `modules/storage/tests` -> `modules/storage/test`
- Split: `modules/admin/internal/common/idgen.go` -> `modules/admin/internal/idgen/idgen.go`
- Split: `modules/admin/internal/common/network.go` -> `modules/admin/internal/network/ip.go`
- Split: `modules/admin/internal/common/softdelete.go` -> `modules/admin/internal/softdelete/flag.go`
- Split: `modules/admin/internal/service/auth/utils/helper.go` -> auth-specific files under `modules/admin/internal/service/auth`

- [x] **Step 1: Rename directories with `git mv` and update imports.**

  Use package declarations that match the new directory names. Keep the external provider string `tencent-scf` unchanged in store rows, config, tests, and RPC behavior.

- [x] **Step 2: Move admin gateway context helpers to module-level `internal/spacecontext`.**

  Update gateway imports and preserve the existing HTTP header injection/filter behavior. Do not move this package into `packages`, because it is tied to admin gateway transport behavior.

- [x] **Step 3: Split admin `common` by responsibility.**

  Update all imports in auth, secret, SSH, space, and sysdeploy code. Keep the collector model types under a model-specific namespace; do not move domain types into a generic shared package only to satisfy naming symmetry.

- [x] **Step 4: Move storage E2E tests from `tests/` to `test/`.**

  Keep unit tests in their current package directories. Update scripts, README references, and CI commands that mention `modules/storage/tests`.

- [x] **Step 5: Verify the naming migration.**

  Run:

  ```bash
  gofmt -w modules/admin/internal modules/cloudnode/internal modules/collector/internal modules/storage/internal
  go test -count=1 ./modules/admin/... ./modules/cloudnode/... ./modules/collector/... ./modules/storage/...
  rg 'exchangetypes|providers/tencent-scf|internal/gateway/spacecontext|modules/storage/tests|internal/services' modules scripts docs --glob '!*.md' --glob '*.go' --glob '*.sh'
  ```

  Expected: no stale import paths remain and all affected module tests pass.

## Task 5: Standardize Generated Proto Module And Package Names

**Files:**
- Modify: `modules/*/proto/*.proto` `go_package` options
- Rename: `modules/storage/proto/gen` -> `modules/storage/proto/storagegen`
- Modify: `modules/storage/proto/Makefile`
- Modify: `modules/storage/proto/storagegen/go.mod`
- Modify: `modules/storage/proto/storagegen/go.sum`
- Modify: `modules/*/proto/Makefile`
- Modify: all affected module `go.mod` and `go.work` entries
- Regenerate: all `modules/*/proto/*gen/*.pb.go` and `*.trpc.go`

- [x] **Step 1: Define the generated names.**

  Use these `go_package` suffixes and package names: `adminpb`, `cloudnodepb`, `collectorpb`, `eventbuspb`, `factorpb`, `hostagentpb`, `monitorpb`, `storagepb`, `strategypb`, and `tradepb`; use directory/module suffixes `admingen`, `cloudnodegen`, `collectorgen`, `eventbusgen`, `factorgen`, `hostagentgen`, `monitorgen`, `storagegen`, `strategygen`, and `tradegen`.

- [x] **Step 2: Update proto sources and generation Makefiles.**

  Change `go_package` in every source proto before generation. Update each `OUTPUT_DIR` and storage's module path. Do not manually edit generated files.

- [x] **Step 3: Regenerate all generated code.**

  Run:

  ```bash
  for dir in modules/admin/proto modules/cloudnode/proto modules/collector/proto modules/eventbus/proto modules/factor/proto modules/hostagent/proto modules/monitor/proto modules/storage/proto modules/strategy/proto modules/trade/proto; do
    make -C "$dir" clean all
  done
  ```

- [x] **Step 4: Update all Go imports and module replacements.**

  Replace old `modules/storage/proto/gen` imports and old `mooxpb` imports with the standardized paths and package names. Update CLI, archive, storage, admin, and trade callers explicitly.

- [x] **Step 5: Verify generated contracts.**

  Run:

  ```bash
  go test -count=1 ./modules/... ./packages/...
  bash scripts/check-module-boundaries.sh
  git diff --check
  ```

  Expected: all generated modules compile, tRPC service names remain unchanged, and the boundary check passes.

## Task 6: Normalize CLI And Configuration File Organization

**Files:**
- Create: `modules/cli/internal/command`
- Move: `modules/cli/cmd/{auth.go,collector.go,data.go,data_remote.go,metadata.go,storage.go,storage_import.go,tencent_ops.go,tencent_ops_firewall_open.go}`
- Modify: `modules/cli/cmd/root.go`
- Keep: `modules/cli/cmd/moox-cli/main.go`
- Split: `modules/storage/internal/infra/device/duckdb/view_store.go`
- Split: `modules/storage/internal/infra/metadata/sqlite/crud.go`
- Split: `modules/storage/cmd/bench/main.go`
- Split: `modules/cli/internal/command/metadata.go`
- Modify: `modules/eventbus/internal/config/config.go`

- [x] **Step 1: Move CLI command construction behind an internal command package.**

  Keep Cobra command registration and command behavior unchanged. `cmd/moox-cli/main.go` remains the binary entry point; `cmd/root.go` becomes a thin exported `Execute(versionInfo)` adapter. Put metadata, storage import, collector, auth, data, and Tencent operations into focused files under `modules/cli/internal/command`.

- [x] **Step 2: Split storage files by responsibility, not by arbitrary line count.**

  Split `view_store.go` into DDL/schema, row writes, query/read, and lifecycle/pool files. Split `crud.go` by metadata entity and transaction helpers. Split the benchmark main into scenario definitions, runners, result formatting, and the thin `main.go` orchestration file.

- [x] **Step 3: Split EventBus configuration by subsystem.**

  Keep the public `Config` composition in `config.go`, and move broker, TLS/auth, stream/topic, consumer, KV, validation, and defaults into focused files within the same package. No runtime configuration keys may change.

- [x] **Step 4: Verify CLI and storage behavior.**

  Run:

  ```bash
  go test -count=1 ./modules/cli/... ./modules/storage/...
  go build ./modules/cli/cmd/moox-cli
  go test -count=1 ./modules/eventbus/internal/config
  ```

  Expected: CLI command names/flags, storage benchmark output, and EventBus config parsing remain unchanged.

## Task 7: Add Enforceable Checks And Finish Repository Hygiene

**Files:**
- Modify: `scripts/check-module-boundaries.sh`
- Create: `scripts/check-package-boundaries.sh`
- Modify: `.gitignore` only if a new generated/runtime artifact is found
- Modify: affected README and architecture documents

- [x] **Step 1: Add package-level boundary rules.**

  Check imports using `go list -json` or Go AST, with explicit allowed directions: `service -> domain`, `service -> infra`, `infra -> domain`, and `cmd -> internal`; reject `domain -> infra`, `domain -> service`, and cross-business-module imports. Keep generated proto imports and `packages/*` exceptions explicit.

- [x] **Step 2: Add naming checks.**

  Fail on new `internal/services`, `internal/gateway/spacecontext`, `proto/gen`, `providers/tencent-scf`, and module-root `tests` directories. Do not fail on external persisted identifiers such as the string `tencent-scf`.

- [x] **Step 3: Clean only ignored local artifacts.**

  Remove existing ignored `*.test`, `cover*.out`, and `*.tmp` artifacts after confirming with `git status --ignored`. Do not delete tracked files or unrelated user changes.

- [x] **Step 4: Update documentation and verify the full workspace.**

  Run:

  ```bash
  while IFS= read -r dir; do
    (cd "$dir" && go test -count=1 ./...)
  done < <(awk '/^[[:space:]]+\.\// {print substr($1, 3)}' go.work)
  while IFS= read -r dir; do
    (cd "$dir" && go vet ./...)
  done < <(awk '/^[[:space:]]+\.\// {print substr($1, 3)}' go.work)
  bash scripts/check-module-boundaries.sh
  bash scripts/check-package-boundaries.sh
  git diff --check
  git status --short
  ```

  Expected: the workspace passes tests and static checks, no old naming paths remain, and the final diff contains no generated runtime artifacts.

## Commit And Review Order

- [x] Commit 1: `refactor(crypto): centralize AES-GCM primitives`
- [x] Commit 2: `refactor(report): share metrics reporter implementation`
- [x] Commit 3: `refactor: standardize module directory names`
- [x] Commit 4: `refactor(proto): standardize generated package layout`
- [x] Commit 5: `refactor(cli): move commands behind internal boundary`
- [x] Commit 6: `chore: enforce package boundaries and clean artifacts`
- [x] Run an independent code review after Commit 3 and again after Commit 6.

## Scope Boundaries

- Do not delete `internal/health` adapters; common behavior is already centralized in `packages/healthz`, while admin and eventbus have module-specific behavior.
- Do not change tRPC service names, protobuf field numbers, persisted provider identifiers, or CLI command/flag names. Removing the unused fresh-project `c_salt` column is intentional.
- Do not add compatibility shims for the old AES ciphertext format or old Go import paths.
- Do not put admin claim semantics, role authorization, environment-specific key loading, or API-key field selection into `packages/crypto`; only the reusable cryptographic mechanics belong there.
