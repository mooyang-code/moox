package jobs

import (
	"testing"
)

func TestListJobDefinitionsReturnsStableDataTypes(t *testing.T) {
	defs := ListJobDefinitions()
	if len(defs) != 2 || defs[0].DataType != "kline" || defs[1].DataType != "kline_resample" {
		t.Fatalf("data types = %#v; want kline, kline_resample", defs)
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
}

func TestExistingJobDefinitionsRemainCloudInvoked(t *testing.T) {
	for _, dataType := range []string{"kline"} {
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
	if len(def.Fields) != 1 {
		t.Fatalf("len(kline fields) = %d, want 1", len(def.Fields))
	}
	if def.Fields[0].FieldKey != "frequency" {
		t.Fatalf("kline field = %q, want frequency", def.Fields[0].FieldKey)
	}
	if def.Fields[0].DefaultValue != "1m" {
		t.Fatalf("kline frequency default = %#v, want 1m", def.Fields[0].DefaultValue)
	}
}
