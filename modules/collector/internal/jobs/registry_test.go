package jobs

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
)

func TestListJobDefinitionsReturnsStableDataTypes(t *testing.T) {
	defs := ListJobDefinitions()
	if len(defs) != 3 || defs[0].DataType != "kline" || defs[1].DataType != "symbol" || defs[2].DataType != "kline_resample" {
		t.Fatalf("data types = %#v; want kline, symbol, kline_resample", defs)
	}
	if defs[0].DataSourceOptions.Options[0].Value != "binance" {
		t.Fatalf("first datasource = %q, want binance", defs[0].DataSourceOptions.Options[0].Value)
	}
}

func TestResampleJobDefinitionRunsInsideCollector(t *testing.T) {
	def, ok := JobDefinitionByDataType(" KLINE_RESAMPLE ")
	if !ok {
		t.Fatal("kline_resample definition not found")
	}
	if def.ExecutionMode != ExecutionModeCollectorLocal {
		t.Fatalf("execution mode = %q, want collector_local", def.ExecutionMode)
	}
	if _, routeExists := JobRouteFor("moox", "kline_resample"); routeExists {
		t.Fatal("local resample definition must not register a cloud queue route")
	}
}

func TestExistingJobDefinitionsRemainCloudInvoked(t *testing.T) {
	for _, dataType := range []string{"kline", "symbol"} {
		def, ok := JobDefinitionByDataType(dataType)
		if !ok || def.ExecutionMode != ExecutionModeCloudInvoke {
			t.Fatalf("%s definition = %#v", dataType, def)
		}
	}
}

func TestJobDefinitionByDataTypeReturnsKlineFields(t *testing.T) {
	def, ok := JobDefinitionByDataType("kline")
	if !ok {
		t.Fatalf("kline definition not found")
	}
	if len(def.Fields) != 2 {
		t.Fatalf("len(kline fields) = %d, want 2", len(def.Fields))
	}
	if def.Fields[1].FieldKey != "intervals" {
		t.Fatalf("second kline field = %q, want intervals", def.Fields[1].FieldKey)
	}
	defaults, ok := def.Fields[1].DefaultValue.([]any)
	if !ok || len(defaults) != 1 || defaults[0] != "1m" {
		t.Fatalf("kline interval default = %#v, want [1m]", def.Fields[1].DefaultValue)
	}
}

func TestBuildTaskSpecsDispatchesByCollectorParams(t *testing.T) {
	params := &domain.CollectParams{}
	params.Normalize("binance", "spot", "kline")
	params.Source.Kind = "dataset_subjects"
	params.Collector.Market = "spot"
	params.Collector.Intervals = []string{"1m"}
	params.Target.DatasetID = "ds-1"
	subjects := []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT"}}

	specs, err := BuildTaskSpecs(context.Background(), &domain.TaskRule{RuleID: "r1"}, params, subjects)
	if err != nil {
		t.Fatalf("BuildTaskSpecs() error = %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("len(specs) = %d, want 1", len(specs))
	}
	if _, exists := specs[0].Params["job_type"]; exists {
		t.Fatalf("job_type must not be duplicated in task params: %#v", specs[0].Params)
	}
}
