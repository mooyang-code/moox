# Caddy HTTPS Edges, Request Signing, and Health Authentication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the MooX management console and backend service gateway through two route-isolated Caddy HTTPS listeners, require a 24-hour JWT session plus replay-resistant HMAC signing for every management operation, preserve service HMAC for backend traffic, isolate and authenticate all health endpoints, automate Caddy and private-CA provisioning during deployment, and update the repository's security documentation and reusable MooX skill.

**Architecture:** A rootless, pinned Caddy binary owns two public HTTPS listeners. The browser listener on `:9527` proxies `/api/admin/*` to Admin control on `127.0.0.1:11000` and all non-API site paths to the static-only web-host on `127.0.0.1:9528`. The backend listener on `:11001` proxies only `/api/service/*` to Admin service on `127.0.0.1:11002`; backend and SCF callers therefore avoid the browser path while still receiving TLS protection over public networks. Both listeners reject unrelated API and diagnostic routes. Caddy `tls internal` manages the private CA and leaf certificates in persistent deployment data. Login issues a 24-hour JWT containing a session ID plus an independent request-signing key; browser clients persist the JWT and a non-exportable Web Crypto HMAC key and sign each management request. Health handlers stay on dedicated internal ports, require a separate Monitor HMAC credential, and use no database dependency.

**Tech Stack:** Go 1.24, Caddy v2.11.4, Caddyfile, `net/http`, AES-GCM, HMAC-SHA256, BadgerDB, JWT v5, tRPC-Go, Vue 3, TypeScript, Axios, Web Crypto, IndexedDB, Bash deployment scripts.

---

## Fixed Security Decisions

- Public browser URL: `https://<server-ip>:9527`; Caddy proxies site traffic to web-host and `/api/admin/*` to Admin control.
- Backend service URL: `https://<server-ip>:11001/api/service/*`; Caddy proxies it directly to the dedicated Admin service listener, independently of web-host and Admin control.
- Public browser listener rejects `/api/service/*`, `/healthz`, `/readyz`, and `/metrics`. Public service listener rejects everything except `/api/service/*`.
- Browser API boundary: every MooX management request, including login, Storage, Trade, SSH WebSocket, and SFTP, uses same-origin `/api/admin/*`. The browser never calls `/api/service/*` or Admin port `11000` directly. A browser may call an external pre-signed object-storage URL returned by an authenticated Admin RPC; that upload is not a MooX API request and must not carry MooX credentials.
- Service API boundary: `/api/service/*` is machine-to-machine only for Collector, SCF/CloudRuntime, Trade secret access, service discovery, status reporting, and explicitly service-authenticated CLI workflows. It never accepts browser JWT as a substitute for service HMAC.
- Unauthenticated Admin RPCs: `/api/admin/auth/GetLoginSalt` and `/api/admin/auth/Login` only.
- Removed RPCs: public `Register`, `GetChangePasswordSalt`, and `ChangePassword`.
- Initial and reset passwords: local `moox-admin-cli user ensure/reset-password`, with password read from stdin or a `0600` file, never a command-line argument.
- Session lifetime: absolute 24 hours from login; requests do not extend it and there is no refresh token.
- Request signing: client-generated random nonce, timestamp, body hash, HMAC-SHA256, and server-side atomic nonce consumption.
- Raw SSH/SFTP routes: obtain a 60-second one-time ticket through a normally signed management RPC; never put JWT or the session HMAC key in a URL.
- Health listener: a dedicated non-public port per process, normally bound to `127.0.0.1`.
- Health authentication: a dedicated `moox-health-v1` access key and secret, independent from JWT, browser session keys, and `/api/service/*` credentials.
- Certificate model: Caddy `tls internal` creates and renews a private CA and leaf certificates. Caddy data persists across deployment and data resets unless CA rotation is explicitly requested.
- Certificate trust: deployment exposes a safe root-CA retrieval workflow. Browser machines and every backend/SCF client that connects to `:11001` must trust that CA; no caller may disable TLS verification.
- Trust automation: deployment automatically configures the target host and deployed backend clients to use the generated Caddy CA. On the operator's desktop, interactive deployment offers a one-confirmation CA install; non-interactive deployment requires an explicit trust flag. The script must never silently modify a workstation trust store or claim to configure browser machines on which it is not running.
- Caddy distribution: Caddy is a MooX runtime prerequisite managed by the MooX skill's initial-deployment workflow. Pin v2.11.4, verify the official release checksum before installation, run from the deployment root without `apt`, `sudo`, or a system-wide service, and never resolve an unpinned `latest` artifact.
- Caddy ownership: every deployment that enables web-host must inspect the target before changing it. Reuse only the MooX-managed Caddy instance under `${DEPLOY_DIR}`; never overwrite, stop, reconfigure, or adopt an unrelated system/package-manager Caddy process.
- `internal/health` packages remain as thin module adapters. Shared routing and authentication live under `packages/healthz`.

## Target Ports

| Surface | Default bind | Exposure |
|---|---|---|
| Caddy browser edge | `0.0.0.0:9527` HTTPS | Public |
| Web-host static upstream | `127.0.0.1:9528` HTTP | Caddy only |
| Web-host health | `127.0.0.1:19527` HTTP + health HMAC | Internal |
| Admin control gateway | `127.0.0.1:11000` HTTP | Caddy browser edge only |
| Caddy service edge | `0.0.0.0:11001` HTTPS + service HMAC | Backend/SCF only |
| Admin service gateway | `127.0.0.1:11002` HTTP + service HMAC | Caddy service edge/local callers only |
| Admin health | `127.0.0.1:11010` HTTP + health HMAC | Internal |
| Other module health listeners | Existing dedicated port, explicitly loopback-bound | Internal |

## Request-Signing Protocol

Canonical bytes use UTF-8 with newline separators:

```text
moox-request-v1
<UPPERCASE_METHOD>
<ESCAPED_PATH_WITHOUT_SCHEME_OR_HOST>
<LOWERCASE_SHA256_HEX_OF_EXACT_BODY_BYTES>
<UNIX_SECONDS>
<LOWERCASE_32_BYTE_NONCE_HEX>
```

Browser request headers:

```text
Authorization: <JWT>
X-Access-Token: <JWT>
X-Moox-Timestamp: <unix-seconds>
X-Moox-Nonce: <64-lowercase-hex>
X-Moox-Signature: <64-lowercase-hex>
```

The JWT `sid` claim selects the server-side signing session. The server accepts at most 60 seconds of clock skew, verifies the HMAC in constant time, and atomically records `session_nonce:<sid>:<nonce>` for 2 minutes. Any missing, malformed, expired, or repeated value returns the same `401` response.

## Health-Signing Protocol

```text
X-Moox-Health-Auth: moox-health-v1/<access-key>/<timestamp>/<nonce>/<signature>
```

The signature uses the same canonical request format with an empty GET body. Health nonce retention is 2 minutes in an in-memory bounded cache so liveness remains independent from SQLite, Badger, Admin, and EventBus.

---

### Task 1: Preserve and Separate Existing Worktree Changes

**Files:**
- Existing: `.github/workflows/deploy-docs.yml`
- Existing untracked: `pnpm-lock.yaml`
- Existing untracked: `docs/superpowers/plans/2026-07-12-moox-crypto-and-style-unification.md`
- Existing commit: `3668038 refactor: unify crypto and module organization`

- [ ] **Step 1: Record the baseline without modifying it**

Run:

```bash
git status --short --branch
git log -3 --oneline --decorate
git diff -- .github/workflows/deploy-docs.yml
```

Expected: `main` is one commit ahead of `origin/main`; the workflow is modified; the pnpm lock and prior plan are untracked.

- [ ] **Step 2: Verify the docs workflow and lock file belong together**

Run:

```bash
pnpm install --frozen-lockfile
pnpm run docs:build
```

Expected: both commands pass and `.github/workflows/deploy-docs.yml` references `pnpm-lock.yaml` with `cache: pnpm`.

- [ ] **Step 3: Commit the pre-existing docs CI files separately**

```bash
git add .github/workflows/deploy-docs.yml pnpm-lock.yaml
git commit -m "ci: use pnpm for docs deployment"
```

- [ ] **Step 4: Commit the previous implementation plan separately**

```bash
git add docs/superpowers/plans/2026-07-12-moox-crypto-and-style-unification.md
git commit -m "docs: record crypto and style unification plan"
```

- [ ] **Step 5: Recheck that the security implementation starts from a clean tree**

Run: `git status --short --branch`

Expected: no modified or untracked files. Do not combine these files with later security commits.

---

### Task 2: Finish the Small Crypto and Boundary-Script Cleanups

**Files:**
- Modify: `packages/crypto/crypto.go`
- Modify: `packages/crypto/crypto_test.go`
- Modify: `scripts/check/check-package-boundaries.sh`

- [ ] **Step 1: Add a failing bcrypt length test**

Add to `packages/crypto/crypto_test.go`:

