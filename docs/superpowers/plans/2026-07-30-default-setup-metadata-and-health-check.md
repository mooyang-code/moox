# Default Setup Metadata And Health Check Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a rerunnable `moox-cli setup init --config-dir` workflow that initializes the default A-share and crypto spaces in Admin and Storage, and rename the existing Pipeline monitoring vocabulary to module health checks plus Dataset health policy.

**Architecture:** A fixed `config/setup` bundle contains three schema-owned YAML files. `metadata.yaml` is the single initialization source for Admin business spaces and Storage metadata; Monitor alone consumes `dataset-health-policy.yaml`; the code-owned module health-check registry has no YAML dependency. The CLI performs strict create-or-verify operations and activates ready Datasets without pretending that Admin and Storage share a transaction.

**Tech Stack:** Go 1.24, Cobra, YAML v3, protobuf/tRPC-Go, GORM/SQLite, Prometheus, Vue 3, Playwright, Bash contract tests.

---

## File Structure

- `config/setup/README.md`: explains the three fixed files and the `setup init` command.
- `config/setup/metadata.yaml`: A-share, crypto, and internal Monitor Storage metadata.
- `config/setup/dataset-health-policy.yaml`: Monitor-only realtime Dataset health thresholds.
- `config/setup/service-deployments.yaml`: Admin service deployment seed.
- `packages/report/module_health_checks.go`: code-owned module health-check registry.
- `packages/report/dataset_health_policy.go`: strict Dataset health policy loader and environment validation.
- `packages/report/module_health.go`: shared health verdict truth table.
- `modules/admin/internal/service/setup/service.go`: transactional create-or-verify of Admin spaces.
- `modules/admin/internal/service/setup/rpc/service.go`: Setup Proto/domain mapping.
- `modules/cli/internal/command/setup_init.go`: high-level initialization orchestration.
- `modules/cli/internal/command/metadata_implementation.go`: strict Storage create-or-verify.
- `modules/cli/internal/command/metadata_spaces.go`: extraction of Admin business spaces from metadata.
- `modules/cli/internal/command/setup_storage.go`: Dataset activation over the Storage tunnel.
- `modules/monitor/proto/monitor.proto`: Doctor health-check request and response vocabulary.

### Task 1: Split Dataset Policy From Module Health Checks

**Files:**
- Create: `packages/report/module_health_checks.go`
- Create: `packages/report/dataset_health_policy.go`
- Move: `packages/report/pipeline_eval.go` to `packages/report/module_health.go`
- Delete: `packages/report/pipelines.go`
- Test: `packages/report/module_health_checks_test.go`
- Test: `packages/report/dataset_health_policy_test.go`
- Modify: `packages/report/module_metrics.go`
- Modify: `packages/report/module_metrics_test.go`
- Modify: `packages/report/dataset_module_observer.go`
- Modify: `packages/report/handler.go`

- [ ] **Step 1: Write failing public-contract tests**

Add tests that compile against the intended API:

```go
func TestBuiltInModuleHealthChecksAreUnique(t *testing.T) {
	checks := BuiltInModuleHealthChecks()
	seen := map[string]bool{}
	for _, check := range checks {
		if check.ID == "" || check.Module == "" || seen[check.ID] {
			t.Fatalf("invalid health check: %+v", check)
		}
		seen[check.ID] = true
	}
}

func TestLoadDatasetHealthPolicyRejectsUnknownFields(t *testing.T) {
	path := writePolicy(t, "version: 2\nunknown: true\nrealtime_timeseries: {}\n")
	if _, err := LoadDatasetHealthPolicy(path); err == nil {
		t.Fatal("expected strict YAML error")
	}
}

func TestModuleMetricsUsesHealthCheckLabel(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewModuleMetrics(registry, "collector", []string{"collector-market-data"})
	if err != nil {
		t.Fatal(err)
	}
	if err := metrics.ObserveRun("collect", "success", "collector-market-data", time.Now()); err != nil {
		t.Fatal(err)
	}
	body := gatherText(t, registry)
	if !strings.Contains(body, `health_check="collector-market-data"`) ||
		strings.Contains(body, `pipeline=`) {
		t.Fatalf("unexpected metrics:\n%s", body)
	}
}
```

