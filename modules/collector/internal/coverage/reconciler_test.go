package coverage

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

func TestReconcilerPersistsIncompleteStateAndDeterministicRepairs(t *testing.T) {
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	fixture := &coverageFixture{present: []time.Time{base, base.Add(2 * time.Minute)}}
	reconciler := Reconciler{Reader: fixture, States: fixture, Repairs: fixture, Now: func() time.Time { return base.Add(time.Hour) }}
	request := Request{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", PartitionID: "2026-07-11", Frequency: marketdata.FrequencyMinute, Start: base, End: base.Add(3 * time.Minute), Sessions: []Session{{TradeDate: "2026-07-11", Open: base, Close: base.Add(3 * time.Minute), DailyAnchor: base}}}
	state, err := reconciler.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "incomplete" || state.Expected != 3 || state.Present != 2 || state.Missing != 1 || len(fixture.repairs) != 1 {
		t.Fatalf("state=%+v repairs=%+v", state, fixture.repairs)
	}
	firstID := fixture.repairs[0].ID
	fixture.repairs = nil
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(fixture.repairs) != 1 || fixture.repairs[0].ID != firstID {
		t.Fatalf("repair id drifted: %q != %q", fixture.repairs[0].ID, firstID)
	}
}

type coverageFixture struct {
	present []time.Time
	states  []State
	repairs []RepairRequest
}

func (f *coverageFixture) PresentBuckets(context.Context, string, string, string, marketdata.Frequency, time.Time, time.Time) ([]time.Time, error) {
	return append([]time.Time(nil), f.present...), nil
}
func (f *coverageFixture) WriteCoverageState(_ context.Context, state State) error {
	f.states = append(f.states, state)
	return nil
}
func (f *coverageFixture) EnqueueRepair(_ context.Context, request RepairRequest) error {
	f.repairs = append(f.repairs, request)
	return nil
}