```go
func TestHashPasswordRejectsMoreThan72Bytes(t *testing.T) {
	_, err := HashPassword(strings.Repeat("a", 73))
	if !errors.Is(err, bcrypt.ErrPasswordTooLong) {
		t.Fatalf("HashPassword error = %v, want bcrypt.ErrPasswordTooLong", err)
	}
}
```

Add imports for `errors` and `golang.org/x/crypto/bcrypt`.

- [ ] **Step 2: Run the focused test and confirm the existing wrapper preserves the error**

Run:

```bash
go test -count=1 ./packages/crypto -run TestHashPasswordRejectsMoreThan72Bytes
```

Expected: PASS. This proves current bcrypt behavior; do not pre-hash or replace bcrypt.

- [ ] **Step 3: Clarify the AES key-input contract**

Replace the `Encrypt` comment with:

```go
// Encrypt encrypts plaintext with AES-256-GCM. The key is derived from secret
// with SHA-256, so callers do not need to coordinate AES key lengths. Secret
// must already contain high-entropy key material; this function is not a
// password-based key derivation function.
```

- [ ] **Step 4: Delete the unused boundary helper**

Delete `check_forbidden_imports()` from `scripts/check/check-package-boundaries.sh`. Do not rewrite the active domain/core and infra loops in this task.

- [ ] **Step 5: Verify and commit**

```bash
go test -count=1 ./packages/crypto/...
bash -n scripts/check/check-package-boundaries.sh
bash scripts/check/check-package-boundaries.sh
git add packages/crypto/crypto.go packages/crypto/crypto_test.go scripts/check/check-package-boundaries.sh
git commit -m "chore: clarify crypto contracts and remove dead boundary code"
```

---

### Task 3: Add Shared Canonical Request-Signing Primitives

**Files:**
- Create: `packages/requestauth/go.mod`
- Create: `packages/requestauth/requestauth.go`
- Create: `packages/requestauth/requestauth_test.go`
- Modify: `go.work`
- Modify: `packages/crypto/crypto.go`
- Modify: `packages/crypto/crypto_test.go`
- Modify: `modules/admin/internal/gateway/service_auth.go`

- [ ] **Step 1: Write request-auth contract tests**

Cover this exact API:

```go
type Material struct {
	Method    string
	Path      string
	Body      []byte
	Timestamp int64
	Nonce     string
}

func Canonical(m Material) ([]byte, error)
func Sign(secret string, m Material) (string, error)
func Verify(secret string, m Material, signature string) error
func NewNonce() (string, error)
```

Tests must assert:

```go
func TestCanonicalUsesExactBodyAndEscapedPath(t *testing.T)
func TestSignVerifyRoundTrip(t *testing.T)
func TestVerifyRejectsBodyPathTimestampAndNonceChanges(t *testing.T)
func TestNewNonceReturns64LowercaseHexCharacters(t *testing.T)
func TestVerifyRejectsMalformedSignature(t *testing.T)
```

- [ ] **Step 2: Run tests and verify the package is absent**

Run: `go test -count=1 ./packages/requestauth/...`

Expected: FAIL because the module and API do not exist.

- [ ] **Step 3: Add narrowly scoped crypto primitives**

Add to `packages/crypto/crypto.go`:

```go
func SHA256Hex(data []byte) string
func HMACSHA256Hex(secret string, data []byte) string
func RandomHex(size int) (string, error)
```

`RandomHex` must reject `size <= 0`, use `crypto/rand`, and return lowercase hexadecimal. Add deterministic hash/HMAC tests and a shape/uniqueness test for `RandomHex`.

- [ ] **Step 4: Implement `packages/requestauth`**

Rules:

```go
const Version = "moox-request-v1"

// Canonical rejects an empty method/path, non-positive timestamp, nonce values
// that are not exactly 64 lowercase hex characters, and paths containing a
// scheme or host. It uppercases method and hashes the exact body bytes.
```

Use `crypto.HMACSHA256Hex` and `hmac.Equal`; do not duplicate cryptographic implementations.

- [ ] **Step 5: Add the workspace module**

`packages/requestauth/go.mod`:

```go
module github.com/mooyang-code/moox/packages/requestauth

go 1.24.0

require github.com/mooyang-code/moox/packages/crypto v0.0.0-00010101000000-000000000000

replace github.com/mooyang-code/moox/packages/crypto => ../crypto
```

Add `./packages/requestauth` to `go.work`.

- [ ] **Step 6: Reuse the shared HMAC primitive in existing service auth**

Replace `hmacSha256Hex` in `modules/admin/internal/gateway/service_auth.go` with `mooxcrypto.HMACSHA256Hex`. Preserve the `moox-auth-v1` wire format and existing tests.

- [ ] **Step 7: Verify and commit**

```bash
go test -count=1 ./packages/crypto/... ./packages/requestauth/...
go test -count=1 ./modules/admin/internal/gateway/...
git add go.work packages/crypto packages/requestauth modules/admin/internal/gateway/service_auth.go
git commit -m "feat: add shared request signing primitives"
```

---

### Task 4: Add Atomic Session and Nonce Storage

**Files:**
- Modify: `modules/admin/internal/service/auth/dao/badger.go`
- Modify: `modules/admin/internal/service/auth/dao/badger_test.go`
- Modify: `modules/admin/internal/service/auth/dao/user.go`
- Modify: `modules/admin/internal/service/auth/dao/user_test.go`
- Modify: `modules/admin/internal/service/auth/model/user.go`
- Modify: `modules/admin/internal/service/auth/config/config.go`
- Modify: `modules/admin/internal/service/auth/config/config_test.go`

- [ ] **Step 1: Write failing atomic-cache tests**

Add tests for:

```go
func TestCacheDBDeleteRemovesValue(t *testing.T)
func TestCacheDBSetIfAbsentAllowsOnlyOneConcurrentWriter(t *testing.T)
func TestConsumeLoginSaltReturnsValueOnlyOnce(t *testing.T)
func TestConsumeSessionNonceRejectsReplay(t *testing.T)
```

Run: `go test -count=1 ./modules/admin/internal/service/auth/dao/...`

Expected: FAIL because `Delete`, `SetIfAbsent`, and consume methods do not exist.

- [ ] **Step 2: Implement atomic Badger operations**

Add:

```go
func (c *CacheDB) Delete(ctx context.Context, key string) error
func (c *CacheDB) SetIfAbsent(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
```

`SetIfAbsent` must perform read and write in one Badger update transaction. Existing non-expired keys return `(false, nil)`; no read-then-write race is allowed.

- [ ] **Step 3: Define session records**

Add:

```go
type RequestSigningSession struct {
	SessionID       string    `json:"session_id"`
	UserID          string    `json:"user_id"`
	EncryptedSecret string    `json:"encrypted_secret"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type RawSessionTicket struct {
	TicketID  string    `json:"ticket_id"`
	SessionID string    `json:"session_id"`
	UserID    string    `json:"user_id"`
	Operation string    `json:"operation"`
	ExpiresAt time.Time `json:"expires_at"`
}
```

- [ ] **Step 4: Add DAO methods with stable prefixes**

```go
func (d *UserDAO) ConsumeLoginSalt(ctx context.Context, username string) (*model.LoginSalt, error)
func (d *UserDAO) SetSigningSession(ctx context.Context, session model.RequestSigningSession) error
func (d *UserDAO) GetSigningSession(ctx context.Context, sessionID string) (*model.RequestSigningSession, error)
func (d *UserDAO) DeleteSigningSession(ctx context.Context, sessionID string) error
func (d *UserDAO) ConsumeSessionNonce(ctx context.Context, sessionID, nonce string, ttl time.Duration) (bool, error)
func (d *UserDAO) SetRawSessionTicket(ctx context.Context, ticket model.RawSessionTicket) error
func (d *UserDAO) ConsumeRawSessionTicket(ctx context.Context, ticketID string) (*model.RawSessionTicket, error)
```

Use prefixes `login_salt:`, `signing_session:`, `session_nonce:`, and `raw_ticket:`. Consume methods must be atomic.

- [ ] **Step 5: Make security durations explicit**

Extend `SecurityConfig`:

```go
SessionTTL       time.Duration `yaml:"session_ttl"`
RequestClockSkew time.Duration `yaml:"request_clock_skew"`
NonceTTL         time.Duration `yaml:"nonce_ttl"`
RawTicketTTL     time.Duration `yaml:"raw_ticket_ttl"`
```

Defaults: `24h`, `60s`, `2m`, and `60s`. Reject non-positive values and require JWT access expiry to equal session TTL.

- [ ] **Step 6: Verify and commit**

```bash
go test -count=1 ./modules/admin/internal/service/auth/dao/... ./modules/admin/internal/service/auth/config/...
git add modules/admin/internal/service/auth/{dao,model,config}
git commit -m "feat: add atomic admin signing session storage"
```

---

### Task 5: Simplify the Auth Protocol and Add 24-Hour Signing Sessions

**Files:**
- Modify: `modules/admin/proto/infra_service.proto`
- Regenerate: `modules/admin/proto/admingen/infra_service.pb.go`
- Regenerate: `modules/admin/proto/admingen/infra_service.trpc.go`
- Delete: `modules/admin/internal/service/auth/impl/password.go`
- Modify: `modules/admin/internal/service/auth/impl/password_crypto.go`
- Modify: `modules/admin/internal/service/auth/impl/login.go`
- Modify: `modules/admin/internal/service/auth/impl/login_test.go`
- Modify: `modules/admin/internal/service/auth/impl/helper.go`
- Modify: `modules/admin/internal/service/auth/impl/helper_test.go`
- Modify: `modules/admin/internal/service/auth/impl/user.go`
- Modify: `modules/admin/internal/service/auth/model/user.go`
- Create: `modules/admin/internal/service/auth/impl/session.go`
- Create: `modules/admin/internal/service/auth/impl/session_test.go`
- Modify: `modules/admin/config/gateway.yaml`

- [ ] **Step 1: Change the proto contract first**

Delete `RegisterReq/Rsp`, `GetChangePasswordSaltReq/Rsp`, `ChangePasswordReq/Rsp`, and their RPC methods. Extend login and add session operations:

```proto
message LoginRsp {
  common.RetInfo ret_info = 1;
  string access_token = 2;
  int64 expires_in = 3;
  UserInfo user_info = 4;
  string session_id = 5;
  string request_signing_key = 6;
  int64 expires_at = 7;
}

