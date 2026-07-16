# Custom Setup Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a repository-root `custom.toml` workflow that validates user credentials, deploys the minimal control plane, and atomically initializes the first Admin user, Tencent Cloud credential, and SSH hosts without exposing secrets to an Agent.

**Architecture:** `moox-cli setup` owns strict TOML loading, Tencent/SSH validation, control-plane deployment, SSH tunneling, and sanitized output. Admin owns a loopback-only setup RPC and one transaction that writes all initial records plus an HMAC manifest fingerprint. The MooX Skill invokes only the high-level commands and never reads `custom.toml`.

**Tech Stack:** Go 1.24, Cobra, BurntSushi TOML, `golang.org/x/crypto/ssh`, tRPC-Go HTTP/PB, GORM/SQLite, Tencent Cloud SDK, Bash deployment contract tests, MooX Skill Markdown.

---

## File Map

The CLI setup implementation is grouped by capability rather than flattened
into prefixed package names:

```text
modules/cli/internal/setup/
├── config/
├── ssh/
├── validate/
├── client/
└── deploy/
```

- Create `custom.toml.example`: exact user-facing setup template with empty secrets.
- Modify `.gitignore`: ignore only repository-root `/custom.toml`.
- Create `modules/cli/internal/setup/config/config.go`: secure file snapshot, strict TOML decode, canonical manifest, and field validation.
- Create `modules/cli/internal/setup/config/config_test.go`: file security, parsing, uniqueness, canonicalization, and mutation tests.
- Create `modules/cli/internal/setup/ssh/client.go`: password SSH, dedicated known-hosts policy, connection validation, and local forwarding.
- Create `modules/cli/internal/setup/ssh/client_test.go`: SSH fixture, unknown-key behavior, auth failure redaction, and forwarding tests.
- Create `modules/cli/internal/setup/validate/validate.go`: ordered local, Tencent, and SSH validation with stable result codes.
- Create `modules/cli/internal/setup/validate/validate_test.go`: validator ordering, per-host results, and output secrecy.
- Create `modules/admin/proto/setup_service.proto`: loopback setup apply/status contract.
- Regenerate `modules/admin/proto/admingen/setup_service*.go`: generated PB and tRPC bindings.
- Modify `modules/admin/schema/admin.sql`: add singleton setup state and cloud-secret uniqueness.
- Create `modules/admin/internal/service/setup/service.go`: transactional create-or-verify domain.
- Create `modules/admin/internal/service/setup/service_test.go`: commit, rollback, retry, conflict, hash/encryption, and adoption tests.
- Create `modules/admin/internal/service/setup/rpc/service.go`: PB mapping with sanitized errors.
- Create `modules/admin/internal/service/setup/rpc/service_test.go`: RPC result and redaction tests.
- Modify `modules/admin/internal/bootstrap/services.go`: construct the setup service with the shared DB and encryption key.
- Modify `modules/admin/internal/bootstrap/trpc.go`: register the private service.
- Modify `modules/admin/config/trpc_go.yaml`: bind the setup listener to `127.0.0.1:11110`.
- Modify `modules/admin/internal/bootstrap/config_test.go`: enforce loopback binding.
- Create `modules/cli/internal/setup/client/client.go`: SSH-forwarded Admin setup apply/status client.
- Create `modules/cli/internal/setup/client/login.go`: public GetLoginSalt/Login verification without token output.
- Create `modules/cli/internal/setup/client/client_test.go`: forwarded requests, timeout, conflict, and secret-free errors.
- Create `modules/cli/internal/setup/client/login_test.go`: browser-path login protocol and response redaction.
- Create `modules/cli/internal/setup/deploy/deploy.go`: minimal control profile orchestration and safe Go SSH/SFTP transport.
- Create `modules/cli/internal/setup/deploy/deploy_test.go`: profile arguments, upload behavior, readiness ordering, and no-secret output.
- Modify `scripts/deploy-moox.sh`: add `control` profile and remove deploy-time Admin user creation.
- Modify `scripts/test-deploy-moox-admin-bootstrap.sh`: enforce the new deployment/setup boundary.
- Create `scripts/test-deploy-moox-control-profile.sh`: verify exact control-profile contents and service startup.
- Create `modules/cli/internal/command/setup.go`: register `validate`, `deploy-control`, `apply`, `status`, and host trust commands.
- Create `modules/cli/internal/command/setup_test.go`: CLI contract and stdout/stderr secrecy.
- Modify `modules/cli/internal/command/root.go`: show the setup command without eager unrelated YAML warnings.
- Modify `modules/cli/go.mod` and `modules/cli/go.sum`: make TOML and SSH dependencies direct.
- Create `skills/moox/references/custom-setup.md`: operator contract and exact template.
- Modify `skills/moox/SKILL.md`: mandatory two-stage setup sequence and prohibition on Agent file reads.
- Create `skills/moox/scripts/test-custom-setup-contract.sh`: check template, ignore rule, command order, and forbidden secret-reading patterns.
- Modify `modules/cli/README.md`: document `setup` commands and the read-only manifest contract.