- [ ] **Step 2: Run the report tests and verify RED**

Run:

```bash
go test ./packages/report -run 'Test(BuiltInModuleHealthChecks|LoadDatasetHealthPolicy|ModuleMetricsUsesHealthCheckLabel)' -count=1
```

Expected: compilation fails because the new health-check and policy APIs do not exist.

- [ ] **Step 3: Implement the split public API**

Define:

```go
type ModuleHealthCheck struct {
	ID                    string
	Module                string
	Enabled               bool
	MaxLag                time.Duration
	CheckFreshness        bool
	CheckWatermark        bool
	ObservabilityDeferred bool
}

func BuiltInModuleHealthChecks() []ModuleHealthCheck
func HealthCheckIDsForModule(module string) []string

type DatasetHealthPolicy struct {
	Version            int                      `yaml:"version"`
	RealtimeTimeSeries RealtimeTimeSeriesPolicy `yaml:"realtime_timeseries"`
	Checksum           string                   `yaml:"-"`
}

func LoadDatasetHealthPolicy(path string) (DatasetHealthPolicy, error)
func ValidateDatasetHealthEnvironment() (DatasetHealthPolicy, error)
```

Use `yaml.Decoder.KnownFields(true)`, reject a second YAML document, preserve the current SHA-256 checksum behavior, and use `MOOX_DATASET_HEALTH_POLICY` plus `MOOX_DATASET_HEALTH_POLICY_HASH`. Rename `PipelineSignals`, `PipelineVerdict`, and `EvaluatePipelineSignals` to `ModuleHealthSignals`, `ModuleHealthVerdict`, and `EvaluateModuleHealth`. Rename every metrics parameter, allowlist field, error, help string, and Prometheus label from `pipeline` to `health_check`.

- [ ] **Step 4: Run report tests and verify GREEN**

Run:

```bash
go test ./packages/report -count=1
go test -race ./packages/report -count=1
```

Expected: both commands pass and `rg -n -i 'pipeline' packages/report` has no matches.

- [ ] **Step 5: Commit the report vocabulary change**

```bash
git add packages/report
git commit -m "refactor: name module health checks explicitly"
```

### Task 2: Rename Monitor Doctor Contracts

**Files:**
- Modify: `modules/monitor/proto/monitor.proto`
- Regenerate: `modules/monitor/proto/monitorgen/*.go`
- Modify: `modules/monitor/internal/doctor/context.go`
- Modify: `modules/monitor/internal/doctor/context_test.go`
- Modify: `modules/monitor/internal/rpc/doctor_context.go`
- Modify: `modules/monitor/internal/rpc/doctor_context_test.go`
- Modify: `modules/monitor/proto/monitorgen/validation.go`
- Modify: `modules/monitor/proto/monitorgen/validation_test.go`
- Modify: `modules/monitor/test/doctor_context_e2e_test.go`
- Modify: `modules/cli/internal/doctor/bootstrap.go`
- Modify: `modules/cli/internal/doctor/bootstrap_test.go`
- Modify: `modules/cli/internal/doctor/diagnose.go`
- Modify: `modules/cli/internal/doctor/diagnose_test.go`
- Modify: `modules/cli/internal/command/doctor.go`
- Modify: `modules/cli/internal/config/config.go`
- Modify: `modules/cli/config/cli.yaml`

- [ ] **Step 1: Change handwritten tests to the new wire vocabulary**

Make Doctor tests construct and assert:

```go
&pb.GetDoctorContextReq{
	Modules:        []string{"collector"},
	HealthCheckIds: []string{"collector-market-data"},
}

&pb.DoctorWatermark{
	Module:        "collector",
	Stage:         "collect",
	HealthCheckId: "collector-market-data",
}
```

Assert check IDs and recovery action exactly:

```go
"module.health_check:collector:collector-market-data"
"inspect_health_check_input"
```

- [ ] **Step 2: Run focused Monitor and CLI tests and verify RED**

Run:

```bash
(cd modules/monitor && go test ./internal/doctor ./internal/rpc ./proto/monitorgen ./test -count=1)
(cd modules/cli && go test ./internal/doctor ./internal/command ./internal/config -count=1)
```

Expected: compilation fails on missing `HealthCheckIds` and `HealthCheckId` fields or old Doctor symbols.

