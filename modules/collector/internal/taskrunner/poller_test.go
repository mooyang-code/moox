package taskrunner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/jobs"
	nodeRuntime "github.com/mooyang-code/moox/packages/cloudruntime"
)

func TestTaskEventFromJobItemUsesStableTaskIDFromParams(t *testing.T) {
	event, err := taskEventFromJobItem(nodeRuntime.JobItem{
		JobItemID: "task-1:2026-07-04T09:07:00Z",
		Params: map[string]any{
			"space_id":   "crypto",
			"task_id":    "task-1",
			"exchange":   "binance",
			"data_type":  "kline",
			"market":     "spot",
			"symbol":     "BTCUSDT",
			"subject_id": "BTCUSDT",
			"interval":   "1m",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.TaskID != "task-1" {
		t.Fatalf("TaskID = %q, want stable task_id from params", event.TaskID)
	}
	if event.Symbol != "BTCUSDT" || len(event.Intervals) != 1 || event.Intervals[0] != "1m" {
		t.Fatalf("unexpected task event: %#v", event)
	}
}

func TestMarketExecutionContextReservesFinalizeWindow(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	ctx, cancel, err := marketExecutionContext(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.Equal(now.Add(20*time.Second)) {
		t.Fatalf("deadline=%s", deadline)
	}
	parent, parentCancel := context.WithDeadline(context.Background(), now.Add(15*time.Second))
	defer parentCancel()
	short, shortCancel, err := marketExecutionContext(parent, now)
	if err != nil {
		t.Fatal(err)
	}
	defer shortCancel()
	shortDeadline, _ := short.Deadline()
	if !shortDeadline.Equal(now.Add(5 * time.Second)) {
		t.Fatalf("short deadline=%s", shortDeadline)
	}
}

func TestTaskEventFromJobItemAllowsSymbolWithoutSymbolOrInterval(t *testing.T) {
	got, err := taskEventFromJobItem(nodeRuntime.JobItem{
		JobItemID: "task-symbol:2026-07-04T10:30:00Z",
		Params: map[string]any{
			"task_id":   "task-symbol",
			"exchange":  "binance",
			"market":    "spot",
			"data_type": "symbol",
		},
	})
	if err != nil {
		t.Fatalf("taskEventFromJobItem() error = %v", err)
	}
	if got.TaskID != "task-symbol" || got.DataType != "symbol" || got.Market != "spot" || got.InstType != "SPOT" {
		t.Fatalf("unexpected event: %+v", got)
	}
	if len(got.Intervals) != 1 || got.Intervals[0] != "" {
		t.Fatalf("Intervals = %#v, want one empty interval marker", got.Intervals)
	}
}

func TestMarketFollowUpsGroupContinuationAndFallbackDeterministically(t *testing.T) {
	item := nodeRuntime.JobItem{JobType: jobs.JobTypeCollectKline, Params: map[string]any{"provider_id": "primary", "candidate_chain": []any{"primary", "fallback"}, "candidate_bindings": map[string]any{"fallback": map[string]any{"source_dataset_id": "fallback_kline", "provider_symbol": "BTC-USD", "quota_scope_key": "fallback-ip", "quota_windows": []any{map[string]any{"limit": float64(10)}}}}, "subject_id": "BTC-USDT"}}
	continuation := marketFollowUps(item, map[string]any{"next_cursor": "page-2"}, nil)
	if len(continuation) != 1 || continuation[0].GetKind() != "continuation" || continuation[0].GetPayload().AsMap()["cursor"] != "page-2" {
		t.Fatalf("continuation=%+v", continuation)
	}
	fallback := marketFollowUps(item, map[string]any{}, errors.New("temporary"))
	if len(fallback) != 1 || fallback[0].GetKind() != "fallback" || fallback[0].GetPayload().AsMap()["provider_id"] != "fallback" {
		t.Fatalf("fallback=%+v", fallback)
	}
	payload := fallback[0].GetPayload().AsMap()
	if payload["source_dataset_id"] != "fallback_kline" || payload["provider_symbol"] != "BTC-USD" || payload["quota_scope_key"] != "fallback-ip" {
		t.Fatalf("fallback binding=%v", payload)
	}
}