## Task 1: Define And Secure The Manifest

**Files:**
- Create: `custom.toml.example`
- Modify: `.gitignore`
- Create: `modules/cli/internal/setup/config/config.go`
- Create: `modules/cli/internal/setup/config/config_test.go`
- Modify: `modules/cli/go.mod`
- Modify: `modules/cli/go.sum`

- [ ] **Step 1: Add the ignored template contract**

Create `custom.toml.example` with the exact approved structure and no real values:

```toml
[admin]
username = "admin"
password = ""

[tencent_cloud]
secret_id = ""
secret_key = ""

[control_host]
name = "control"
address = ""
port = 22
username = "ubuntu"
password = ""

[[other_hosts]]
name = "compute-1"
address = ""
port = 22
username = "ubuntu"
password = ""
```

Add `/custom.toml` to `.gitignore`. Do not ignore every `*.toml`; module files and future checked-in TOML remain trackable.

- [ ] **Step 2: Write failing secure-load tests**

Create table-driven tests for the approved structure, missing required values, unknown fields, duplicate host names, duplicate addresses, a missing `other_hosts` array, a 73-byte Admin password, symlink input, mode `0644`, wrong basename, and mutation during a held snapshot. The core success assertion is:

```go
snapshot, err := Load(filepath.Join(root, "custom.toml"), root)
require.NoError(t, err)
assert.Equal(t, "admin", snapshot.Manifest.Admin.Username)
assert.Equal(t, 22, snapshot.Manifest.ControlHost.Port)
assert.Empty(t, snapshot.Manifest.OtherHosts)
require.NoError(t, snapshot.VerifyUnchanged())
```

- [ ] **Step 3: Run the focused test and verify failure**

Run:

```bash
cd modules/cli
go test -count=1 ./internal/setup/config
```

Expected: FAIL because `config.Load` and its types do not exist.

- [ ] **Step 4: Implement strict loading and canonicalization**

Use `toml.Decode` and reject undecoded keys:

```go
type Manifest struct {
    Admin        Admin        `toml:"admin"`
    TencentCloud TencentCloud `toml:"tencent_cloud"`
    ControlHost  Host         `toml:"control_host"`
    OtherHosts   []Host       `toml:"other_hosts"`
}

func decodeStrict(raw []byte, out *Manifest) error {
    md, err := toml.Decode(string(raw), out)
    if err != nil {
        return fmt.Errorf("config_invalid: decode custom.toml")
    }
    if keys := md.Undecoded(); len(keys) != 0 {
        return fmt.Errorf("config_invalid: unknown field %s", keys[0].String())
    }
    if out.ControlHost.Port == 0 {
        out.ControlHost.Port = 22
    }
    for i := range out.OtherHosts {
        if out.OtherHosts[i].Port == 0 {
            out.OtherHosts[i].Port = 22
        }
    }
    return validate(out)
}
```

Open with `os.Open` after `Lstat`, reject symlinks and non-regular files, compare `os.SameFile` after opening, require current-user ownership and `0600`, read through a size-limited buffer, and retain only the immutable bytes, SHA-256, and file metadata needed by `VerifyUnchanged`.

Canonicalize with length-prefixed fields and hosts sorted by `(name,address,port,username)`; never log or expose the canonical bytes.

- [ ] **Step 5: Promote dependencies and run tests**

Make `github.com/BurntSushi/toml v1.3.2` a direct dependency. Run:

```bash
cd modules/cli
go mod tidy
go test -count=1 ./internal/setup/config
```

Expected: PASS.

- [ ] **Step 6: Commit the manifest loader**

```bash
git add .gitignore custom.toml.example modules/cli/go.mod modules/cli/go.sum modules/cli/internal/setup/config
git commit -m "feat(cli): define secure setup manifest"
```

## Task 2: Add SSH Trust, Authentication, And Forwarding

