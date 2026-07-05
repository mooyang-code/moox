# Collector Jobs Registry Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move collector business-specific data type definitions and task planning dispatch out of `internal/rpc` and into `internal/jobs`.

**Architecture:** `rpc.Service` remains the tRPC protocol entrypoint and only converts requests/responses. `internal/jobs` owns data type definitions, form field definitions, supported exchanges/markets, job type constants, and planner dispatch. `internal/planner` delegates atomic task spec building to `jobs.BuildTaskSpecs`.

**Tech Stack:** Go 1.24, tRPC-Go, protobuf generated types, GORM-backed collector repositories.

---

## File Structure

- Create `modules/collector/internal/jobs/jobdef/definition.go`: common data type/field definition structs shared by `jobs` and job subpackages.
- Create `modules/collector/internal/jobs/registry_test.go`: tests for registry ordering, field metadata, and planner dispatch.
- Modify `modules/collector/internal/jobs/registry.go`: replace constants-only file with definition registry while preserving job type constants.
- Create `modules/collector/internal/jobs/kline/definition.go`: K-line config fields and planner adapter.
- Create `modules/collector/internal/jobs/symbol/definition.go`: symbol config fields and planner adapter.
- Modify `modules/collector/internal/planner/kline.go`: delegate to `jobs.BuildTaskSpecs`.
- Modify `modules/collector/internal/rpc/service.go`: remove K-line/symbol config builders and convert `jobs.Definition` to protobuf response types.

## Tasks

### Task 1: Add jobs registry behavior tests

**Files:**
- Create: `modules/collector/internal/jobs/registry_test.go`

- [x] **Step 1: Write the failing tests**

```go
package jobs

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
)

func TestListDefinitionsReturnsStableDataTypes(t *testing.T) {
	defs := ListDefinitions()
	if len(defs) != 2 {
		t.Fatalf("len(defs) = %d, want 2", len(defs))
	}
	if defs[0].DataType != "kline" || defs[1].DataType != "symbol" {
		t.Fatalf("data types = %q, %q; want kline, symbol", defs[0].DataType, defs[1].DataType)
	}
	if defs[0].DataSourceOptions.Options[0].Value != "binance" {
		t.Fatalf("first datasource = %q, want binance", defs[0].DataSourceOptions.Options[0].Value)
	}
}

func TestDefinitionWithFieldsReturnsKlineFields(t *testing.T) {
	def, ok := DefinitionByDataType("kline")
	if !ok {
		t.Fatalf("kline definition not found")
	}
	if len(def.Fields) != 3 {
		t.Fatalf("len(kline fields) = %d, want 3", len(def.Fields))
	}
	if def.Fields[2].FieldKey != "intervals" {
		t.Fatalf("third kline field = %q, want intervals", def.Fields[2].FieldKey)
	}
	if def.Fields[2].DefaultValue.([]any)[0] != "1m" {
		t.Fatalf("kline interval default = %#v, want [1m]", def.Fields[2].DefaultValue)
	}
}

func TestBuildTaskSpecsDispatchesByCollectorParams(t *testing.T) {
	params := &domain.CollectParams{}
	params.Normalize("binance", "kline")
	params.Target.JobType = JobTypeCollectKline
	subjects := []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT"}}

	specs, err := BuildTaskSpecs(context.Background(), &domain.TaskRule{RuleID: "r1"}, params, subjects)
	if err != nil {
		t.Fatalf("BuildTaskSpecs() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d, want 1", len(specs))
	}
	if specs[0].Params["job_type"] != JobTypeCollectKline {
		t.Fatalf("job_type = %v, want %s", specs[0].Params["job_type"], JobTypeCollectKline)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/jobs`

Expected: FAIL because `ListDefinitions`, `DefinitionByDataType`, and `BuildTaskSpecs` are not defined.

### Task 2: Implement jobs registry and definitions

**Files:**
- Modify: `modules/collector/internal/jobs/registry.go`
- Create: `modules/collector/internal/jobs/jobdef/definition.go`
- Create: `modules/collector/internal/jobs/kline/definition.go`
- Create: `modules/collector/internal/jobs/symbol/definition.go`

- [x] **Step 1: Implement common registry types**

Add `Definition`, `FieldDefinition`, `OptionList`, `BuildTaskSpecs`, `ListDefinitions`, and `DefinitionByDataType` under `internal/jobs`.

- [x] **Step 2: Implement kline and symbol definitions**

Move K-line and symbol type names, descriptions, field metadata, interval options, and Binance datasource option into their job packages.

- [x] **Step 3: Run registry tests**

Run: `go test ./internal/jobs`

Expected: PASS.

### Task 3: Route planner through jobs registry

**Files:**
- Modify: `modules/collector/internal/planner/kline.go`
- Test: existing planner and jobs tests

- [x] **Step 1: Replace hard-coded planner switch**

Change `planner.buildTaskSpecs` to call `jobs.BuildTaskSpecs`.

- [x] **Step 2: Run planner-related tests**

Run: `go test ./internal/planner ./internal/jobs`

Expected: PASS.

### Task 4: Slim RPC data type config methods

**Files:**
- Modify: `modules/collector/internal/rpc/service.go`
- Test: new RPC data type config tests if needed

- [x] **Step 1: Convert jobs definitions to protobuf**

`GetDataTypeConfigs` should call `jobs.ListDefinitions`. `GetDataTypeConfigWithFields` should call `jobs.DefinitionByDataType`.

- [x] **Step 2: Delete RPC-local K-line/symbol config helpers**

Remove `klineDataTypeConfig`, `symbolDataTypeConfig`, and `supportedDataSourceOptions` from `service.go`.

- [x] **Step 3: Run collector tests**

Run: `go test ./...`

Expected: PASS.

### Task 5: Final verification

**Files:**
- All touched collector files

- [x] **Step 1: Run module tests**

Run: `go test ./...` from `modules/collector`.

Expected: PASS.

- [x] **Step 2: Run diff checks**

Run from repo root: `git diff --check`

Expected: no output and exit 0.
