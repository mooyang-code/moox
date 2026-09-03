# Custom Setup EventBus Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `moox.toml` the single user-authored source for the public TLS EventBus endpoint used by control deployment and Collector SCF.

**Architecture:** Extend the strict setup manifest with a typed `EventBus` section, pass it through setup deployment options, and translate it into child-process environment variables for `deploy-moox.sh`. The deployment script renders one authoritative TLS URL and port into the broker, service directory, generated credentials, and remote package without copying `moox.toml`.

**Tech Stack:** Go 1.25, BurntSushi TOML, Cobra, Bash, embedded NATS JetStream, Testify, repository deployment contract scripts.

---

### Task 1: Parse and validate the EventBus setup table

**Files:**
- Modify: `modules/cli/internal/setup/config/config.go`
- Modify: `modules/cli/internal/setup/config/config_test.go`
- Modify: `moox.toml.example`

- [ ] **Step 1: Add failing manifest tests**

Add an `[eventbus]` block to `validManifest` and tests that assert:

```go
assert.Equal(t, "eventbus.example.test", snapshot.Manifest.EventBus.PublicAddress)
assert.Equal(t, 4222, snapshot.Manifest.EventBus.Port)
assert.True(t, snapshot.Manifest.EventBus.TLSEnabled)
```

Add table cases for missing address, scheme/path/embedded port, IPv6, invalid port, and `tls_enabled = false`. Add a port-default test that omits `port` and expects `4222`.

- [ ] **Step 2: Run the focused test and verify failure**

Run:

```bash
go test ./modules/cli/internal/setup/config -run 'TestLoadValidManifest|TestLoadDefaultsEventBusPort|TestLoadRejectsInvalidManifest' -count=1
```

Expected: compilation fails because `Manifest.EventBus` does not exist.

- [ ] **Step 3: Add the typed configuration and validation**

Add:

```go
type EventBus struct {
	PublicAddress string `toml:"public_address"`
	Port          int    `toml:"port"`
	TLSEnabled    bool   `toml:"tls_enabled"`
}

type Manifest struct {
	Admin        Admin        `toml:"admin"`
	TencentCloud TencentCloud `toml:"tencent_cloud"`
	EventBus     EventBus     `toml:"eventbus"`
	ControlHost  Host         `toml:"control_host"`
	CompileHost  Host         `toml:"compile_host"`
	OtherHosts   []Host       `toml:"other_hosts"`
}
```

Default `EventBus.Port` to `4222`. Validate a trimmed IPv4 or DNS hostname with no `://`, `/`, whitespace, colon, or brackets; require a valid TCP port and `TLSEnabled == true`. Return field-scoped errors without echoing the value.

- [ ] **Step 4: Update the public template**

Insert:

```toml
[eventbus]
public_address = ""
port = 4222
tls_enabled = true
```

into `moox.toml.example`.

- [ ] **Step 5: Run the focused tests**

Run:

```bash
go test ./modules/cli/internal/setup/config -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add moox.toml.example modules/cli/internal/setup/config
git commit -m "feat(setup): validate EventBus public endpoint"
```

### Task 2: Pass the manifest endpoint into control packaging

**Files:**
- Modify: `modules/cli/internal/setup/deploy/deploy.go`
- Modify: `modules/cli/internal/setup/deploy/deploy_test.go`
- Modify: `modules/cli/internal/command/setup.go`
- Modify: `modules/cli/internal/command/setup_test.go`

- [ ] **Step 1: Add failing option-propagation tests**

Extend deploy test options with:

```go
EventBusPublicAddress: "eventbus.example.test",
EventBusPort:          4222,
EventBusTLSEnabled:    true,
```

Add a command-packager environment helper test expecting:

```text
MOOX_EVENTBUS_ENABLE_TLS=1
MOOX_EVENTBUS_PUBLIC_IP=eventbus.example.test
MOOX_EVENTBUS_PORT=4222
```