**Files:**
- Create: `modules/cli/internal/setup/ssh/client.go`
- Create: `modules/cli/internal/setup/ssh/client_test.go`
- Modify: `modules/cli/go.mod`
- Modify: `modules/cli/go.sum`

- [ ] **Step 1: Write failing SSH behavior tests**

Start an in-process SSH server with a fixed host key and password. Cover:

```go
func TestDialRejectsUnknownHostKey(t *testing.T) {
    _, err := Dial(context.Background(), target, password, Options{KnownHostsPath: emptyFile})
    require.ErrorIs(t, err, ErrHostKeyUnknown)
    assert.Contains(t, err.Error(), ssh.FingerprintSHA256(serverPublicKey))
    assert.NotContains(t, err.Error(), password)
}

func TestForwardConnectsOnlyThroughSSH(t *testing.T) {
    client := dialTrustedFixture(t)
    listener, err := client.ForwardLocal(context.Background(), "127.0.0.1:11110")
    require.NoError(t, err)
    assertSetupHTTPRoundTrip(t, listener.Addr().String())
}
```

Also assert that wrong passwords produce `ssh_auth_failed`, unreachable ports produce `ssh_unreachable`, and neither captured output nor errors contain the password.

- [ ] **Step 2: Run and verify the package is missing**

```bash
cd modules/cli
go test -count=1 ./internal/setup/ssh
```

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement known-hosts and password SSH**

Inside the `setup/ssh` package, import `golang.org/x/crypto/ssh` as `xssh`.
Build `xssh.ClientConfig` with `xssh.Password`, a context deadline, and
`knownhosts.New`. Never use `xssh.InsecureIgnoreHostKey`. Wrap callback
failures:

```go
var (
    ErrHostKeyUnknown = errors.New("host_key_unknown")
    ErrAuthFailed     = errors.New("ssh_auth_failed")
    ErrUnreachable    = errors.New("ssh_unreachable")
)

type Client interface {
    Check(ctx context.Context) error
    ForwardLocal(ctx context.Context, remote string) (net.Listener, error)
    Upload(ctx context.Context, src io.Reader, size int64, dst string, mode fs.FileMode) error
    Run(ctx context.Context, argv []string, stdin io.Reader) (Result, error)
    Close() error
}
```

Use a dedicated `~/.config/moox/known_hosts` file and require it to be regular and `0600`. Add a separate `TrustHost` function that appends the exact scanned key only after the user invokes an explicit trust command.

- [ ] **Step 4: Implement local forwarding and SFTP upload**

The forwarder listens on `127.0.0.1:0`, accepts local connections, opens `client.Dial("tcp", remote)`, copies both directions, and closes all connections on context cancellation. SFTP uploads to a random remote `.next` file, applies the requested mode, fsyncs, then renames atomically.

- [ ] **Step 5: Run tests and dependency cleanup**

```bash
cd modules/cli
go mod tidy
go test -count=1 ./internal/setup/ssh
```

Expected: PASS; `golang.org/x/crypto` is a direct dependency.

- [ ] **Step 6: Commit SSH support**

```bash
git add modules/cli/go.mod modules/cli/go.sum modules/cli/internal/setup/ssh
git commit -m "feat(cli): add trusted setup SSH transport"
```

## Task 3: Build Sanitized Preflight Validation

**Files:**
- Create: `modules/cli/internal/setup/validate/validate.go`
- Create: `modules/cli/internal/setup/validate/validate_test.go`
- Modify: `modules/cli/internal/tencentcloud/lighthouse.go`
- Modify: `modules/cli/internal/tencentcloud/lighthouse_test.go`

- [ ] **Step 1: Write failing validation-order tests**

Define fakes and assert local config failure makes no network calls, Tencent failure prevents SSH, and successful validation checks control first then other hosts in file order:

```go
result, err := Validate(ctx, snapshot, Dependencies{Tencent: tc, SSH: sshFactory})
require.NoError(t, err)
assert.Equal(t, []Check{
    {Name: "config", Status: "valid"},
    {Name: "tencent_cloud", Status: "valid"},
    {Name: "host:control", Status: "valid"},
    {Name: "host:compute-1", Status: "valid"},
}, result.Checks)
```

Feed recognizable secret strings through every fake error and assert they do not occur in `Result`, returned errors, stdout, or stderr.

- [ ] **Step 2: Add a read-only Tencent identity seam**

Introduce:

```go
type IdentityAPI interface {
    GetCallerIdentity(context.Context) (CallerIdentity, error)
}

type CallerIdentity struct {
    UIN string `json:"uin"`
}
```

