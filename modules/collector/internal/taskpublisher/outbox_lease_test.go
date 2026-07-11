package taskpublisher

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
)

type outboxLeaseRecorder struct{ values []repository.MarketLease }

func (r *outboxLeaseRecorder) TryAcquireLeaseGroup(_ context.Context, values []repository.MarketLease, _ time.Time) error {
	r.values = append(r.values, values...)
	return nil
}

func TestOutboxLeasePreparerReplacesStaleFencing(t *testing.T) {
	now := time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC)
	recorder := &outboxLeaseRecorder{}
	payload := `{"job_type":"collect.kline","market_id":"stock_cn","provider_id":"fallback","unified_dataset_id":"equity_kline","subject_id":"CN.600000","frequency":"1d","start_time":"2026-07-01T00:00:00Z","end_time":"2026-07-11T00:00:00Z","quota_lease_id":"stale","lease_epoch":"1","resolution_lease_id":"stale","resolution_lease_epoch":"1","execution_nonce":"stale"}`
	value, err := (OutboxLeasePreparer{Leases: recorder}).Prepare(context.Background(), domain.AttemptOutbox{OutboxID: "outbox-1", Payload: payload}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.values) != 2 || recorder.values[0].LeaseType != "provider" || recorder.values[1].LeaseType != "resolution" {
		t.Fatalf("leases=%+v", recorder.values)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(value.Payload), &params); err != nil {
		t.Fatal(err)
	}
	if params["quota_lease_id"] == "stale" || params["resolution_lease_id"] == "stale" || params["execution_nonce"] != "outbox-1" || params["lease_epoch"] == "1" {
		t.Fatalf("prepared payload=%v", params)
	}
}

func TestOutboxLeasePreparerLeavesNonKlinePayloadAlone(t *testing.T) {
	recorder := &outboxLeaseRecorder{}
	input := domain.AttemptOutbox{OutboxID: "outbox-1", Payload: `{"job_type":"collect.instrument"}`}
	got, err := (OutboxLeasePreparer{Leases: recorder}).Prepare(context.Background(), input, time.Now())
	if err != nil || got.Payload != input.Payload || len(recorder.values) != 0 {
		t.Fatalf("got=%+v leases=%v err=%v", got, recorder.values, err)
	}
}