- [ ] **Step 3: Rename the Proto and runtime contracts**

Keep wire field numbers while making a clean source-level break:

```proto
message DoctorWatermark {
  string module = 1;
  string stage = 2;
  string health_check_id = 3;
  int64 last_success_unix = 4;
  int64 watermark_unix = 5;
  int64 input_watermark_unix = 6;
}

message GetDoctorContextReq {
  repeated string modules = 1;
  repeated string dataset_keys = 2;
  repeated string health_check_ids = 3;
}
```

Generate Proto files serially with:

```bash
make proto
```

Then update handwritten Monitor and CLI types, filtering, rendering, check IDs, failure text, and recovery actions to use `health_check`.

- [ ] **Step 4: Verify generated and handwritten contracts**

Run:

```bash
make proto-check
(cd modules/monitor && go test ./... -count=1)
(cd modules/cli && go test ./internal/doctor ./internal/command ./internal/config -count=1)
```

Expected: all commands pass and no Doctor JSON or Proto name contains `pipeline`.

- [ ] **Step 5: Commit the Doctor contract rename**

```bash
git add modules/monitor modules/cli/internal/doctor modules/cli/internal/command/doctor.go modules/cli/internal/config modules/cli/config/cli.yaml
git commit -m "refactor: expose health checks in doctor"
```

### Task 3: Make Dataset Health Policy Monitor-Owned

**Files:**
- Modify: `modules/monitor/internal/config/config.go`
- Modify: `modules/monitor/internal/config/config_test.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/monitor/internal/bootstrap/metrics_runtime.go`
- Modify: `modules/monitor/internal/bootstrap/service_runtime.go`
- Modify: `modules/monitor/internal/doctor/context.go`
- Modify: `modules/monitor/internal/rpc/doctor_context.go`
- Modify: module metric bootstraps in `modules/{archive,cloudnode,collector,factor,strategy,trade}/internal`
- Modify: `scripts/deploy/deploy-moox.sh`
- Modify: `scripts/release/release.sh`
- Modify: `scripts/release/release-matrix.sh`
- Modify: related contract tests in `scripts/test-*.sh`

- [ ] **Step 1: Write failing environment ownership tests**

Add a Monitor test proving its health identity uses the new field and environment:

```go
func TestServiceRuntimeReportsDatasetHealthPolicyHash(t *testing.T) {
	t.Setenv("MOOX_DATASET_HEALTH_POLICY", "/tmp/policy.yaml")
	t.Setenv("MOOX_DATASET_HEALTH_POLICY_HASH", "abc123")
	got := runtimeIdentityFromEnvironment()
	if got.DatasetHealthPolicyHash != "abc123" {
		t.Fatalf("hash = %q", got.DatasetHealthPolicyHash)
	}
}
```

Add package bootstrap tests proving non-Monitor modules obtain IDs directly:

```go
ids := report.HealthCheckIDsForModule("collector")
if diff := cmp.Diff([]string{"collector-market-data"}, ids); diff != "" {
	t.Fatal(diff)
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```bash
(cd modules/monitor && go test ./internal/bootstrap ./internal/config -count=1)
(cd modules/collector && go test ./internal/bootstrap -count=1)
```

Expected: new runtime identity field or direct registry APIs are not wired.

- [ ] **Step 3: Remove policy loading from non-Monitor modules**

Replace calls shaped like:

```go
pipelineConfig, err := report.ValidatePipelineEnvironment()
ids := pipelineConfig.PipelineIDsForModule(module)
```

with:

```go
ids := report.HealthCheckIDsForModule(module)
```

Monitor loads `DatasetHealthPolicy` once and passes `RealtimeTimeSeries` into its existing freshness logic. Rename the shared health endpoint field to `dataset_health_policy_hash`; only Monitor is required to expose and match the policy hash.

Update deploy and release scripts to copy:

```text
config/setup/dataset-health-policy.yaml
```

to:

```text
config/dataset-health-policy.yaml
```

and inject only:

```text
MOOX_DATASET_HEALTH_POLICY
MOOX_DATASET_HEALTH_POLICY_HASH
```

into Monitor.

- [ ] **Step 4: Run module and script contracts**

Run:

```bash
(cd modules/monitor && go test ./... -count=1)
(cd modules/collector && go test ./internal/bootstrap -count=1)
(cd modules/cloudnode && go test ./internal/bootstrap -count=1)
bash scripts/test/contract/test-release-contract.sh
bash scripts/test/contract/test-deploy-moox-monitor.sh
```

Expected: all commands pass and `rg -n 'MOOX_PIPELINE_CONFIG|pipeline_config_hash' modules scripts` returns no matches.

- [ ] **Step 5: Commit Monitor policy ownership**

```bash
git add modules packages scripts
git commit -m "refactor: make dataset health policy monitor owned"
```

### Task 4: Consolidate Default Setup Files

**Files:**
- Create: `config/setup/README.md`
- Create: `config/setup/metadata.yaml`
- Create: `config/setup/dataset-health-policy.yaml`
- Move: `examples/service-deployments.seed.yaml` to `config/setup/service-deployments.yaml`
- Delete: `examples/metadata-quant-initial.seed.yaml`
- Delete: `examples/metadata-monitor-host.seed.yaml`
- Delete: `examples/metadata-monitor-metrics.seed.yaml`
- Delete: `examples/monitor-pipelines.yaml`
- Delete: `examples/platform-local.seed.yaml`
- Modify: `modules/cli/internal/command/metadata_quant_seed_test.go`
- Create: `modules/cli/internal/command/default_setup_bundle_test.go`

- [ ] **Step 1: Write a failing bundle contract test**

The test must decode `config/setup/metadata.yaml` strictly and assert:

```go
businessSpaces := []string{"crypto", "stockcn"}
internalSpaces := []string{"mooxsys"}
stockDatasets := []string{
	"dataset_stockcn_financial_statement_metric",
	"dataset_stockcn_financial_summary",
	"index_kline",
	"stock_kline",
}
cryptoDatasets := []string{"dataset_perpetual_kline_1h", "dataset_spot_kline_1h"}
```

It must also assert that crypto Dataset/View frequencies equal `1H`, all references resolve, every Dataset has columns and a View, every View has ViewColumns, and the old root seed filenames do not exist.

- [ ] **Step 2: Run the bundle contract and verify RED**

Run:

```bash
(cd modules/cli && go test ./internal/command -run TestDefaultSetupBundle -count=1)
```

Expected: failure because `config/setup/metadata.yaml` does not exist.

- [ ] **Step 3: Create the fixed three-file bundle**

Merge current Metadata resources into one YAML, remove `stockhk` and `stockus`, mark:

```yaml
- id: mooxsys
  name: MooX 系统
  attributes:
    scope: internal
```

Add Admin-facing market facts:

```yaml
- id: stockcn
  name: A股市场
  market: CN
  timezone: Asia/Shanghai

- id: crypto
  name: 加密货币市场
  market: crypto
  timezone: UTC
```

Use `1H` for crypto frequency in Dataset/View/sample values. Create the Monitor policy:

```yaml
version: 2
realtime_timeseries:
  defaults:
    run_missed_intervals: 2
    success_missed_intervals: 3
    watermark_periods: 3
    minimum_watermark_lag: 10m
  overrides:
    - space_id: crypto
      dataset_id: dataset_spot_kline_1h
      freq: 1H
      watermark_lag: 5m
```

Document the fixed consumers in the bundle README.

- [ ] **Step 4: Verify the bundle and all repository references**

Run:

```bash
(cd modules/cli && go test ./internal/command -run 'Test(DefaultSetupBundle|InitialQuantMetadataSeed)' -count=1)
rg -n 'metadata-quant-initial|metadata-monitor-host|metadata-monitor-metrics|monitor-pipelines|service-deployments.seed|platform-local.seed' . \
  --glob '!docs/superpowers/specs/**' --glob '!docs/superpowers/plans/**'