Implement it with Tencent STS `GetCallerIdentity`. Map SDK authentication failures to `tencent_auth_failed` and discard the SDK error text after logging only a non-sensitive request ID.

- [ ] **Step 3: Run the failing tests**

```bash
cd modules/cli
go test -count=1 ./internal/setup/validate ./internal/tencentcloud
```

Expected: FAIL until the validator and identity seam exist.

- [ ] **Step 4: Implement ordered validation**

`validate.Run` receives an already-secure `config.Snapshot`. It returns stable
codes and host names only. Use bounded concurrency for `other_hosts`, but
preserve configured order in output. Always call
`snapshot.VerifyUnchanged()` before returning.

- [ ] **Step 5: Run and commit**

```bash
cd modules/cli
go test -count=1 ./internal/setup/validate ./internal/tencentcloud
git add modules/cli/internal/setup/validate modules/cli/internal/tencentcloud
git commit -m "feat(cli): validate setup credentials safely"
```

## Task 4: Add The Transactional Admin Setup Domain

**Files:**
- Modify: `modules/admin/schema/admin.sql`
- Modify: `modules/admin/schema/schema_test.go`
- Create: `modules/admin/internal/service/setup/service.go`
- Create: `modules/admin/internal/service/setup/service_test.go`

- [ ] **Step 1: Write the schema failure test**

Require a singleton state table and a unique active Tencent setup credential:

```sql
CREATE TABLE IF NOT EXISTS t_system_setup (
    c_id INTEGER NOT NULL PRIMARY KEY CHECK (c_id = 1),
    c_status TEXT NOT NULL CHECK (c_status = 'completed'),
    c_manifest_hmac TEXT NOT NULL,
    c_completed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_secrets_tencent_default_active
ON t_secrets(c_secret_id, c_is_deleted)
WHERE c_secret_id = 'tencent-default' AND c_is_deleted = 0;
```

Run `go test -count=1 ./schema`; expect failure before editing `admin.sql`.

- [ ] **Step 2: Write transactional domain tests**

Test `created`, identical `unchanged`, different-manifest `ErrConflict`, exact preexisting-row adoption, conflicting-row rejection, and rollback after injected failures at user, secret, each host, and state writes. Verify:

```go
assert.True(t, mooxcrypto.VerifyPassword(input.Admin.Password, user.PasswordHash))
assert.NotEqual(t, input.TencentCloud.SecretKey, storedSecret.SecretValue)
assert.NotEqual(t, input.ControlHost.Password, storedHost.Password)
assert.Equal(t, hmacHex(encryptionKey, canonical), state.ManifestHMAC)
```

- [ ] **Step 3: Implement transaction-scoped repositories**

Do not call DAO methods bound to the outer DB. Construct repositories from the transaction handle:

```go
func (s *Service) Apply(ctx context.Context, in Manifest) (Result, error) {
    var out Result
    err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        fingerprint := manifestHMAC(s.encryptionKey, canonicalManifest(in))
        state, err := loadState(tx)
        if err != nil { return err }
        if state != nil {
            if subtle.ConstantTimeCompare([]byte(state.ManifestHMAC), []byte(fingerprint)) == 1 {
                out.Action = "unchanged"
                return nil
            }
            return ErrConflict
        }
        if err := ensureAdmin(tx, in.Admin); err != nil { return err }
        if err := ensureTencentSecret(tx, in.TencentCloud, s.encryptionKey); err != nil { return err }
        if err := ensureHosts(tx, in.Hosts(), s.encryptionKey); err != nil { return err }
        if err := insertState(tx, fingerprint); err != nil { return err }
        out.Action = "created"
        return nil
    })
    return out, err
}
```

Reuse `packages/crypto` for bcrypt and encryption. Add `cloud` to the accepted SecretMgr category set so later management operations recognize the record.

- [ ] **Step 4: Run Admin domain tests**

```bash
cd modules/admin
go test -count=1 ./schema ./internal/service/setup ./internal/service/secret/rpc
```

Expected: PASS.

- [ ] **Step 5: Commit the domain**

```bash
git add modules/admin/schema modules/admin/internal/service/setup modules/admin/internal/service/secret/rpc
git commit -m "feat(admin): add atomic system setup domain"
```

## Task 5: Expose A Loopback-Only Setup RPC