message LogoutReq { string access_token = 1; }
message LogoutRsp { common.RetInfo ret_info = 1; }

message IssueRawSessionTicketReq { string operation = 1; }
message IssueRawSessionTicketRsp {
  common.RetInfo ret_info = 1;
  string ticket = 2;
  int64 expires_at = 3;
}

service Auth {
  rpc GetLoginSalt(GetLoginSaltReq) returns (GetLoginSaltRsp);
  rpc Login(LoginReq) returns (LoginRsp);
  rpc Logout(LogoutReq) returns (LogoutRsp);
  rpc IssueRawSessionTicket(IssueRawSessionTicketReq) returns (IssueRawSessionTicketRsp);
  rpc GetUserInfo(GetUserInfoReq) returns (GetUserInfoRsp);
  rpc UpdateUserInfo(UpdateUserInfoReq) returns (UpdateUserInfoRsp);
}
```

- [ ] **Step 2: Regenerate protobuf code and prove deleted methods no longer compile**

Run:

```bash
make -C modules/admin/proto all
go test -count=1 ./modules/admin/internal/service/auth/...
```

Expected: FAIL at old Register/change-password implementations and tests.

- [ ] **Step 3: Make login salts one-time**

`Login` must atomically consume the salt before password decryption. A failed password attempt cannot reuse the same salt. `GetLoginSalt` may return an existing unused salt, but once consumed it must generate a new one.

- [ ] **Step 4: Create a signing session on successful login**

Generate a 32-byte key and 16-byte session ID through `crypto.RandomHex`. Encrypt the key with the Admin encryption key before writing Badger. Add `sid` to JWT claims. Return the plaintext key exactly once in `LoginRsp`. Set JWT `exp`, session TTL, and `expires_at` to the same absolute instant.

- [ ] **Step 5: Implement logout and raw tickets**

`Logout` deletes only the current `sid`. `IssueRawSessionTicket` accepts only `ssh_ws`, `sftp_download`, or `sftp_upload`; it stores a random one-time ticket for 60 seconds and binds it to user ID and session ID.

- [ ] **Step 6: Remove public registration and password-change code**

Delete the change-password implementation and cache model. Remove `ActionChangePassword`. Remove Register implementation/tests from `user.go` and `helper_test.go`. Keep bcrypt creation available to the CLI task below.

- [ ] **Step 7: Tighten the unauthenticated allowlist**

`gateway.no_auth_methods` must contain exactly:

```yaml
no_auth_methods:
  - "/api/admin/auth/GetLoginSalt"
  - "/api/admin/auth/Login"
```

Raw routes move to explicit ticket validation and must not be described as unauthenticated.

- [ ] **Step 8: Verify and commit**

```bash
go test -count=1 ./modules/admin/internal/service/auth/...
go test -count=1 ./modules/admin/proto/admingen/...
git add modules/admin/proto modules/admin/internal/service/auth modules/admin/config/gateway.yaml
git commit -m "feat: issue 24 hour signed admin sessions"
```

---

### Task 6: Add a Safe Local Admin User CLI

**Files:**
- Create: `modules/admin/cmd/cli/admin_user.go`
- Create: `modules/admin/cmd/cli/admin_user_test.go`
- Modify: `modules/admin/cmd/cli/main.go`
- Modify: `modules/admin/cmd/cli/init_schema.go`
- Modify: `scripts/deploy/deploy-moox.sh`

- [ ] **Step 1: Write CLI contract tests**

Test these commands with temporary SQLite databases and stdin buffers:

```text
moox-admin-cli user ensure --db-path <path> --username admin --password-stdin
moox-admin-cli user reset-password --db-path <path> --username admin --password-stdin
moox-admin-cli random-secret --bytes 32
```

Assertions:

- password arguments are not accepted;
- empty stdin fails;
- password over 72 bytes fails clearly;
- `ensure` creates one SUPER_ADMIN and is idempotent without changing its password;
- `reset-password` changes only the bcrypt hash;
- stdout is JSON and never contains password or hash.
- `random-secret` uses `crypto/rand`, emits exactly the requested number of random bytes as lowercase hex, and rejects unsafe sizes.

- [ ] **Step 2: Implement the CLI**

Use `crypto.HashPassword`, trim only the trailing newline, and keep all database access local. Implement `random-secret` directly with `crypto/rand`; never reuse password, JWT, or deterministic key helpers. Do not call the public Auth RPC.

- [ ] **Step 3: Integrate first-deploy bootstrap**

Add deployment support:

```text
--admin-username <name>             default: admin
--admin-password-file <local-path>  required only when no user exists in non-interactive mode
```

Interactive deployment uses a hidden terminal prompt twice. Send the password over SSH stdin directly to `moox-admin-cli user ensure --password-stdin`; never place it in argv, generated scripts, logs, environment files, or release archives.

- [ ] **Step 4: Verify and commit**

```bash
go test -count=1 ./modules/admin/cmd/cli/...
bash -n scripts/deploy/deploy-moox.sh
git add modules/admin/cmd/cli scripts/deploy/deploy-moox.sh
git commit -m "feat: add local admin user provisioning"
```

---

### Task 7: Enforce JWT Plus HMAC on Management Requests

**Files:**
- Create: `modules/admin/internal/gateway/request_auth.go`
- Create: `modules/admin/internal/gateway/request_auth_test.go`
- Modify: `modules/admin/internal/gateway/authorize.go`
- Modify: `modules/admin/internal/gateway/authorize_test.go`
- Modify: `modules/admin/internal/gateway/gateway.go`
- Modify: `modules/admin/internal/gateway/gateway_test.go`
- Modify: `modules/admin/internal/gateway/rawhandler.go`
- Modify: `modules/admin/internal/gateway/rawhandler_test.go`
- Modify: `modules/admin/go.mod`
- Modify: `modules/admin/go.sum`

- [ ] **Step 1: Write failing gateway security tests**

Cover:

```go
func TestAdminRequestRequiresJWTAndSignature(t *testing.T)
func TestAdminRequestRejectsExpiredSession(t *testing.T)
func TestAdminRequestRejectsTimestampOutsideWindow(t *testing.T)
func TestAdminRequestRejectsChangedBodyOrPath(t *testing.T)
func TestAdminRequestRejectsReplayedNonce(t *testing.T)
func TestLoginRoutesRemainUnsigned(t *testing.T)
func TestServiceRoutesKeepServiceAuthOnly(t *testing.T)
func TestRawRouteRequiresMatchingOneTimeTicket(t *testing.T)
```

- [ ] **Step 2: Extend parsed access claims**

```go
type accessClaims struct {
	UserID    string
	Username  string
	Role      int32
	SessionID string
	ExpiresAt time.Time
}
```

Reject tokens without `iss=moox-admin`, `token_type=access`, non-empty `sid`, and a valid expiration.

- [ ] **Step 3: Verify signatures against exact HTTP bytes**

Read and restore JSON request bodies once, before forwarding. Build `requestauth.Material` from `r.Method`, `r.URL.EscapedPath()`, exact bytes, timestamp, and nonce. Load/decrypt the session secret, verify HMAC, then atomically consume nonce. Do not reconstruct JSON from protobuf or maps.

- [ ] **Step 4: Keep error behavior uniform**

Missing JWT, missing signing headers, expired JWT/session, malformed nonce, invalid signature, and replay all return HTTP `401` with the existing `{ret_info}` no-auth shape. Logs may contain session ID and reason but never JWT, signature key, signature, or body.

- [ ] **Step 5: Protect raw routes with tickets**

Validate and consume `ticket` before dispatching `WsConnect`, `SftpDownload`, or `SftpUpload`. Bind operation names to routes. A ticket used on a different route, after expiry, or twice returns `401`.

- [ ] **Step 6: Verify and commit**

```bash
go test -count=1 ./modules/admin/internal/gateway/...
git add modules/admin/internal/gateway modules/admin/go.mod modules/admin/go.sum
git commit -m "feat: require signed admin management requests"
```

---

### Task 8: Persist and Apply Browser Signing Sessions

**Files:**
- Create: `web/src/utils/request-signing.ts`
- Create: `web/src/utils/request-signing.test.ts`
- Create: `web/src/api/admin/signed-client.ts`
- Modify: `web/src/utils/crypto.ts`
- Modify: `web/src/store/modules/user-info.ts`
- Modify: `web/src/views/login/components/login-form.vue`
- Modify: `web/src/api/admin/http.ts`
- Modify: `web/src/api/storage/http.ts`
- Modify: `web/src/api/trade/http.ts`
- Modify: `web/src/api/modules/user/index.ts`
- Modify: `web/src/router/index.ts`
- Modify SSH/SFTP callers found by `rg -n 'WsConnect|SftpDownload|SftpUpload' web/src`

- [ ] **Step 1: Write deterministic Web Crypto tests**

Test:

- import a 32-byte key as non-exportable `HMAC/SHA-256`;
- IndexedDB persistence and deletion by session ID;
- canonical bytes match the Go test vector;
- nonce shape is 64 lowercase hex characters;
- exact serialized request body is hashed and signed;
- missing or expired local session triggers cleanup.

- [ ] **Step 2: Implement the persistent key store**

Expose:

```ts
export async function saveSigningSession(input: {
  sessionId: string;
  rawKeyHex: string;
  expiresAt: number;
}): Promise<void>;

