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

func TestDefinitionByDataTypeReturnsKlineFields(t *testing.T) {
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
	defaults, ok := def.Fields[2].DefaultValue.([]any)
	if !ok || len(defaults) != 1 || defaults[0] != "1m" {
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