**Files:**
- Create: `modules/admin/proto/setup_service.proto`
- Regenerate: `modules/admin/proto/admingen/setup_service.pb.go`
- Regenerate: `modules/admin/proto/admingen/setup_service.trpc.go`
- Create: `modules/admin/internal/service/setup/rpc/service.go`
- Create: `modules/admin/internal/service/setup/rpc/service_test.go`
- Modify: `modules/admin/internal/bootstrap/services.go`
- Modify: `modules/admin/internal/bootstrap/trpc.go`
- Modify: `modules/admin/config/trpc_go.yaml`
- Modify: `modules/admin/internal/bootstrap/config.go`
- Modify: `modules/admin/internal/bootstrap/config_test.go`

- [ ] **Step 1: Define the private protocol**

Add messages with `password`, `secret_id`, and `secret_key` fields only in requests. Responses contain action and counts; the stored HMAC never leaves Admin:

```proto
service Setup {
  rpc ApplySetup(ApplySetupReq) returns (ApplySetupRsp);
  rpc GetSetupStatus(GetSetupStatusReq) returns (GetSetupStatusRsp);
}

message ApplySetupReq {
  SetupAdmin admin = 1;
  SetupTencentCloud tencent_cloud = 2;
  SetupHost control_host = 3;
  repeated SetupHost other_hosts = 4;
}
```

Do not add this service to any public gateway service-ID map.

- [ ] **Step 2: Generate bindings**

```bash
make proto
```

Expected: generated `setup_service.pb.go` and `setup_service.trpc.go` compile.

- [ ] **Step 3: Write failing RPC and binding tests**

Test successful mapping, `setup_conflict`, storage failure, and status. Marshal every response and assert it contains none of the request secrets.

Add a config test that changes the setup bind address to `0.0.0.0:11110` and expects `setup listener must bind to loopback`.

- [ ] **Step 4: Implement RPC mapping and service wiring**

Register only on the dedicated service:

```go
adminpb.RegisterSetupService(
    s.Service("trpc.moox.admin.Setup"),
    setuprpc.NewService(services.SystemSetup),
)
```

Add `trpc.moox.admin.Setup` to `trpc_go.yaml` with `network: tcp`, `protocol: http`, and `ip: 127.0.0.1`, `port: 11110`. Validate the listener during config load before server startup.

- [ ] **Step 5: Run narrow and module tests**

```bash
cd modules/admin
go test -count=1 ./internal/service/setup/... ./internal/bootstrap ./schema
```

Expected: PASS.

- [ ] **Step 6: Commit the private API**

```bash
git add modules/admin/proto modules/admin/internal/service/setup modules/admin/internal/bootstrap modules/admin/config/trpc_go.yaml
git commit -m "feat(admin): expose loopback setup API"
```

## Task 6: Add The SSH-Forwarded Setup Client

**Files:**
- Create: `modules/cli/internal/setup/client/client.go`
- Create: `modules/cli/internal/setup/client/login.go`
- Create: `modules/cli/internal/setup/client/client_test.go`
- Create: `modules/cli/internal/setup/client/login_test.go`

- [ ] **Step 1: Write failing forwarded-client tests**

Use a fake SSH forwarder and HTTP server to assert the request reaches
`/trpc.moox.admin.Setup/ApplySetup`, status uses
`GetSetupStatus`, context cancellation closes the forward, and remote
responses containing unexpected text are converted to stable errors.

- [ ] **Step 2: Run and verify failure**

```bash
cd modules/cli
go test -count=1 ./internal/setup/client
```

Expected: FAIL because the client does not exist.

- [ ] **Step 3: Implement apply and status**

Use `ssh.Client.ForwardLocal(ctx, "127.0.0.1:11110")`, a private
`http.Client` with a fixed timeout, and PB JSON names. Map responses into:

```go
type ApplyResult struct {
    Action      string `json:"action"`
    Users       int    `json:"users"`
    Secrets     int    `json:"secrets"`
    Hosts       int    `json:"hosts"`
}
```

Never include the request body or raw remote error body in an error. Call
`snapshot.VerifyUnchanged()` immediately before and after the request.

- [ ] **Step 4: Implement public login verification**

Implement `VerifyPublicLogin` against `/api/admin/auth/GetLoginSalt` and
`/api/admin/auth/Login`. Encrypt the password exactly as the Web client does:

```go
encrypted, err := mooxcrypto.Encrypt(password, salt+strconv.FormatInt(timestamp, 10))
```

Accept only `ret_info.code == 0`, then discard `access_token`, `session_id`, and
`request_signing_key`. Tests must prove no password, encrypted password, token,
or signing key appears in the result or error.

- [ ] **Step 5: Run and commit**

