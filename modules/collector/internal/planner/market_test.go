package planner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
)

type leaseRecorder struct{ leases []repository.MarketLease }

func (r *leaseRecorder) PutLease(_ context.Context, lease repository.MarketLease) error {
	r.leases = append(r.leases, lease)
	return nil
}

func TestMarketPlannerCreatesLogicalTaskAndFencedRuntimeParams(t *testing.T) {
	now := time.Date(2026, 7, 11, 2, 0, 0, 0, time.UTC)
	recorder := &leaseRecorder{}
	planner := MarketPlanner{Leases: recorder, Now: func() time.Time { return now }}
	request := MarketKlinePlanRequest{RuleID: "hourly", MarketID: "crypto_binance", SpaceID: "crypto_binance", ExchangeID: "BINANCE", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, UnifiedDatasetID: "spot_kline", SubjectID: "BTC-USDT", Frequency: marketdata.FrequencyHour, StartTime: now.Add(-time.Hour), EndTime: now, ScheduleWindow: now, ScheduleInterval: time.Hour, LeaseTTL: 2 * time.Minute, LeaseEpoch: 7, Limit: 1000, CandidateChain: []KlinePlanProvider{{ProviderID: "binance_rest", SourceDatasetID: "binance_spot_kline", ProviderSymbol: "BTCUSDT", QuotaScopeKey: "public-ip", QuotaWindows: []repository.QuotaWindow{{WindowSeconds: 60, Limit: 1200}}}}}
	instance, err := planner.PlanKline(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.leases) != 2 || recorder.leases[0].LeaseType != "provider" || recorder.leases[1].LeaseType != "resolution" {
		t.Fatalf("leases = %+v", recorder.leases)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(instance.TaskParams), &params); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"market_id", "source_dataset_id", "unified_dataset_id", "quota_lease_id", "lease_epoch", "resolution_lease_id", "resolution_lease_epoch", "execution_nonce", "quota_windows"} {
		if params[key] == nil || params[key] == "" {
			t.Fatalf("missing runtime param %q in %v", key, params)
		}
	}
	if len(params["source_dataset_ids"].([]any)) != 1 || params["source_datasets"].(map[string]any)["binance_rest"] != "binance_spot_kline" {
		t.Fatalf("source candidates=%v", params)
	}
	request.CandidateChain[0].ProviderID = "other"
	request.CandidateChain[0].SourceDatasetID = "other_source"
	second, err := planner.PlanKline(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.TaskID != instance.TaskID {
		t.Fatalf("logical task id changed with provider: %s != %s", second.TaskID, instance.TaskID)
	}
}
