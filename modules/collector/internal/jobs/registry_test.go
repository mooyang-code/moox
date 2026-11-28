package jobs

import (
	"testing"
)

func TestListJobDefinitionsReturnsStableDataTypes(t *testing.T) {
	defs := ListJobDefinitions()
	if len(defs) != 2 || defs[0].DataType != "kline" || defs[1].DataType != "kline_resample" {
		t.Fatalf("data types = %#v; want kline, kline_resample", defs)
	}
}

func TestJobDefinitionByDataTypeReturnsKlineFrequencyOptions(t *testing.T) {
	def, ok := JobDefinitionByDataType(" KLINE ")
	if !ok {
		t.Fatal("kline definition not found")
	}
	if len(def.FrequencyOptions) == 0 || def.FrequencyOptions[0].Value != "1m" {
		t.Fatalf("kline frequency options = %#v, want non-empty starting with 1m", def.FrequencyOptions)
	}
}

func TestJobDefinitionByDataTypeFindsResample(t *testing.T) {
	def, ok := JobDefinitionByDataType(" KLINE_RESAMPLE ")
	if !ok || def.DataType != "kline_resample" {
		t.Fatalf("kline_resample definition = %#v, ok=%v", def, ok)
	}
}