```bash
cd modules/cli
go test -count=1 ./internal/setup/client
git add modules/cli/internal/setup/client/client.go modules/cli/internal/setup/client/client_test.go \
  modules/cli/internal/setup/client/login.go modules/cli/internal/setup/client/login_test.go
git commit -m "feat(cli): initialize Admin through SSH forwarding"
```

## Task 7: Separate The Control Deployment Profile

**Files:**
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/test-deploy-moox-admin-bootstrap.sh`
- Create: `scripts/test-deploy-moox-control-profile.sh`

- [ ] **Step 1: Write failing shell contract tests**

The control-profile test must assert:

```bash
scripts/deploy-moox.sh --profile control --no-start ...
test -x "${stage}/bin/moox-admin"
test -x "${stage}/bin/moox-gateway"
test -x "${stage}/bin/moox-web-host"
test ! -e "${stage}/bin/moox-storage"
test ! -e "${stage}/bin/moox-cloudnode"
test ! -e "${stage}/bin/moox-collector"
```

Update the Admin setup test to fail if the script contains or accepts
`--admin-username`, `--admin-password-file`, `BOOTSTRAP_ADMIN`, or `user ensure`.

- [ ] **Step 2: Run and confirm failure**

```bash
bash scripts/test-deploy-moox-admin-bootstrap.sh
bash scripts/test-deploy-moox-control-profile.sh
```

Expected: FAIL against the old deploy-time user creation and missing profile.

- [ ] **Step 3: Implement the explicit profile**

Parse `--profile control` before component flags and set exactly:

```bash
WITH_ADMIN=1
WITH_GATEWAY=1
WITH_WEB_HOST=1
WITH_STORAGE=0
WITH_ARCHIVE=0
WITH_EVENTBUS=0
WITH_CLOUDNODE=0
WITH_COLLECTOR=0
WITH_FACTOR=0
WITH_MONITOR=0
```

Remove deploy-time Admin username/password parsing, password-file reads, and
all local/remote `moox-admin-cli user ensure` calls. Keep server-generated
Admin encryption, JWT, health, service, and Gateway secrets in owner-only files.

- [ ] **Step 4: Run deployment contracts**

```bash
bash scripts/test-deploy-moox-admin-bootstrap.sh
bash scripts/test-deploy-moox-control-profile.sh
bash scripts/test-deploy-moox-gateway.sh
bash scripts/test-deploy-moox-https.sh
```

Expected: PASS.

- [ ] **Step 5: Commit the profile**

```bash
git add scripts/deploy-moox.sh scripts/test-deploy-moox-admin-bootstrap.sh scripts/test-deploy-moox-control-profile.sh
git commit -m "refactor(deploy): separate control setup profile"
```

## Task 8: Orchestrate Control Deployment Without Shell Password Exposure

**Files:**
- Create: `modules/cli/internal/setup/deploy/deploy.go`
- Create: `modules/cli/internal/setup/deploy/deploy_test.go`
- Modify: `scripts/deploy-moox.sh`

- [ ] **Step 1: Add a package-only deploy contract**

Extend `deploy-moox.sh` with `--package-only --archive <path>`. In this mode it
builds the `control` stage and archive, then exits before local/remote sync,
service stop, or SSH. A shell test must assert no `ssh`, `scp`, `rsync`, or
remote command is invoked.

- [ ] **Step 2: Write failing Go orchestration tests**

Inject a `Packager`, `ssh.Client`, and readiness probe. Assert the
order:

```text
package -> upload .next -> atomic install -> start -> admin ready -> setup ready -> gateway ready -> web ready -> browser HTTPS ready
```

Assert captured argv, environment, stdout, stderr, remote commands, and uploaded
archive do not contain any manifest credential.

- [ ] **Step 3: Implement `deploy.Control`**

Use the SSH/SFTP client from Task 2. The packager receives only non-sensitive
deployment values:

```go
type Options struct {
    RepositoryRoot string
    PublicHost     string
    BrowserPort    int
}
```

Derive `PublicHost` from `control_host.address` and use the fixed remote path
`~/moox/prod`. Upload the archive with `0600`,
execute a fixed remote installer already contained in the archive, remove the
archive in a deferred cleanup, and poll signed health commands without printing
headers or secrets.

- [ ] **Step 4: Run Go and shell tests**

```bash
cd modules/cli
go test -count=1 ./internal/setup/deploy ./internal/setup/ssh
cd ../..
bash scripts/test-deploy-moox-control-profile.sh
```

Expected: PASS.

- [ ] **Step 5: Commit deployment orchestration**

```bash
git add modules/cli/internal/setup/deploy scripts/deploy-moox.sh scripts/test-deploy-moox-control-profile.sh
git commit -m "feat(cli): deploy setup control plane safely"
```

## Task 9: Register The Public CLI Contract

**Files:**
- Create: `modules/cli/internal/command/setup.go`
- Create: `modules/cli/internal/command/setup_test.go`
- Modify: `modules/cli/internal/command/root.go`
- Modify: `modules/cli/README.md`

- [ ] **Step 1: Write failing command tests**

Construct the command with injected dependencies and test:

```text
setup validate --file
setup trust-host --file --host control --fingerprint
setup deploy-control --file
setup apply --file
setup status --file
```

Require JSON output with stable fields. Capture both streams and assert no test
Admin password, SSH password, Tencent SecretId, or SecretKey occurs. Also prove
setup commands do not emit the unrelated legacy YAML-load warning from
`loadGlobalConfig`.

- [ ] **Step 2: Run and verify failure**

```bash
cd modules/cli
go test -count=1 ./internal/command -run Setup
```

Expected: FAIL because the command is not registered.

- [ ] **Step 3: Implement the Cobra tree**

Use a shared file flag default:

```go
const defaultSetupFile = "./custom.toml"