export async function loadSigningSession(sessionId: string): Promise<{
  key: CryptoKey;
  expiresAt: number;
} | null>;

export async function clearSigningSessions(): Promise<void>;
```

Store a non-exportable `CryptoKey` in IndexedDB. Never persist `rawKeyHex`.

- [ ] **Step 3: Centralize signed Axios behavior**

All Admin, Storage, and Trade clients must call one request interceptor. Serialize JSON once, calculate its exact UTF-8 bytes, then add JWT, timestamp, nonce, and signature headers. Remove duplicate token-reading interceptors.

- [ ] **Step 4: Persist the 24-hour session metadata**

Pinia persists `token`, `sessionId`, and `expiresAt`; the signing key remains only in IndexedDB. On startup, require all four pieces. An absolute expiry, missing key, `401`, or logout clears Pinia, localStorage, and IndexedDB and redirects to `/login`.

- [ ] **Step 5: Update login and logout**

After Login, save the non-exportable key before navigating. Logout calls the signed `Logout` RPC and clears local state even if the network call fails.

- [ ] **Step 6: Add raw-operation tickets**

Before opening WebSocket or SFTP, call signed `IssueRawSessionTicket` and pass only the one-time ticket to the raw route. Do not put JWT or request-signing key in URL/query parameters.

- [ ] **Step 7: Remove the password-change surface**

Delete any change-password route, menu, API helper, form, and translation keys found by:

```bash
rg -n 'ChangePassword|GetChangePasswordSalt|change-password|修改密码' web/src
```

- [ ] **Step 8: Verify and commit**

```bash
pnpm --dir web test --run
pnpm --dir web build:prod
git add web/src web/tests
git commit -m "feat: sign persistent admin browser sessions"
```

---

### Task 9: Keep Web-host Internal and Static-only

**Files:**
- Modify: `web-host/main.go`
- Modify: `web-host/health_test.go`
- Modify: `web-host/README.md`
- Modify: `web/src/api/gateway.ts`
- Modify: frontend tests covering production gateway origin

- [ ] **Step 1: Inventory and lock down every browser network path**

Search all `fetch`, Axios, WebSocket, upload, and download construction under `web/src`. Record tests proving:

- login and login-salt requests use `/api/admin/auth/*`;
- Storage, Trade, Monitor, Collector, CloudNode, settings, and other MooX RPCs use `/api/admin/*`;
- SSH WebSocket and SFTP use `/api/admin/ssh/*` on the browser origin;
- no frontend source constructs `/api/service/*` or a direct `:11000` URL;
- an external `upload_url` returned by `InitPackageUpload` may be called directly, but the request sends only the file body and headers required by the pre-signed URL, never JWT, session HMAC, service HMAC, or selected-space headers.

Add a lightweight source-contract test so future frontend code cannot silently introduce `/api/service/*`, hard-coded Admin ports, or credential-bearing external uploads.

- [ ] **Step 2: Add failing bind and routing tests**

Tests must prove that web-host defaults to `127.0.0.1:9528`, serves embedded assets and SPA fallback, does not implement TLS, does not proxy `/api/*`, and exposes diagnostics only through its separate authenticated health listener on `127.0.0.1:19527`.

- [ ] **Step 3: Simplify web-host configuration**

Use these defaults:

```text
MOOX_WEB_HOST_ADDR=127.0.0.1:9528
MOOX_WEB_HOST_HEALTH_ADDR=127.0.0.1:19527
```

Delete or avoid introducing Admin target, certificate, and public gateway configuration. Caddy owns TLS, route allowlisting, forwarding headers, WebSocket transport, and browser security headers.

- [ ] **Step 4: Use same-origin Admin URLs in production**

Make the production frontend use `window.location.origin` (or an empty Axios base URL) for `/api/admin/*`. Preserve an explicit development override that must be set deliberately and is never included in production assets. Remove `DEFAULT_GATEWAY_PORT=11000`, `VITE_GATEWAY_PORT`, README guidance, and any other defaults that send browsers directly to Admin.

- [ ] **Step 5: Verify and commit**

```bash
go test -count=1 ./web-host/...
pnpm --dir web test --run
pnpm --dir web build:prod
git add web-host web/src web/tests
git commit -m "refactor: keep web host behind the edge proxy"
```

---

### Task 10: Add Route-isolated Caddy HTTPS Edges

**Files:**
- Create: `deploy/caddy/Caddyfile`
- Create: `scripts/test/contract/test-caddy-config.sh`
- Modify: `.gitignore` only if generated Caddy data needs an explicit rule

- [ ] **Step 1: Write the Caddy configuration contract test**

Start fake upstreams and a temporary Caddy data directory. Assert:

- browser `:9527` sends `/api/admin/*` unchanged to `127.0.0.1:11000`;
- browser `:9527` sends non-API paths to `127.0.0.1:9528`;
- browser `:9527` returns `404` for `/api/service/*`, all other `/api/*`, `/healthz`, `/readyz`, and `/metrics`;
- service `:11001` sends only `/api/service/*` unchanged to `127.0.0.1:11002` and returns `404` for every other path;
- WebSocket upgrades and streaming Admin responses survive the browser edge;
- Caddy emits HTTPS certificates from its internal CA and configuration validation succeeds.

- [ ] **Step 2: Create the dual-listener Caddyfile**

Use mutually exclusive `handle` blocks, not `handle_path`, so upstream paths are never stripped:

```caddyfile
{
    auto_https disable_redirects
    admin 127.0.0.1:2019
}

https://{$MOOX_PUBLIC_HOST}:9527 {
    tls internal
    encode zstd gzip

    header {
        X-Content-Type-Options nosniff
        X-Frame-Options DENY
        Referrer-Policy no-referrer
    }

    handle /api/admin/* {
        reverse_proxy 127.0.0.1:11000 {
            stream_close_delay 5m
        }
    }
    handle /api/* {
        respond 404
    }
    handle /healthz {
        respond 404
    }
    handle /readyz {
        respond 404
    }
    handle /metrics {
        respond 404
    }
    handle {
        reverse_proxy 127.0.0.1:9528
    }
}

https://{$MOOX_PUBLIC_HOST}:11001 {
    tls internal
    handle /api/service/* {
        reverse_proxy 127.0.0.1:11002
    }
    handle {
        respond 404
    }
}
```

Keep the host and ports configurable through deployment-time environment substitution without widening either route set. Add the final tested CSP after confirming the frontend's actual asset and connection requirements. Do not enable HSTS by default for a private CA deployment.

- [ ] **Step 3: Validate configuration and transport behavior**

Run:

```bash
bin/caddy validate --config deploy/caddy/Caddyfile --adapter caddyfile
bash scripts/test/contract/test-caddy-config.sh
```

- [ ] **Step 4: Commit**

```bash
git add deploy/caddy scripts/test/contract/test-caddy-config.sh .gitignore
git commit -m "feat: add isolated caddy https edges"
```

---

### Task 11: Pin and Deploy Caddy Rootlessly

**Files:**
- Create: `scripts/deps/caddy-v2.11.4-checksums.txt`
- Create: `scripts/deploy/install-caddy-ca.sh`
- Create: `scripts/test/contract/test-install-caddy-ca.sh`
- Modify: `scripts/deploy/deploy-moox.sh`
- Modify: `scripts/release/release.sh`
- Modify: `scripts/test/contract/test-deploy-moox-preserve-disabled.sh`
- Create: `scripts/test/contract/test-deploy-moox-https.sh`
- Modify: `Makefile`

- [ ] **Step 1: Add a deployment-contract test before implementation**

The test must deploy to a temporary directory and assert that the exact Caddy v2.11.4 archive is selected for the target OS/architecture, the official checksum is verified before extraction, the binary and Caddyfile are installed below the deployment root, and a second deployment preserves the Caddy CA fingerprint. Cover a missing Caddy binary, stopped MooX Caddy, healthy running MooX Caddy, wrong managed version, stale PID file, occupied edge port, and an unrelated system Caddy. Runtime installation remains rootless; only an explicitly approved local trust-store operation may request administrator privileges.

- [ ] **Step 2: Pin official artifacts**

Use release assets from `https://github.com/caddyserver/caddy/releases/download/v2.11.4/`. Commit the official checksum manifest and verify the selected archive with `sha256sum` or `shasum -a 256`. Fail closed on download, version, architecture, or checksum mismatch. Do not use `latest` URLs.

- [ ] **Step 3: Inspect Caddy installation, ownership, and runtime state**

Before deploying web-host, run a target-side preflight that records:

```text
managed binary:   ${DEPLOY_DIR}/bin/caddy
managed config:   ${DEPLOY_DIR}/config/caddy/Caddyfile
managed PID file: ${DEPLOY_DIR}/run/caddy.pid
managed data:     ${DEPLOY_DIR}/data/caddy
admin endpoint:   127.0.0.1:2019
edge listeners:   <browser-port>, <service-port>
```

The preflight must:

1. Check whether the managed binary exists, is executable, reports exactly `v2.11.4`, and matches the committed checksum.
2. Validate the candidate Caddyfile before stopping or replacing anything.
3. If the PID file exists, verify `/proc/<pid>/exe` or the platform equivalent points to `${DEPLOY_DIR}/bin/caddy`; treat a mismatched or reused PID as stale and never signal it.
4. Query the managed admin endpoint and inspect process arguments/listeners to determine whether Caddy is healthy and serving the expected config.
5. Check whether `9527`, `11001`, or the configured ports are occupied. If owned by the verified MooX Caddy, perform a controlled reload; if owned by any other process, fail with the PID/process details and remediation guidance.
6. Detect package-manager/system Caddy installations for diagnostics only. Do not stop, overwrite, or reconfigure them automatically.
7. Install the pinned managed binary when absent or replace it atomically when its verified managed version is wrong. Preserve the previous binary for rollback until acceptance succeeds.

Deployment must not infer ownership from the process name alone. Ownership requires the expected executable path plus deployment metadata/PID validation.

- [ ] **Step 4: Add explicit edge options**

```text
--public-host <ip-or-dns>
--browser-https-port <port>        default 9527
--service-https-port <port>        default 11001
--web-host-port <port>             default 9528, loopback only
--admin-control-port <port>        default 11000, loopback only
--admin-service-port <port>        default 11002, loopback only
--local-ca <auto|install|skip>     default auto
--local-ca-output <path>           default ~/.moox/certs/<public-host>-root.crt
--caddy-conflict <fail>            only supported policy; never take over unrelated Caddy
```

Require an explicit `--public-host` when the SSH target is an alias, localhost, or not a valid certificate SAN. Never discover the public IP through an external service.

`--local-ca auto` means: when attached to a terminal, fetch the CA, show its subject and SHA-256 fingerprint, and ask once whether to install it; in non-interactive mode, fetch it but do not alter trust. `install` performs the platform-specific trust operation and may prompt through the operating system for administrator approval. `skip` performs neither local fetch nor trust installation. CI must use `skip` unless testing a disposable trust store.

- [ ] **Step 5: Generate rootless runtime scripts**

Install Caddy as `${DEPLOY_DIR}/bin/caddy`, its configuration as `${DEPLOY_DIR}/config/caddy/Caddyfile`, and use:

```text
XDG_DATA_HOME=${DEPLOY_DIR}/data/caddy
XDG_CONFIG_HOME=${DEPLOY_DIR}/config/caddy/runtime
```

Start Admin and web-host first and verify their loopback listeners. If managed Caddy is already healthy, use `caddy reload --config ... --adapter caddyfile` and verify the new config; otherwise start it and atomically write `${DEPLOY_DIR}/run/caddy.pid`. Stop Caddy before its upstreams during a full stop. Preserve `${DEPLOY_DIR}/data/caddy` across normal deployments and `--reset-data`; only a separately named destructive CA-rotation operation may remove it.

If reload/start acceptance fails, restore the previous Caddyfile and binary, restart or reload the previous verified managed instance, and report failure. Never leave a partially updated edge serving a mixture of old and new upstreams.

- [ ] **Step 6: Wait for Caddy CA creation and configure trust**

After Caddy starts, wait for `${DEPLOY_DIR}/data/caddy/caddy/pki/authorities/local/root.crt`, verify that it is a CA certificate, and print its fingerprint. Then:

1. Copy the public root certificate to `${DEPLOY_DIR}/certs/caddy/root.crt` with mode `0644`; never copy `root.key`.
2. Configure all same-host MooX backend processes with `MOOX_SERVICE_GATEWAY_CA_FILE=${DEPLOY_DIR}/certs/caddy/root.crt`.
3. Encode the same root for SCF/backend environment generation through `MOOX_SERVICE_GATEWAY_CA_PEM_B64`.
4. Fetch the root certificate to the deployment operator's machine unless `--local-ca skip` is set.
5. Under `auto`/`install`, run the platform adapter from `scripts/deploy/install-caddy-ca.sh` according to the confirmation rules above.
6. Verify `https://<public-host>:9527` and `https://<public-host>:11001` with the fetched CA before declaring deployment successful.

Treat CA fingerprint changes as a high-signal event: ordinary redeployment must fail and require an explicit `--accept-ca-change <expected-old-fingerprint>` or separate CA-rotation command. This prevents silently trusting a replaced or compromised deployment.

- [ ] **Step 7: Implement cross-platform local CA installation**

`scripts/deploy/install-caddy-ca.sh` must support:

- macOS: add/update the certificate in the System keychain using `security add-trusted-cert`, allowing the OS to request authorization;
- Windows Git Bash/PowerShell: import into `LocalMachine\\Root` when elevated, otherwise `CurrentUser\\Root`, and report which store was used;
- Debian/Ubuntu: copy to `/usr/local/share/ca-certificates/moox-<fingerprint>.crt` and run `update-ca-certificates` after explicit approval;
- RHEL/Fedora: install under `/etc/pki/ca-trust/source/anchors/` and run `update-ca-trust` after explicit approval;
- unsupported systems: retain the fetched CA and print an exact manual command instead of disabling verification.

Installation must be idempotent by SHA-256 fingerprint, reject non-CA certificates, avoid duplicate entries, never install a private key, and support `--dry-run` for tests.

- [ ] **Step 8: Verify public and private binding**

Deployment tests must prove that Caddy alone owns `0.0.0.0:9527` and `0.0.0.0:11001`, while web-host `9528`, Admin control `11000`, and Admin service `11002` are loopback-only. Prove that an existing healthy managed Caddy is reloaded rather than duplicated, a stopped managed Caddy is restarted, a missing managed Caddy is installed, stale PID files are harmless, unrelated listeners cause a fail-closed result, and failed acceptance restores the prior managed version/config. Test `auto`, `install`, and `skip` against mocked platform trust tools; prove idempotence, safe handling of spaces, privilege cancellation, CA mismatch failure, and that private keys never leave the target.

- [ ] **Step 9: Verify and commit**

```bash
bash -n scripts/deploy/deploy-moox.sh scripts/release/release.sh scripts/deploy/install-caddy-ca.sh scripts/test/contract/test-deploy-moox-https.sh scripts/test/contract/test-install-caddy-ca.sh
bash scripts/test/contract/test-deploy-moox-https.sh
bash scripts/test/contract/test-install-caddy-ca.sh
bash scripts/test/contract/test-deploy-moox-preserve-disabled.sh
git add scripts Makefile
git commit -m "feat: deploy pinned caddy https edges"
```

---

### Task 12: Add the Caddy CA Workflow to the MooX Skill

**Files:**
- Create: `skills/moox/scripts/caddy-ca.sh`
- Create: `skills/moox/scripts/test/contract/test-caddy-ca.sh`
- Create: `skills/moox/scripts/caddy-prerequisite.sh`
- Create: `skills/moox/scripts/test/contract/test-caddy-prerequisite.sh`
- Create: `skills/moox/references/caddy-https.md`
- Modify: `skills/moox/SKILL.md`
- Modify: `skills/moox/references/release.md`

- [ ] **Step 1: Make Caddy an initial-deployment prerequisite**

The primary MooX skill deployment path must run `caddy-prerequisite.sh ensure` before uploading or starting web-host. Users must not need to install Caddy manually. The helper operates on the deployment target over the skill's existing SSH transport and supports:

```text
skills/moox/scripts/caddy-prerequisite.sh check --target <user@host> --deploy-dir <dir>
skills/moox/scripts/caddy-prerequisite.sh ensure --target <user@host> --deploy-dir <dir>
skills/moox/scripts/caddy-prerequisite.sh status --target <user@host> --deploy-dir <dir>
```

`check` reports target OS/architecture, managed binary/version/checksum, process ownership, Caddy admin health, config validity, and edge-port ownership. `ensure` downloads the pinned official artifact to the target when missing, verifies it before atomic installation, creates managed data/config/run directories, and starts or reloads only the MooX-managed instance. `status` produces concise machine-readable JSON suitable for the skill and deployment scripts.

The helper must be idempotent and non-interactive for normal prerequisite installation. It must not require package-manager Caddy, modify a system Caddy, install a systemd service, or request root privileges because MooX uses unprivileged ports and a deployment-local binary.

Keep prerequisite logic single-sourced: the skill helper invokes the repository deployment helper from Task 11 instead of maintaining a second download/version/process implementation. `scripts/deploy/deploy-moox.sh` and the MooX skill must therefore produce identical checks, installation layout, ownership rules, and rollback behavior.

- [ ] **Step 2: Test prerequisite installation through the skill entrypoint**

Contract tests must start from a target with no Caddy and prove that one invocation of the documented initial-deployment command installs the correct binary, validates the Caddyfile, starts Caddy after its upstreams become available, and completes HTTPS acceptance. Also cover existing healthy/stopped/wrong-version managed Caddy, unsupported OS/architecture, unavailable download, checksum mismatch, occupied ports, stale PID, and unrelated system Caddy.

Do not test only the low-level script: invoke the same MooX skill command sequence documented for users so the integration cannot be accidentally omitted.

- [ ] **Step 3: Write a certificate-helper contract test**

Assert that the helper fetches only Caddy's root certificate from `${DEPLOY_DIR}/data/caddy/caddy/pki/authorities/local/root.crt`, rejects private-key paths, uses SSH `BatchMode` and safe quoting, verifies the expected fingerprint, and delegates trust changes to the tested `scripts/deploy/install-caddy-ca.sh` adapter only after explicit confirmation or an explicit `install` command.

- [ ] **Step 4: Implement the certificate helper**

Supported commands:

```text
skills/moox/scripts/caddy-ca.sh fetch --target user@host --deploy-dir <dir> --output <file>
skills/moox/scripts/caddy-ca.sh inspect --ca-file <path>
skills/moox/scripts/caddy-ca.sh install --ca-file <path>
skills/moox/scripts/caddy-ca.sh status --ca-file <path>
skills/moox/scripts/caddy-ca.sh trust-help --ca-file <path>
```

`inspect` prints subject, SHA-256 fingerprint, and validity. `install` invokes the shared platform adapter and clearly displays the trust store being changed. `status` verifies whether that exact fingerprint is trusted. `trust-help` prints commands for macOS Keychain, Windows certificate import, Linux system trust, `curl --cacert`, and application-specific CA configuration.

- [ ] **Step 5: Cover both browser and backend trust**

Document that interactive deployment can install the CA on the machine running the deployment command, after one confirmation. Other browser machines run `caddy-ca.sh fetch` followed by `install`; they cannot be configured remotely unless the script is executed there. Backend and SCF callers receive the CA automatically as a protected file or base64-encoded configuration and must never use insecure TLS verification.

- [ ] **Step 6: Rewrite obsolete skill guidance**

Make the initial-deployment section begin with an automatic prerequisite phase: inspect target, install/verify managed Caddy if absent, deploy upstream binaries/configuration, start/reload Caddy, configure CA trust, and run HTTPS acceptance. Document Caddy's two HTTPS listeners, web-host/Admin loopback upstreams, CA persistence and rotation, certificate renewal, route isolation, signed health checks, service HMAC over TLS, and conflict recovery. Manual Caddy installation may appear only as troubleshooting guidance, not as a normal user step.

- [ ] **Step 7: Verify and commit**

```bash
bash -n skills/moox/scripts/caddy-prerequisite.sh skills/moox/scripts/test/contract/test-caddy-prerequisite.sh skills/moox/scripts/caddy-ca.sh skills/moox/scripts/test/contract/test-caddy-ca.sh
bash skills/moox/scripts/test/contract/test-caddy-prerequisite.sh
bash skills/moox/scripts/test/contract/test-caddy-ca.sh
git add skills/moox
git commit -m "feat: automate caddy prerequisite deployment"
```

---

### Task 13: Authenticate Shared Health Endpoints

**Files:**
- Modify: `packages/healthz/go.mod`
- Modify: `packages/healthz/go.sum`
- Create: `packages/healthz/auth.go`
- Create: `packages/healthz/auth_test.go`
- Modify: `packages/healthz/healthz.go`
- Modify: `packages/healthz/healthz_test.go`
- Modify: module `go.mod`/`go.sum` files only where direct dependencies change
- Modify: existing `modules/*/internal/health/server.go` adapters only to inject shared auth configuration

- [ ] **Step 1: Write failing health-auth tests**

Cover valid signature, missing header, wrong access key, wrong path, expired timestamp, future timestamp, replayed nonce, concurrent duplicate nonce, and unavailable configuration. Confirm `/healthz`, `/readyz`, and `/metrics` all pass through the same middleware.

- [ ] **Step 2: Implement environment-independent authenticator types**

```go
type AuthConfig struct {
	Version   string
	AccessKey string
	SecretKey string
	ClockSkew time.Duration
	NonceTTL  time.Duration
}