```

Expected: tests pass and the reference search is empty after references are updated.

- [ ] **Step 5: Commit the bundle consolidation**

```bash
git add examples modules/cli/internal/command/metadata_quant_seed_test.go modules/cli/internal/command/default_setup_bundle_test.go
git commit -m "feat: consolidate default setup bundle"
```

### Task 5: Add Admin Space Create-Or-Verify

**Files:**
- Modify: `modules/admin/proto/setup_service.proto`
- Regenerate: `modules/admin/proto/admingen/*.go`
- Modify: `modules/admin/internal/service/setup/service.go`
- Modify: `modules/admin/internal/service/setup/service_test.go`
- Modify: `modules/admin/internal/service/setup/rpc/service.go`
- Modify: `modules/admin/internal/service/setup/rpc/service_test.go`
- Modify: `modules/cli/internal/setup/client/client.go`
- Modify: `modules/cli/internal/setup/client/client_test.go`

- [ ] **Step 1: Write failing Admin domain and RPC tests**

Add these concrete cases to `service_test.go`:

```go
func setupSpaceManifest() Manifest {
	manifest := testManifest()
	manifest.Spaces = []Space{{
		SpaceID: "stockcn", Name: "A股市场", Market: "CN",
		Timezone: "Asia/Shanghai", Status: "active", AttributesJSON: "{}",
	}}
	return manifest
}

func TestApplyCreatesAndInspectsSpaces(t *testing.T) {
	db := setupDB(t)
	service := NewService(db, testEncryptionKey)
	result, err := service.Apply(context.Background(), setupSpaceManifest())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Spaces)
	status, err := service.Inspect(context.Background(), setupSpaceManifest())
	require.NoError(t, err)
	assert.Equal(t, 1, status.Spaces)
	assert.Equal(t, "completed", status.State)
}

func TestApplyIdenticalSpaceIsUnchanged(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)
	_, err := service.Apply(context.Background(), setupSpaceManifest())
	require.NoError(t, err)
	result, err := service.Apply(context.Background(), setupSpaceManifest())
	require.NoError(t, err)
	assert.Equal(t, "unchanged", result.Action)
}

func TestApplyRejectsConflictingSpace(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)
	manifest := setupSpaceManifest()
	_, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)
	manifest.Spaces[0].Name = "不同名称"
	_, err = service.Apply(context.Background(), manifest)
	require.ErrorIs(t, err, ErrConflict)
}

func TestApplyRollsBackSpaceOnLaterFailure(t *testing.T) {
	db := setupDB(t)
	service := NewService(db, testEncryptionKey)
	service.writeHook = func(stage string) error {
		if stage == "host:control" {
			return errors.New("injected host failure")
		}
		return nil
	}
	_, err := service.Apply(context.Background(), setupSpaceManifest())
	require.ErrorIs(t, err, ErrStorage)
	var spaces int64
	require.NoError(t, db.Model(&adminspace.Space{}).Count(&spaces).Error)
	assert.Zero(t, spaces)
}
```

RPC tests must map `market`, `timezone`, `status`, and canonical `attributes_json`, and return `spaces_created` plus `spaces_unchanged`.

- [ ] **Step 2: Run Admin setup tests and verify RED**

Run:

```bash
(cd modules/admin && go test ./internal/service/setup ./internal/service/setup/rpc -count=1)
```

Expected: compilation fails because `Manifest.Spaces` and Setup Proto space fields are absent.

- [ ] **Step 3: Add the Setup Space contract**

Add:

```proto
message SetupSpace {
  string space_id = 1;
  string name = 2;
  string description = 3;
  string owner = 4;
  string market = 5;
  string timezone = 6;
  string status = 7;
  string attributes_json = 8;
}
```

Add `repeated SetupSpace spaces = 5` to Apply and Status requests. Add created/unchanged/total space counts at field 6 in their responses. In the domain service, normalize attributes to canonical JSON and create or compare every declared field in the same transaction as the existing setup records. Return `setup_conflict` without updating an existing space.

Expose client methods:

```go
func (c *Client) ApplyWithSpaces(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaces []Space,
) (ApplyResult, error)

func (c *Client) StatusWithSpaces(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaces []Space,
) (StatusResult, error)
```

Keep `Apply` and `Status` as thin empty-space wrappers.

- [ ] **Step 4: Generate and verify Admin/CLI contracts**

Run serially:

```bash
make proto
make proto-check
(cd modules/admin && go test ./internal/service/setup ./internal/service/setup/rpc -count=1)
(cd modules/cli && go test ./internal/setup/client -count=1)
```

Expected: all commands pass.

- [ ] **Step 5: Commit Admin space initialization**

```bash
git add modules/admin modules/cli/internal/setup/client
git commit -m "feat: initialize admin spaces from setup"
```

### Task 6: Make Metadata Loading And Apply Strict

**Files:**
- Modify: `modules/cli/internal/command/metadata_types.go`
- Modify: `modules/cli/internal/command/metadata_implementation.go`
- Modify: `modules/cli/internal/command/metadata_spaces.go`
- Modify: `modules/cli/internal/command/metadata_test.go`
- Modify: `modules/cli/internal/command/metadata_quant_seed_test.go`

- [ ] **Step 1: Write failing strict-load and idempotency tests**

Add tests for:

```go
func TestLoadMetadataSeedRejectsUnknownField(t *testing.T)
func TestLoadMetadataSeedRejectsSecondDocument(t *testing.T)
func TestBusinessSpacesExcludeInternalScope(t *testing.T)
func TestRunMetadataApplySecondPassIsUnchanged(t *testing.T)
func TestRunMetadataApplyRejectsViewColumnConflict(t *testing.T)
```

The second-pass fake Storage transport must return existing resources from List/Get RPCs and assert no Create RPC occurs.

- [ ] **Step 2: Run CLI Metadata tests and verify RED**

Run:

```bash
(cd modules/cli && go test ./internal/command -run 'Test(LoadMetadataSeed|BusinessSpaces|RunMetadataApply)' -count=1)
```

Expected: unknown YAML is accepted, internal space extraction is absent, or a second apply attempts a duplicate ViewColumn create.

- [ ] **Step 3: Implement strict parsing and complete equality**

Decode a bounded file through:

```go
decoder := yaml.NewDecoder(io.LimitReader(file, maxMetadataSeedBytes))
decoder.KnownFields(true)
if err := decoder.Decode(&seed); err != nil {
	return metadataSeed{}, fmt.Errorf("decode metadata seed: %w", err)
}
if err := decoder.Decode(&struct{}{}); err != io.EOF {
	return metadataSeed{}, fmt.Errorf("metadata seed must contain exactly one YAML document")
}
```

Add `Market` and `Timezone` to `metadataSpace`, preserve attributes as structured data, and derive sorted Admin spaces by excluding `attributes.scope == "internal"`. Extend strict create-or-verify to DataSource, FieldGroup, Field, Dataset, DatasetColumn, View, and ViewColumn using their List/Get RPCs. Compare all declarative fields while ignoring server-owned revision and timestamps.

- [ ] **Step 4: Verify Metadata behavior and race safety**

Run:

```bash
(cd modules/cli && go test ./internal/command -run 'Test(LoadMetadataSeed|BusinessSpaces|RunMetadataApply|DefaultSetupBundle)' -count=1)
(cd modules/cli && go test -race ./internal/command -run 'Test(LoadMetadataSeed|BusinessSpaces|RunMetadataApply)' -count=1)
```

Expected: all tests pass; a second identical apply reports every resource unchanged.

- [ ] **Step 5: Commit strict Metadata apply**

```bash
git add modules/cli/internal/command/metadata_*.go modules/cli/internal/command/default_setup_bundle_test.go
git commit -m "feat: apply setup metadata idempotently"
```

### Task 7: Implement `setup init` And Dataset Activation

**Files:**
- Create: `modules/cli/internal/command/setup_init.go`
- Create: `modules/cli/internal/command/setup_init_test.go`
- Modify: `modules/cli/internal/command/setup.go`
- Modify: `modules/cli/internal/command/setup_storage.go`
- Modify: `modules/cli/internal/command/setup_storage_test.go`

- [ ] **Step 1: Write failing command and orchestration tests**

Test:

```go
func TestSetupInitRequiresStorageHost(t *testing.T)
func TestSetupInitRunsStagesInOrder(t *testing.T)
func TestSetupInitStopsAfterAdminFailure(t *testing.T)
func TestSetupInitActivatesReadyDatasets(t *testing.T)
func TestSetupInitLeavesActiveDatasetsUnchanged(t *testing.T)
func TestSetupInitJSONDoesNotExposeSecrets(t *testing.T)
```

The ordered test must assert:

```go
want := []string{
	"load-bundle",
	"load-config",
	"admin-apply",
	"admin-status",
	"login",
	"storage-apply",
	"dataset-activate",
	"verify",
}
```

- [ ] **Step 2: Run setup tests and verify RED**

Run:

```bash
(cd modules/cli && go test ./internal/command -run 'TestSetupInit' -count=1)
```

Expected: `setup init` and its orchestration dependency are absent.

- [ ] **Step 3: Implement the command**

Register:

```go
setupCmd.AddCommand(newSetupInitCmd(deps))
```

with:

```text
--file         ./moox.toml
--config-dir   ./config/setup
--storage-host required
```

Load only `filepath.Join(configDir, "metadata.yaml")`. Apply Admin spaces using `ApplyWithSpaces`; inspect using `StatusWithSpaces`; verify login using the existing setup client; open the existing Storage SSH tunnel; call strict Metadata apply; then activate each declared Dataset.

For activation:

```go
check, err := session.metadata.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{
	AuthInfo:  session.auth,
	SpaceId:   spaceID,
	DatasetId: datasetID,
})
if err != nil {
	return fmt.Errorf("check dataset activation %s/%s: %w", spaceID, datasetID, err)
}
if !check.Ready {
	return fmt.Errorf("dataset_activation_not_ready: %s/%s: %s", spaceID, datasetID, summarizeChecks(check.Checks))
}
_, err = session.metadata.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{
	AuthInfo:         session.auth,
	SpaceId:          spaceID,
	DatasetId:        datasetID,
	ExpectedRevision: check.DatasetRevision,
})
```

Treat an already active Dataset as unchanged. Return a typed JSON summary with Admin, Metadata, activation, and business-space counts only.

- [ ] **Step 4: Verify command behavior**

Run:

```bash
(cd modules/cli && go test ./internal/command ./internal/setup/client ./internal/setup/config -count=1)
(cd modules/cli && go test -race ./internal/command -run 'TestSetupInit' -count=1)
```

Expected: all tests pass and the test output contains no password, token, private key, or SSH command.

- [ ] **Step 5: Commit the initialization command**

```bash
git add modules/cli/internal/command
git commit -m "feat: initialize default spaces with setup init"
```

### Task 8: Update Documentation And Release Contracts

**Files:**
- Modify: `README.md`
- Modify: `docs/setup.md`
- Modify: `modules/monitor/README.md`
- Modify: `scripts/test/contract/test-release-contract.sh`
- Modify: `scripts/test/contract/test-secret-scan-contract.sh`
- Modify: `scripts/check/verify-event-contracts.sh`
- Modify: deployment scripts that refer to old example paths or Pipeline terms

- [ ] **Step 1: Change contract assertions first**

Make shell contracts require:

```bash
test -f "${release}/config/setup/metadata.yaml"
test -f "${release}/config/setup/dataset-health-policy.yaml"
test -f "${release}/config/setup/service-deployments.yaml"
test ! -e "${release}/examples/monitor-pipelines.yaml"
```

Add a source contract:

```bash
if rg -n -i 'pipeline' packages/report modules/monitor modules/cli/internal/doctor; then
  echo "legacy Pipeline vocabulary remains" >&2
  exit 1
fi
```

- [ ] **Step 2: Run contracts and verify RED**

Run:

```bash
bash scripts/test/contract/test-release-contract.sh
bash scripts/test/contract/test-secret-scan-contract.sh
```

Expected: old packaging paths or health vocabulary make at least one contract fail.

- [ ] **Step 3: Update docs, deploy paths, and remaining names**

Document:

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host control
```

Rename remaining variables, comments, logs, config keys, metric assertions, and scripts in the health subsystem from Pipeline to health check. Preserve stable check IDs such as `collector-market-data`.

- [ ] **Step 4: Run every affected contract**

Run:

```bash
bash scripts/test/contract/test-release-contract.sh
bash scripts/test/contract/test-secret-scan-contract.sh
bash scripts/test/contract/test-monitor-coverage-contract.sh
bash scripts/check/verify-event-contracts.sh
rg -n -i 'pipeline' packages/report modules/monitor modules/cli/internal/doctor scripts \
  --glob '!*.sum'
```

Expected: contracts pass and the source search has no legacy health-subsystem match.

- [ ] **Step 5: Commit documentation and contracts**

```bash
git add README.md docs modules/monitor/README.md scripts
git commit -m "docs: document default setup initialization"
```

### Task 9: Local Integration And Independent Review

**Files:**
- Review all files changed since `c3e22754`

- [ ] **Step 1: Format and inspect the complete diff**

Run:

```bash
gofmt -w $(git diff --name-only --diff-filter=ACMR c3e22754 -- '*.go')
git diff --check
git status --short
```

Expected: no formatting errors; unrelated pre-existing dirty files remain unstaged.

- [ ] **Step 2: Run serialized Proto and workspace verification**

Run in order, never concurrently:

```bash
make proto-check
./scripts/test/contract/test-go-workspace.sh
make verify-pr
```

Expected: all commands exit 0.

- [ ] **Step 3: Run focused race tests**

Run:

```bash
(cd modules/admin && go test -race ./internal/service/setup ./internal/service/setup/rpc -count=1)
(cd modules/cli && go test -race ./internal/command ./internal/setup/client -count=1)
(cd modules/monitor && go test -race ./internal/doctor ./internal/rpc ./internal/bootstrap -count=1)
go test -race ./packages/report -count=1
```

Expected: all commands exit 0 with no race report.

- [ ] **Step 4: Dispatch `codeCR` independent review**

Ask the reviewer to compare `c3e22754..HEAD` plus the current uncommitted task diff against the design, prioritizing correctness, transaction rollback, strict equality, Dataset activation CAS, secret leakage, Proto breakage, config ownership, and missing tests. Require each finding to include a file and line or symbol.

- [ ] **Step 5: Independently verify and fix every accepted finding**

For each finding, inspect the cited call chain, add a failing regression test, run it RED, implement the fix, and rerun GREEN. Record rejected findings with source evidence.

- [ ] **Step 6: Commit review fixes**

```bash
git diff --name-only --diff-filter=ACMR HEAD -- \
  packages/report modules/admin/internal/service/setup modules/admin/proto \
  modules/cli/internal/command modules/cli/internal/setup/client \
  modules/monitor/internal/doctor modules/monitor/internal/rpc modules/monitor/proto \
  examples/setup scripts > /tmp/moox-review-fix-files
xargs git add -- < /tmp/moox-review-fix-files
git commit -m "fix: harden default setup initialization"
```

### Task 10: Deploy And Prove The Real Multi-Space Workflow

**Files:**
- Modify or create a remote Playwright test under `web/test/` only if the current harness lacks a reusable authenticated space-switch flow.

- [ ] **Step 1: Build release artifacts**

Run:

```bash
make release
```

Expected: release succeeds and includes `config/setup`.

- [ ] **Step 2: Deploy the changed Admin, Monitor, web, and configuration**

Use the repository deployment commands and the existing read-only `moox.toml`; do not print credentials. Confirm the Admin Setup Proto version and Monitor policy hash through their health endpoints before initialization.

- [ ] **Step 3: Run real initialization twice**

Run:

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host control
```

Expected first run: spaces and metadata are created or existing matching resources are unchanged; ready Datasets become active.

Run the same command again.

Expected second run: Admin spaces, Storage metadata, and active Datasets are reported unchanged with no duplicate error.

- [ ] **Step 4: Run authenticated Playwright acceptance**

Against `https://106.53.107.122:9527/#/home`, verify:

```text
A股市场 -> stockcn -> stock_kline/index_kline/financial datasets and A-share fields
加密货币市场 -> crypto -> dataset_spot_kline_1h/dataset_perpetual_kline_1h and crypto fields
```

Capture network requests and assert `X-Space-Id` equals the selected space for home statistics, Dataset list, and Field list. Save screenshots of both selected spaces without exposing credentials.

- [ ] **Step 5: Run final fresh verification**

Run:

```bash
git diff --check
make proto-check
./scripts/test/contract/test-go-workspace.sh
make verify-pr
git status --short
```

Expected: all verification commands pass; status contains only known unrelated user changes plus no uncommitted task files.

- [ ] **Step 6: Push and verify refs**

```bash
git push origin feature/mooyang
git rev-parse HEAD
git ls-remote --heads origin feature/mooyang
```

Expected: local `HEAD` and remote `refs/heads/feature/mooyang` hashes are identical.