func newSetupCommand(deps setupDeps) *cobra.Command {
    cmd := &cobra.Command{Use: "setup", Short: "初始化 MooX 控制面"}
    cmd.AddCommand(
        newSetupValidateCommand(deps),
        newSetupTrustHostCommand(deps),
        newSetupDeployCommand(deps),
        newSetupApplyCommand(deps),
        newSetupStatusCommand(deps),
    )
    return cmd
}
```

Load `custom.toml` inside each `RunE`, never during package initialization.
Print only typed result structs through `json.Encoder`. Mark all secret-bearing
variables as local and clear mutable copies in `defer` blocks.

After `apply` returns `created` or `unchanged`, run the existing public
GetLoginSalt/Login exchange from inside the CLI using the in-memory Admin
credentials. Discard the access token and request-signing key, and report only
`login_api: valid`. This proves the Web-facing authentication path without
exposing the password to the Agent.

- [ ] **Step 4: Make global YAML loading lazy**

Move `loadGlobalConfig()` out of root `init()` and invoke it only from commands
that consume the legacy operational YAML. Setup commands must start with no
unrelated warning or file lookup.

- [ ] **Step 5: Run CLI tests and help smoke test**

```bash
cd modules/cli
go test -count=1 ./internal/command ./internal/setup/config ./internal/setup/validate ./internal/setup/client ./internal/setup/deploy
go run ./cmd/moox-cli setup --help
```

Expected: all tests PASS; help lists five setup subcommands.

- [ ] **Step 6: Commit the command surface**

```bash
git add modules/cli/internal/command modules/cli/README.md
git commit -m "feat(cli): add custom setup workflow"
```

## Task 10: Encode The Workflow In The MooX Skill

**Files:**
- Create: `skills/moox/references/custom-setup.md`
- Modify: `skills/moox/SKILL.md`
- Create: `skills/moox/scripts/test-custom-setup-contract.sh`

- [ ] **Step 1: Write the failing Skill contract test**

The script must verify that the Skill/reference:

- shows the exact `custom.toml.example` template;
- says the user writes the file before deployment;
- requires mode `0600`;
- orders `validate`, `deploy-control`, `apply`, then `status`;
- says the file remains unchanged and contains plaintext credentials;
- prohibits Agent use of `cat`, `sed`, `rg`, Python, or shell `source` on
  `custom.toml`;
- starts later service placement only after setup status succeeds.

- [ ] **Step 2: Run and verify failure**

```bash
bash skills/moox/scripts/test-custom-setup-contract.sh
```

Expected: FAIL because the reference and flow are absent.

- [ ] **Step 3: Write the operator reference**

Document this exact Agent-safe sequence:

```bash
test -e ./custom.toml || exit 2
./bin/moox-cli setup validate --file ./custom.toml
./bin/moox-cli setup deploy-control --file ./custom.toml
./bin/moox-cli setup apply --file ./custom.toml
./bin/moox-cli setup status --file ./custom.toml
```

The `test -e` check may inspect existence only. The Skill must never show a
command that reads the file outside `moox-cli`.

- [ ] **Step 4: Update the main Skill flow**

Replace the current first-stage host interrogation with:

1. show `custom.toml.example` when the manifest is absent;
2. wait for the user to fill and protect `custom.toml`;
3. run the four high-level commands;
4. require the `setup apply` public-login check to pass and ask the user to
   confirm interactive browser login when needed;
5. remind the user the plaintext file remains unchanged;
6. begin incremental service placement from registered hosts.

- [ ] **Step 5: Run and commit**

```bash
bash skills/moox/scripts/test-custom-setup-contract.sh
git add skills/moox/SKILL.md skills/moox/references/custom-setup.md skills/moox/scripts/test-custom-setup-contract.sh
git commit -m "docs(skill): guide custom setup initialization"
```

## Task 11: Run Cross-Module Security And Acceptance Verification

**Files:**
- Create: `modules/admin/test/system_setup_e2e_test.go`
- Create: `modules/cli/test/setup_e2e_test.go`
- Modify: `Makefile`

- [ ] **Step 1: Add the Admin transaction E2E**

Start Admin with a temporary database, `0600` encryption-key file, and
loopback setup listener. Send a manifest through HTTP and verify database
rows, bcrypt, ciphertext, state HMAC, retry, and conflict. Attempt the same path
through browser Admin and node Gateway ports and require route-not-found.

- [ ] **Step 2: Add the CLI workflow E2E**

Use a temporary `0600 custom.toml`, an SSH fixture, fake Tencent identity
server, package-only control archive, and Admin setup server. Capture every
process stream and scan all generated files:

```go
for _, secret := range []string{adminPassword, controlPassword, otherPassword, secretID, secretKey} {
    assert.NotContains(t, combinedOutput, secret)
    assert.NotContains(t, string(releaseArchiveBytes), secret)
}
assert.Equal(t, beforeBytes, afterBytes)
```

- [ ] **Step 3: Add the focused verification target**

Add `verify-custom-setup` to the root Makefile. It runs:

```bash
cd modules/admin && go test -count=1 ./internal/service/setup/... ./internal/bootstrap ./test -run Setup
cd modules/cli && go test -count=1 ./internal/setup/... ./internal/command ./test -run Setup
bash scripts/test-deploy-moox-admin-bootstrap.sh
bash scripts/test-deploy-moox-control-profile.sh
bash skills/moox/scripts/test-custom-setup-contract.sh
```

- [ ] **Step 4: Run focused verification**

```bash
make verify-custom-setup
```

Expected: PASS with no credential values in output or generated artifacts.

- [ ] **Step 5: Run repository verification**

```bash
make verify
```

Expected: PASS. If an unrelated suite fails, record the exact pre-existing
failure and keep `make verify-custom-setup` green before proceeding.

- [ ] **Step 6: Run a real remote acceptance**

With a user-created `0600 custom.toml` that the Agent never reads:

```bash
./bin/moox-cli setup validate --file ./custom.toml
./bin/moox-cli setup deploy-control --file ./custom.toml
./bin/moox-cli setup apply --file ./custom.toml
./bin/moox-cli setup status --file ./custom.toml
```

Verify signed health, public Web access, browser login, encrypted Admin rows,
same-manifest `unchanged`, public setup route rejection, and byte-for-byte
manifest stability.

- [ ] **Step 7: Commit acceptance coverage**

```bash
git add Makefile modules/admin/test/system_setup_e2e_test.go modules/cli/test/setup_e2e_test.go
git commit -m "test: verify custom setup workflow"
```

## Task 12: Final Review And Delivery

**Files:**
- Review all files listed above.

- [ ] **Step 1: Scan for secret-handling regressions**

```bash
rg -n "custom\.toml|password|secret_id|secret_key|InsecureIgnoreHostKey|admin-password" \
  modules/cli modules/admin scripts skills/moox .gitignore custom.toml.example
```

Confirm every occurrence follows the design: no secret argv/env/temp files, no
insecure host-key callback, no deploy-time Admin creation, and no Agent-readable
Skill command.

- [ ] **Step 2: Verify the working tree contains no user manifest**

```bash
git status --short
git ls-files --error-unmatch custom.toml && exit 1 || true
git check-ignore -q custom.toml
```

Expected: `custom.toml` is untracked and ignored; `custom.toml.example` is
tracked.

- [ ] **Step 3: Run final fresh verification**

```bash
make verify-custom-setup
make verify
```

Expected: PASS from fresh, uncached Go test runs.

- [ ] **Step 4: Review commit history and push**

```bash
git log --oneline --decorate -12
git status --short --branch
git push -u origin HEAD
```

Expected: the feature branch is clean and synchronized with its remote branch.