Add a setup command test that captures `deploy.Options` and proves values come from `snapshot.Manifest.EventBus`, not `control_host.address`.

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
go test ./modules/cli/internal/setup/deploy ./modules/cli/internal/command -run 'EventBus|DeployControl' -count=1
```

Expected: compilation fails because deployment options lack EventBus fields.

- [ ] **Step 3: Extend deployment options**

Add:

```go
EventBusPublicAddress string
EventBusPort          int
EventBusTLSEnabled    bool
```

to `deploy.Options`. Add a focused helper that appends the three EventBus variables to the packager command environment while preserving `os.Environ()`.

- [ ] **Step 4: Wire setup command to deployment**

Build `deploy.Options` in `defaultSetupDeploy` from:

```go
EventBusPublicAddress: snapshot.Manifest.EventBus.PublicAddress,
EventBusPort:          snapshot.Manifest.EventBus.Port,
EventBusTLSEnabled:    snapshot.Manifest.EventBus.TLSEnabled,
```

Keep `PublicHost` sourced from `control_host.address` because it serves browser HTTPS and SSH deployment, not EventBus advertisement.

- [ ] **Step 5: Run focused tests**

Run:

```bash
go test ./modules/cli/internal/setup/deploy ./modules/cli/internal/command -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/cli/internal/setup/deploy modules/cli/internal/command/setup.go modules/cli/internal/command/setup_test.go
git commit -m "feat(setup): pass EventBus endpoint to control deploy"
```

### Task 3: Make the deployment script honor the configured EventBus port

**Files:**
- Modify: `scripts/deploy/deploy-moox.sh`
- Modify: `scripts/test/contract/test-deploy-moox-eventbus.sh`

- [ ] **Step 1: Add failing script contract assertions**

Assert that the script:

```text
defaults MOOX_EVENTBUS_PORT to 4222
renders EVENTBUS_URL_ENV with MOOX_EVENTBUS_PORT
patches broker.port in eventbus/config/app.yaml
passes MOOX_EVENTBUS_PORT through the remote bash environment
rejects non-numeric and out-of-range ports
```

- [ ] **Step 2: Run the contract test and verify failure**

Run:

```bash
./scripts/test/contract/test-deploy-moox-eventbus.sh
```

Expected: FAIL on the first missing port assertion.

- [ ] **Step 3: Implement one authoritative port**

At script initialization:

```bash
MOOX_EVENTBUS_PORT="${MOOX_EVENTBUS_PORT:-4222}"
[[ "${MOOX_EVENTBUS_PORT}" =~ ^[0-9]+$ ]] &&
  (( MOOX_EVENTBUS_PORT >= 1 && MOOX_EVENTBUS_PORT <= 65535 )) ||
  { echo "MOOX_EVENTBUS_PORT must be between 1 and 65535" >&2; exit 2; }
```

Use the port in both local and public EventBus URLs. Patch the staged broker
configuration:

```bash
EVENTBUS_PORT="${MOOX_EVENTBUS_PORT}" perl -0pi -e \
  's#(^  port:)\\s*\\d+#$1 $ENV{EVENTBUS_PORT}#m' \
  "${STAGE_DIR}/eventbus/config/app.yaml"
