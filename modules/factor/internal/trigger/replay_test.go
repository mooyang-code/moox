package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
)

type replaySourceFake struct{ events []ReplayEvent }

func (s replaySourceFake) Load(context.Context, ReplayRequest) ([]ReplayEvent, error) {
	return s.events, nil
}

func TestReplayRangeCarriesExplicitIdentityAndTimeSemantics(t *testing.T) {
	start := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	batcher := NewEventBatcher(2*time.Second, []domain.FactorBinding{binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")})
	req := ReplayRequest{SpaceID: "crypto", DatasetID: "binance_spot_kline", StartTime: start, EndTime: end, FactorVersion: "factor-v7", TargetRunID: "run-42"}
	tasks, err := batcher.ReplayRange(context.Background(), req, replaySourceFake{events: []ReplayEvent{{
		MessageID:  "replay-1",
		Event:      event("crypto", "binance_spot_kline", "BTC-USDT", "1m", start.Add(30*time.Second)),
		ReceivedAt: start.Add(10 * time.Second),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks=%+v, want one replay task", tasks)
	}
	task := tasks[0]
	if task.TriggerType != "replay" || task.FactorVersion != "factor-v7" || task.TargetRunID != "run-42" {
		t.Fatalf("replay identity=%+v", task)
	}
	if !task.FirstReceivedAt.Equal(start.Add(10*time.Second)) || !task.LastReceivedAt.Equal(start.Add(10*time.Second)) {
		t.Fatalf("received time metadata=%+v", task)
	}
	if !task.BarTime.Equal(start.Add(30 * time.Second)) {
		t.Fatalf("bar time=%s", task.BarTime)
	}
}

func TestReplayRangeDoesNotFlushLiveBucket(t *testing.T) {
	start := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	batcher := NewEventBatcher(2*time.Second, []domain.FactorBinding{binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")})
	batcher.Ingest(event("crypto", "binance_spot_kline", "ETH-USDT", "1m", start), start)
	req := ReplayRequest{SpaceID: "crypto", DatasetID: "binance_spot_kline", StartTime: start, EndTime: start.Add(time.Minute), FactorVersion: "factor-v7", TargetRunID: "run-43"}
	tasks, err := batcher.ReplayRange(context.Background(), req, replaySourceFake{events: []ReplayEvent{{
		MessageID: "replay-1", Event: event("crypto", "binance_spot_kline", "BTC-USDT", "1m", start.Add(30*time.Second)), ReceivedAt: start.Add(10 * time.Second),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].SubjectID != "BTC-USDT" || tasks[0].TriggerType != "replay" {
		t.Fatalf("replay tasks=%+v", tasks)
	}
	live := batcher.Flush(start.Add(3 * time.Second))
	if len(live) != 1 || live[0].SubjectID != "ETH-USDT" || live[0].TriggerType == "replay" {
		t.Fatalf("live tasks after replay=%+v", live)
	}
}

func TestReplayRangeFlushesWhenReceivedAfterReplayEnd(t *testing.T) {
	start := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	batcher := NewEventBatcher(2*time.Second, []domain.FactorBinding{binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")})
	req := ReplayRequest{SpaceID: "crypto", DatasetID: "binance_spot_kline", StartTime: start, EndTime: end, FactorVersion: "factor-v7", TargetRunID: "run-late"}
	tasks, err := batcher.ReplayRange(context.Background(), req, replaySourceFake{events: []ReplayEvent{{
		MessageID: "replay-late", Event: event("crypto", "binance_spot_kline", "BTC-USDT", "1m", start.Add(30*time.Second)), ReceivedAt: end.Add(10 * time.Minute),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || !tasks[0].LastReceivedAt.Equal(end.Add(10*time.Minute)) {
		t.Fatalf("replay tasks=%+v, want one task with late received_at", tasks)
	}
}

func TestReplayRangeCreatesOneTaskPerHistoricalBar(t *testing.T) {
	start := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	end := start.Add(3 * time.Minute)
	batcher := NewEventBatcher(2*time.Second, []domain.FactorBinding{binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")})
	req := ReplayRequest{SpaceID: "crypto", DatasetID: "binance_spot_kline", StartTime: start, EndTime: end, FactorVersion: "factor-v7", TargetRunID: "run-bars"}
	events := []ReplayEvent{
		{MessageID: "bar-2", Event: event("crypto", "binance_spot_kline", "BTC-USDT", "1m", start.Add(time.Minute)), ReceivedAt: start.Add(2 * time.Hour)},
		{MessageID: "bar-1", Event: event("crypto", "binance_spot_kline", "BTC-USDT", "1m", start), ReceivedAt: start.Add(2 * time.Hour)},
	}
	tasks, err := batcher.ReplayRange(context.Background(), req, replaySourceFake{events: events})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || !tasks[0].BarTime.Equal(start) || !tasks[1].BarTime.Equal(start.Add(time.Minute)) {
		t.Fatalf("replay tasks=%+v, want one task per bar in order", tasks)
	}
}

func TestReplayRequestRejectsImplicitExecutionScope(t *testing.T) {
	request := ReplayRequest{SpaceID: "crypto", DatasetID: "bars", StartTime: time.Now(), EndTime: time.Now().Add(time.Minute)}
	if err := request.Validate(); err == nil {
		t.Fatal("Validate() accepted replay without factor version and target run")
	}
}

func TestReplayRangeRejectsOutOfRangeData(t *testing.T) {
	start := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	request := ReplayRequest{SpaceID: "crypto", DatasetID: "binance_spot_kline", StartTime: start, EndTime: start.Add(time.Minute), FactorVersion: "factor-v7", TargetRunID: "run-42"}
	batcher := NewEventBatcher(time.Second, []domain.FactorBinding{binding("bias", "binance_spot_kline", domain.SubjectModeAll, "[]")})
	_, err := batcher.ReplayRange(context.Background(), request, replaySourceFake{events: []ReplayEvent{{
		MessageID: "replay-1", Event: &storagepb.DatasetRowsUpserted{SpaceId: "crypto", DatasetId: "binance_spot_kline", Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{SpaceId: "crypto", DatasetId: "binance_spot_kline", Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: start.Add(2 * time.Minute).Format(time.RFC3339)}}}}}}, ReceivedAt: start,
	}}})
	if err == nil {
		t.Fatal("ReplayRange accepted data outside requested range")
	}
}