type Authenticator struct {
	// bounded in-memory nonce cache; no database dependencies
}

func NewAuthenticator(cfg AuthConfig) (*Authenticator, error)
func (a *Authenticator) Wrap(next http.Handler) http.Handler
func AuthConfigFromEnv() (AuthConfig, error)
```

Use environment names:

```text
MOOX_HEALTH_AUTH_VERSION=moox-health-v1
MOOX_HEALTH_AUTH_ACCESS_KEY
MOOX_HEALTH_AUTH_SECRET_KEY
MOOX_HEALTH_AUTH_CLOCK_SKEW=60s
```

- [ ] **Step 3: Bound nonce memory**

Use expiration plus a hard maximum entry count. At capacity, purge expired values; if still full, reject new authentication with `503` rather than allowing replay protection to fail open.

- [ ] **Step 4: Keep thin module adapters**

Do not delete `internal/health`. Each adapter obtains one authenticator at startup and wraps `healthz.StandardMux`. Custom EventBus health metrics remain module-owned behind the same middleware.

- [ ] **Step 5: Verify and commit**

```bash
go test -count=1 ./packages/healthz/...
go test -count=1 ./modules/admin/internal/health/... ./modules/eventbus/internal/health/...
git add packages/healthz modules/*/internal/health modules/*/go.mod modules/*/go.sum
git commit -m "feat: require hmac authentication for health endpoints"
```

---

### Task 14: Isolate Every Health Listener from Business and Public Ports

**Files:**
- Modify: `modules/admin/config/trpc_go.yaml`
- Modify: `modules/admin/internal/bootstrap/trpc.go`
- Modify: `modules/admin/internal/gateway/gateway.go`
- Modify: `modules/admin/internal/gateway/init.go`
- Modify: `modules/admin/internal/gateway/config_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: backend/SCF HTTP client and configuration code that calls `/api/service/*`
- Modify: `packages/cloudruntime/runtime.go` and tests
- Modify: service clients under Collector, Factor, Monitor, Trade, and CLI as found by repository search
- Modify: `modules/{archive,cloudnode,collector,eventbus,factor,hostagent,monitor,strategy,trade}/config/trpc_go.yaml`
- Modify: `modules/storage/config/trpc_go*.yaml`
- Modify: `web-host` health listener configuration from Task 9

- [ ] **Step 1: Inventory `/api/service/*` consumers and methods**

Build a checked-in or test-maintained inventory from source showing each machine caller, required service methods, credential source, gateway target source, and whether it runs locally or remotely. At minimum cover:

- `packages/cloudruntime`: CloudNode polling and job-status reporting;
- Collector: CloudNode task submission/invocation, heartbeat/task-status reporting, and SysDeploy discovery;
- Trade: `/api/service/secret/*` access;
- CLI commands that explicitly enable `ServiceAuth`, including cloud-account/firewall workflows.

Remove unused service routes rather than exposing them speculatively. Confirm frontend source is absent from this inventory. Keep one route family and one HMAC implementation; do not create module-specific public service ports.

- [ ] **Step 2: Add negative exposure tests**

Assert:

- Admin gateway `11000` has no `/api/admin/health`, `/healthz`, `/readyz`, or `/metrics` route;
- Admin control gateway `11000` serves `/api/admin/*` only and binds `127.0.0.1`;
- Admin service gateway `11002` serves `/api/service/*` only, binds loopback, and rejects `/api/admin/*` and diagnostics;
- Caddy service edge `11001` is the only public service listener and allowlists `/api/service/*`;
- Admin health is registered only as `trpc.moox.admin.Health` on `127.0.0.1:11010`;
- web-host public handler returns `404` for diagnostics;
- every configured health service has an explicit loopback IP and a port distinct from its business service.

- [ ] **Step 3: Split browser control and backend service routers**

Register two `http_no_protocol` services in the same Admin binary:

```yaml
- name: trpc.moox.gateway.control
  ip: 127.0.0.1
  port: 11000
  protocol: http_no_protocol
- name: trpc.moox.gateway.service
  ip: 127.0.0.1
  port: 11002
  protocol: http_no_protocol
```

The control router contains only `/api/admin/{service}/{method}` and uses browser session authentication. The service router contains only `/api/service/{service}/{method}` and always invokes existing service HMAC validation. Browser JWT must never satisfy service authentication, and service HMAC must never satisfy Admin authentication. Do not use one catch-all router on both listeners.

- [ ] **Step 4: Move Admin health off both gateways**

Register `adminhealth.Register(s.Service("trpc.moox.admin.Health"), state)` from bootstrap. Remove all diagnostic routes from `gateway.go` and remove `/api/admin/health` from configuration and docs.

- [ ] **Step 5: Move service callers to the service-only path**

Use `http://127.0.0.1:11002` only for same-host internal callers. Publish the SysDeploy `service_gateway` as `https://<public-host>:11001`, which terminates at Caddy and retains the existing service HMAC contract. Keep the browser Caddy upstream at `127.0.0.1:11000`. Update CloudRuntime, Collector, Factor, Monitor, Trade secret client, CLI, and all tests that assert service-gateway defaults.

- [ ] **Step 6: Add private-CA support to backend clients**

Support:

```text
MOOX_SERVICE_GATEWAY_CA_FILE
MOOX_SERVICE_GATEWAY_CA_PEM_B64
```

Build an `http.Transport` rooted in the supplied Caddy CA while retaining normal hostname/IP verification. SCF deployments use the base64 form when a mounted file is unavailable. Reject non-loopback plain HTTP service URLs and reject insecure TLS flags; allow loopback HTTP for same-host calls. Add tests for trusted CA, wrong CA, SAN mismatch, missing CA, HTTPS success, and loopback HTTP.

- [ ] **Step 7: Bind Admin internal RPC services to loopback**

Add `ip: 127.0.0.1` to Auth, DNS, SSH, SpaceMgr, SecretMgr, and SysDeploy entries on ports `11100` through `11109`. This prevents direct network callers from bypassing the gateway authentication layers.

- [ ] **Step 8: Normalize existing module health services**

Add explicit `ip: 127.0.0.1` to local health service entries. Keep existing dedicated ports unless they collide. Correct SysDeploy defaults to point to the actual health port rather than a business/public port.

- [ ] **Step 9: Define the remote-host override**

Document that a health listener may bind a private IP only when Monitor is on another host. Public `0.0.0.0` health binds are forbidden; firewall rules must allow only Monitor source addresses.

- [ ] **Step 10: Verify and commit**

```bash
go test -count=1 ./modules/admin/internal/gateway/... ./modules/admin/internal/bootstrap/... ./modules/admin/internal/service/sysdeploy/...
rg -n 'healthz|readyz' modules/*/config web-host modules/admin/internal/gateway
git add modules web-host
git commit -m "refactor: isolate edge and internal listeners"
```

---

### Task 15: Make Monitor Sign Every Health Probe

**Files:**
- Modify: `modules/monitor/internal/config/config.go`
- Modify: `modules/monitor/internal/config/config_test.go`
- Modify: `modules/monitor/config/app.yaml`
- Modify: `modules/monitor/internal/probe/http.go`
- Modify: `modules/monitor/internal/probe/probe.go`
- Modify: `modules/monitor/internal/probe/probe_test.go`
- Modify: `modules/monitor/internal/scheduler/scheduler.go`
- Modify: `modules/monitor/internal/rpc/service.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`

- [ ] **Step 1: Add signed-probe tests**

Create an authenticated `httptest.Server` and assert the Monitor probe succeeds only when it dynamically signs the exact method/path/body with a fresh timestamp and nonce. Two sequential probes must use different nonces. Non-health manual checks must remain unsigned unless explicitly configured.

- [ ] **Step 2: Add health-auth configuration**

```go
type HealthAuthConfig struct {
	Version   string `yaml:"version"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
}
```

Load access/secret values from `MOOX_HEALTH_AUTH_ACCESS_KEY` and `MOOX_HEALTH_AUTH_SECRET_KEY`. Fail startup when SysDeploy health monitoring is enabled but credentials are missing.

- [ ] **Step 3: Inject a signer into `HTTPRunner`**

Replace the empty struct with:

```go
type HTTPRunner struct {
	HealthSigner *HealthSigner
	Client       *http.Client
	Now          func() time.Time
}
```

Sign exact `/healthz`, `/readyz`, and `/metrics` paths for SysDeploy-sourced checks. Preserve user-configured non-auth headers.

- [ ] **Step 4: Verify and commit**

```bash
go test -count=1 ./modules/monitor/internal/probe/... ./modules/monitor/internal/config/... ./modules/monitor/internal/scheduler/...
git add modules/monitor
git commit -m "feat: sign monitor health probes"
```

---

### Task 16: Generate and Inject Dedicated Health Credentials During Deployment

**Files:**
- Modify: `scripts/deploy/deploy-moox.sh`
- Modify: `scripts/test/contract/test-deploy-moox-eventbus.sh`
- Modify: `scripts/test/contract/test-deploy-moox-storage-split.sh`
- Modify: `scripts/test/contract/test-deploy-moox-https.sh`
- Modify: `skills/moox/scripts/hostagent-deploy.sh`
- Modify: `skills/moox/scripts/test/contract/test-hostagent-deploy.sh`

- [ ] **Step 1: Add deployment tests for secret lifecycle**

Assert:

- first deployment generates a 32-byte health secret into a `0600` file;
- redeployment preserves it;
- the secret is absent from stdout, release archives, process argv, checked-in YAML, and generated public files;
- all health-serving processes and Monitor receive the same credential through a protected runtime environment file;
- generated `healthcheck.sh` signs probes and rejects unsigned probes.

- [ ] **Step 2: Store health credentials separately**

Create under deployment root:

```text
secrets/health-auth.env  mode 0600
```

Contents:

```text
MOOX_HEALTH_AUTH_VERSION=moox-health-v1
MOOX_HEALTH_AUTH_ACCESS_KEY=monitor
MOOX_HEALTH_AUTH_SECRET_KEY=<64-lowercase-hex>
```

Generate with a `moox-admin-cli random-secret --bytes 32` command backed by `crypto/rand` (added with tests in Task 6), not shell `$RANDOM` or UUIDs.

- [ ] **Step 3: Update runtime helpers**

`start.sh` sources the protected file before starting services. `wait_http` and `healthcheck.sh` compute a fresh signed header for each probe. Never use a static captured header.

Install the Caddy root CA into backend runtime configuration through `MOOX_SERVICE_GATEWAY_CA_FILE` or `MOOX_SERVICE_GATEWAY_CA_PEM_B64`. Do not place CA private keys or service HMAC secrets in release archives.

- [ ] **Step 4: Update Host Agent deployment**

Allow `hostagent-deploy.sh --health-auth-file <path>` and install it as `0600`. Do not make remote Host Agent health publicly reachable; default it to loopback.

- [ ] **Step 5: Verify and commit**

```bash
bash scripts/test/contract/test-deploy-moox-https.sh
bash scripts/test/contract/test-deploy-moox-eventbus.sh
bash scripts/test/contract/test-deploy-moox-storage-split.sh
bash skills/moox/scripts/test/contract/test-hostagent-deploy.sh
git add scripts skills/moox/scripts
git commit -m "feat: provision dedicated health authentication"
```

---

### Task 17: Rewrite Security, Architecture, Monitoring, and Operations Documentation

**Files:**
- Modify: `README.md`
- Modify: `docs/认证鉴权.md`
- Modify: `docs/架构总览.md`
- Modify: `docs/大仓架构.md`
- Modify: `docs/监控配置.md`
- Create: `docs/运维/管理台HTTPS与证书.md`
- Modify: `docs/SUMMARY.md`
- Modify: `web-host/README.md`
- Modify: `skills/moox/references/release.md`

- [ ] **Step 1: Replace stale authentication claims**

Remove documentation claiming passwords are stored as SHA256 plus user salt, that login salt alone prevents replay, that modification-password RPCs exist, or that registration is public. Document bcrypt, one-time login salt, 24-hour JWT/session expiration, request HMAC, nonce replay protection, raw tickets, CLI provisioning, and logout invalidation.

Document the API audience explicitly: `/api/admin/*` is the browser/user control plane; `/api/service/*` is the machine-to-machine control plane. List the current service consumers and explain that frontend code must never call `/api/service/*`.

- [ ] **Step 2: Replace the old network topology**

Document:

```text
Browser --HTTPS--> Caddy:9527 --HTTP loopback--> web-host:9528 or admin-control:11000
Backend/SCF --HTTPS + service HMAC--> Caddy:11001 --HTTP loopback--> admin-service:11002
Monitor --health HMAC--> dedicated internal health ports
```

State that Caddy, not web-host, proxies traffic; browsers never connect directly to Admin, and backend service calls do not traverse web-host or the browser listener.

- [ ] **Step 3: Document certificate operations completely**

Include first deployment, Caddy data persistence, CA retrieval, macOS/Windows/Linux trust installation, browser-warning behavior, backend/SCF CA injection, `curl --cacert`, certificate inspection, IP/DNS SAN changes, automatic leaf renewal, CA rotation consequences, rollback, file permissions, and recovery when the CA private key is lost.

- [ ] **Step 4: Document endpoint exposure and port policy**

Include a table of public, loopback, and configurable-private listeners. Explicitly state that the browser edge exposes only site routes and `/api/admin/*`, the service edge exposes only `/api/service/*`, diagnostics return `404` on both public ports, and diagnostic ports return `401` without health HMAC.

- [ ] **Step 5: Run documentation checks and commit**

```bash
rg -n 'web-host.*only|direct.*11000|SHA256\(password|修改密码|/api/admin/health' README.md docs skills/moox web-host/README.md
pnpm run docs:build
git add README.md docs web-host/README.md skills/moox/references/release.md
git commit -m "docs: describe the secured admin control plane"
```

Expected: the grep returns no stale normative statements; historical review documents may retain quoted findings only when clearly marked historical.

---

### Task 18: Run Full Security Acceptance and Independent Code Review

**Files:**
- Modify only files required by verified review findings.

- [ ] **Step 1: Run formatting and static repository checks**

```bash
gofmt -w packages/crypto packages/requestauth packages/healthz web-host modules/admin modules/monitor packages/cloudruntime
bash scripts/check/check-package-boundaries.sh
bash scripts/check/check-module-boundaries.sh
bash -n scripts/*.sh skills/moox/scripts/*.sh
bin/caddy validate --config deploy/caddy/Caddyfile --adapter caddyfile
```

Expected: all commands pass.

- [ ] **Step 2: Run fresh Go tests for every changed module**

```bash
go test -count=1 ./packages/crypto/...
go test -count=1 ./packages/requestauth/...
go test -count=1 ./packages/healthz/...
go test -count=1 ./web-host/...
go test -count=1 ./modules/admin/...
go test -count=1 ./modules/monitor/...
go test -count=1 ./modules/cloudnode/...
go test -count=1 ./modules/collector/...
go test -count=1 ./modules/eventbus/...
go test -count=1 ./modules/factor/...
go test -count=1 ./modules/storage/...
go test -count=1 ./modules/strategy/...
go test -count=1 ./modules/trade/...
go test -count=1 ./modules/archive/...
go test -count=1 ./modules/hostagent/...
```

- [ ] **Step 3: Run frontend and deployment tests**

```bash
pnpm --dir web test --run
pnpm --dir web build:prod
bash scripts/test/contract/test-deploy-moox-https.sh
bash scripts/test/contract/test-deploy-moox-eventbus.sh
bash scripts/test/contract/test-deploy-moox-storage-split.sh
bash scripts/test/contract/test-deploy-moox-preserve-disabled.sh
bash scripts/test/contract/test-caddy-config.sh
bash scripts/test/contract/test-install-caddy-ca.sh
bash skills/moox/scripts/test/contract/test-caddy-prerequisite.sh
bash skills/moox/scripts/test/contract/test-caddy-ca.sh
bash skills/moox/scripts/test/contract/test-hostagent-deploy.sh
```

- [ ] **Step 4: Run local end-to-end security probes**

Deploy to a temporary directory and prove:

```text
PASS: trusted CA opens https://127.0.0.1:9527 without warning in the test client
PASS: trusted CA opens https://127.0.0.1:11001 and a valid service HMAC reaches Admin service
PASS: production frontend assets contain no direct :11000 target and every MooX browser API uses same-origin /api/admin/*
PASS: frontend source contains no /api/service/* request
PASS: pre-signed object-storage uploads contain no MooX JWT, request-signing, service-HMAC, or space headers
PASS: /api/admin/auth/GetLoginSalt and Login work without JWT/HMAC
PASS: every other /api/admin route rejects missing JWT, missing HMAC, bad body, expired timestamp, and nonce replay
PASS: a valid session survives process/browser-test restart within 24 hours
PASS: an expired session requires login and cannot be refreshed by activity
PASS: browser edge returns 404 for /api/service, /healthz, /readyz, /metrics, and unknown /api paths
PASS: service edge returns 404 for /api/admin, site paths, /healthz, /readyz, and /metrics
PASS: direct external connections to web-host:9528, Admin control:11000, and Admin service:11002 fail
PASS: wrong CA, SAN mismatch, missing service HMAC, and replayed service HMAC all fail closed
PASS: internal health endpoints reject unsigned requests and accept Monitor signatures
PASS: WebSocket and SFTP one-time tickets reject mismatch, expiry, and replay
PASS: Caddy redeployment preserves CA fingerprint and automatically serves a valid leaf
PASS: missing or stopped MooX-managed Caddy is installed/started before web-host deployment succeeds
PASS: the documented MooX skill initial-deployment command installs Caddy on a clean target without a manual prerequisite step
PASS: an already-running healthy MooX-managed Caddy is validated and reloaded without spawning a duplicate
PASS: stale PID files and unrelated system Caddy/listener processes are never signaled or overwritten
PASS: a Caddy port conflict or failed reload/start rolls back and fails deployment with actionable diagnostics
PASS: interactive deployment fetches, confirms, and idempotently installs the CA on the operator machine
PASS: non-interactive deployment never changes local trust without --local-ca install
PASS: deployed backend and SCF configuration receives the CA automatically without insecure TLS flags
PASS: unexpected CA replacement blocks deployment until the old fingerprint is explicitly acknowledged
```

- [ ] **Step 5: Start a fresh review agent**

Use `superpowers:requesting-code-review` with a new agent. Ask it to inspect the complete diff against this plan, with special attention to raw-body canonicalization, nonce atomicity, key leakage, session expiry, Caddy route ordering and allowlisting, WebSocket upgrades, Caddy data persistence, CA propagation without disabled verification, health fail-closed behavior, deployment idempotence, and stale documentation. The reviewer must not modify code.

- [ ] **Step 6: Fix confirmed findings and rerun affected tests**

Apply `superpowers:receiving-code-review`; verify each finding against source before editing. Add regression tests before fixes. Repeat the independent review if any P0/P1 finding changes authentication or TLS behavior.

- [ ] **Step 7: Final repository verification**

```bash
git status --short --branch
git diff --check
git log --oneline --decorate -12
```

Expected: no uncommitted files and no whitespace errors.

- [ ] **Step 8: Push and verify synchronization**

```bash
git push origin main
git status --short --branch
```

Expected: `## main...origin/main` with no ahead/behind count and no local changes.

---

## Completion Criteria

- Caddy is the only public edge: browser HTTPS on `:9527` and backend HTTPS on `:11001`.
- The browser edge serves site traffic and proxies only `/api/admin/*`; the service edge proxies only `/api/service/*`.
- Every browser-originated MooX API call uses same-origin `/api/admin/*`; `/api/service/*` remains machine-to-machine only.
- External pre-signed uploads remain direct but never receive MooX authentication or tenant headers.
- Web-host, Admin control, and Admin service bind loopback and are unreachable directly from external networks.
- Admin control routes and all diagnostics are absent from direct public ports.
- Remote service traffic uses TLS with the Caddy CA plus the existing replay-resistant service HMAC; local same-host callers may use loopback HTTP on `:11002`.
- Only GetLoginSalt and Login are unauthenticated management RPCs.
- Every other management operation requires an unexpired 24-hour JWT session and valid replay-resistant HMAC proof, or a one-time raw-operation ticket issued through that proof.
- Password registration/reset exists only through local CLI; no password-change UI or RPC remains.
- Caddy v2.11.4 installation is checksum-verified, rootless, idempotent, permission-safe, and documented in the MooX skill.
- A clean target with no Caddy can complete the MooX skill's initial deployment without manual package installation; the skill and release script share one prerequisite implementation.
- Web-host deployment verifies the target's managed Caddy binary, version, checksum, PID ownership, admin health, configuration, and listeners; it installs, starts, or reloads only the MooX-managed instance.
- Existing unrelated Caddy installations and occupied ports are reported and left untouched; failed edge updates restore the prior managed configuration.
- Caddy CA state survives deployment; same-host backend and generated SCF configuration receive it automatically without disabled TLS verification.
- Interactive deployment offers one-confirmation CA installation on the operator machine; non-interactive trust changes are explicit, fingerprint-checked, cross-platform, and idempotent.
- Every health/ready endpoint uses a dedicated internal listener and Monitor-only HMAC credentials.
- Existing `internal/health` adapters remain thin and module-owned.
- Existing unrelated/untracked files are resolved in separate commits.
- Full fresh tests, deployment security probes, and an independent code review pass before push.
