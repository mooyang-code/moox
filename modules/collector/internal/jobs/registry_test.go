package jobs

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
)

func TestListJobDefinitionsReturnsStableDataTypesAndJobTypes(t *testing.T) {
	defs := ListJobDefinitions()
	if len(defs) != 2 || defs[0].DataType != "kline" || defs[1].DataType != "symbol" {
		t.Fatalf("data types = %#v; want kline, symbol", defs)
	}
	if defs[0].JobType != JobTypeCollectKline || defs[1].JobType != JobTypeCollectSymbol {
		t.Fatalf("job types = %q, %q; want %q, %q", defs[0].JobType, defs[1].JobType, JobTypeCollectKline, JobTypeCollectSymbol)
	}
	if defs[0].DataSourceOptions.Options[0].Value != "binance" {
		t.Fatalf("first datasource = %q, want binance", defs[0].DataSourceOptions.Options[0].Value)
	}
}

func TestSupportedJobTypesReturnsStableCopy(t *testing.T) {
	got := SupportedJobTypes()
	if len(got) != 2 || got[0] != JobTypeCollectKline || got[1] != JobTypeCollectSymbol {
		t.Fatalf("SupportedJobTypes() = %#v", got)
	}
	got[0] = "modified"
	if next := SupportedJobTypes(); next[0] != JobTypeCollectKline {
		t.Fatalf("SupportedJobTypes() returned shared storage: %#v", next)
	}
}

func TestJobDefinitionByDataTypeReturnsKlineFields(t *testing.T) {
	def, ok := JobDefinitionByDataType("kline")
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