```

Add the port to generated start-script placeholders and to the quoted remote
environment in `sync_remote_stage`.

- [ ] **Step 4: Run syntax and contract tests**

Run:

```bash
bash -n scripts/deploy/deploy-moox.sh
./scripts/test/contract/test-deploy-moox-eventbus.sh
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add scripts/deploy/deploy-moox.sh scripts/test/contract/test-deploy-moox-eventbus.sh
git commit -m "feat(eventbus): configure advertised and listen port"
```

### Task 4: Align setup documentation and template contracts

**Files:**
- Modify: `modules/cli/README.md`
- Modify: `README.md`
- Modify: `docs/运维/MooX-EventBus运维.md`
- Modify: `skills/moox/REFERENCE.md`
- Modify: `skills/moox/scripts/test/contract/test-custom-setup-contract.sh`

- [ ] **Step 1: Update the documented manifest**

Document that `[eventbus]` contains public deployment facts only. State that
MooX generates role tokens, the private CA, and `cloudnode-worker.yaml`.

- [ ] **Step 2: Update the setup contract test**

Extend the reference/template comparison so the EventBus block must match
`moox.toml.example`. Retain the checks that forbid Agents from reading or
printing `moox.toml`.

- [ ] **Step 3: Run documentation contracts**

Run:

```bash
./skills/moox/scripts/test/contract/test-custom-setup-contract.sh
./scripts/test/contract/test-deploy-moox-eventbus.sh
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add README.md modules/cli/README.md docs/运维/MooX-EventBus运维.md skills/moox
git commit -m "docs(setup): document EventBus manifest fields"
```

### Task 5: Complete automated verification

**Files:**
- Verify only

- [ ] **Step 1: Run focused race tests**

```bash
go test -race ./modules/cli/internal/setup/config ./modules/cli/internal/setup/deploy ./modules/cli/internal/command
```

Expected: PASS.

- [ ] **Step 2: Run repository contracts**

```bash
./scripts/test/contract/test-deploy-moox-eventbus.sh
./scripts/check/verify-event-contracts.sh
./scripts/test/contract/test-go-workspace.sh
make verify-pr
git diff --check
```

Expected: PASS with no generated diff.

### Task 6: Independent review, real deployment, and real E2E

**Files:**
- Review the full diff from `65af3add` through candidate HEAD.
- Read `moox.toml` only through `moox-cli setup` commands.

- [ ] **Step 1: Start a fresh independent review Agent**

Ask the Agent to inspect configuration validation, secret boundaries, port
propagation, script quoting, deployment rollback, and test coverage. The Agent
must not read `moox.toml` or modify files.

- [ ] **Step 2: Fix every actionable review finding**

Apply corrections, rerun affected race and contract tests, and commit the
fixes before deployment.

- [ ] **Step 3: Build the setup CLI**

```bash
go build -o ./bin/moox-cli ./modules/cli/cmd/moox-cli
```

Expected: `./bin/moox-cli` exits successfully for `setup --help`.

- [ ] **Step 4: Validate the real manifest without exposing it**

```bash
./bin/moox-cli setup validate --file ./moox.toml
```

Expected: sanitized JSON with successful configuration, Tencent identity, and
SSH validation. Do not print or parse the manifest outside setup CLI.

- [ ] **Step 5: Deploy the control profile**

```bash
./bin/moox-cli setup deploy-control --file ./moox.toml
```

Expected: sanitized JSON with `status: ready`; the target runs Admin, Gateway,
Web, EventBus, CloudNode, and Collector from the candidate revision.

- [ ] **Step 6: Apply setup state**

```bash
./bin/moox-cli setup apply --file ./moox.toml
./bin/moox-cli setup status --file ./moox.toml
```

Expected: setup state `completed`, with no credentials in output.

- [ ] **Step 7: Run real EventBus and CloudNode E2E**

Use setup/status and remote service commands over the existing SSH setup
transport to prove:

```text
service directory EventBus URL == tls://<custom eventbus address>:<port>
broker listens on 0.0.0.0:<port> with TLS
cloudnode-worker.yaml exists with mode 0600
worker authenticates and can bind/fetch/ack
worker cannot create consumers or publish business events
CloudNode submits a real JobItem and the Collector SCF path reports a terminal state
```

All output must be sanitized. Do not copy tokens, private keys, or
`moox.toml` into artifacts or chat.

- [ ] **Step 8: Verify candidate revision and clean state**

```bash
git status --short
git rev-parse HEAD
git push origin feature/mooyang
git ls-remote origin refs/heads/feature/mooyang
```

Expected: clean worktree and identical local/remote SHA.
