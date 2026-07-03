package planner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
)

func TestBuildInstancesSymbolDoesNotRequireSubjects(t *testing.T) {
	rule := &domain.TaskRule{
		SpaceID:  "space-a",
		RuleID:   "rule-symbol",
		Exchange: "binance",
		DataType: "symbol",
		CollectParams: `{
			"collector":{"exchange":"binance","market":"spot","data_type":"symbol"},
			"target":{"dataset_id":"binance_spot_symbol","workload_type":"collector.binance.spot.symbol","deployment_id":"symbol-v1"},
			"schedule":{"interval":"6h","timezone":"UTC"}
		}`,
	}

	got, err := NewTaskBuilder().BuildInstances(context.Background(), rule, nil)
	if err != nil {
		t.Fatalf("BuildInstances() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("BuildInstances() len = %d, want 1", len(got))
	}
	inst := got[0]
	if inst.DataType != "symbol" || inst.Exchange != "binance" || inst.Market != "spot" {
		t.Fatalf("unexpected instance routing: exchange=%s market=%s data_type=%s", inst.Exchange, inst.Market, inst.DataType)
	}
	if inst.SubjectID != "" || inst.Symbol != "" || inst.Interval != "" {
		t.Fatalf("symbol instance should not be subject driven: subject=%q symbol=%q interval=%q", inst.SubjectID, inst.Symbol, inst.Interval)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(inst.TaskParams), &params); err != nil {
		t.Fatalf("unmarshal task params: %v", err)
	}
	if params["workload_type"] != "collector.binance.spot.symbol" {
		t.Fatalf("workload_type = %v", params["workload_type"])
	}
}

func TestBuildInstancesNormalizesRoutingFields(t *testing.T) {
	rule := &domain.TaskRule{
		SpaceID:  "space-a",
		RuleID:   "rule-symbol-upper",
		Exchange: "BINANCE",
		DataType: "SYMBOL",
		CollectParams: `{
			"collector":{"exchange":"BINANCE","market":"SPOT","data_type":"SYMBOL"},
			"target":{"dataset_id":"binance_spot_symbol"}
		}`,
	}

	got, err := NewTaskBuilder().BuildInstances(context.Background(), rule, nil)
	if err != nil {
		t.Fatalf("BuildInstances() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("BuildInstances() len = %d, want 1", len(got))
	}
	if got[0].Exchange != "binance" || got[0].Market != "spot" || got[0].DataType != "symbol" {
		t.Fatalf("routing fields = %s/%s/%s, want lowercase binance/spot/symbol", got[0].Exchange, got[0].Market, got[0].DataType)
	}
}

func TestBuildInstancesKlineRequiresDatasetSubjects(t *testing.T) {
	rule := &domain.TaskRule{
		SpaceID:  "space-a",
		RuleID:   "rule-kline",
		Exchange: "binance",
		DataType: "kline",
		CollectParams: `{
			"source":{"kind":"none"},
			"collector":{"exchange":"binance","market":"spot","data_type":"kline"},
			"target":{"dataset_id":"binance_spot_kline"}
		}`,
	}

	_, err := NewTaskBuilder().BuildInstances(context.Background(), rule, nil)
	if err == nil {
		t.Fatalf("BuildInstances() expected error")
	}
	if !strings.Contains(err.Error(), "dataset_subjects") {
		t.Fatalf("BuildInstances() error = %v, want dataset_subjects message", err)
	}
}
