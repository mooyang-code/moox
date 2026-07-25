package trigger

import (
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/testkit"
	"github.com/mooyang-code/moox/packages/events"
)

func TestLiveConsumerContractUsesOwnedConsumer(t *testing.T) {
	valid := NATSConfig{Stream: LiveStream, Consumer: LiveConsumer, FetchMaxWait: time.Second}
	if err := ValidateLiveConsumerConfig(valid); err != nil {
		t.Fatalf("valid live consumer config rejected: %v", err)
	}

	for name, cfg := range map[string]NATSConfig{
		"different stream":  {Stream: "MOOX_STORAGE_REPLAY", Consumer: LiveConsumer},
		"different durable": {Stream: LiveStream, Consumer: "factor_replay"},
		"empty stream":      {Consumer: LiveConsumer},
		"empty durable":     {Stream: LiveStream},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateLiveConsumerConfig(cfg); !errors.Is(err, ErrInvalidLiveConsumer) {
				t.Fatalf("ValidateLiveConsumerConfig() error = %v, want ErrInvalidLiveConsumer", err)
			}
		})
	}
}

func TestLiveConsumerConfig(t *testing.T) {
	ref, err := liveConsumerConfig(NATSConfig{Stream: LiveStream, Consumer: LiveConsumer, FetchMaxWait: 2 * time.Second})
	if err != nil {
		t.Fatalf("liveConsumerConfig() error = %v", err)
	}
	if ref.Event.Name() != events.DatasetRowsUpserted.Name() ||
		ref.Event.Version() != events.DatasetRowsUpserted.Version() ||
		ref.Name != LiveConsumer ||
		ref.FetchMaxWait != 2*time.Second {
		t.Fatalf("live consumer bind ref = %+v", ref)
	}
}

func TestEventStormEmitsOneTaskPerSubject(t *testing.T) {
	symbols := testkit.Symbols(500)
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	d := NewEventBatcher(time.Second, []domain.FactorBinding{{
		BindingID:     "b1",
		FactorID:      "bias",
		SpaceID:       "crypto",
		SourceDataset: "binance_spot_kline",
		Freq:          "1m",
		SubjectMode:   domain.SubjectModeAll,
		SubjectsJSON:  "[]",
		TargetDataset: "binance_spot_factor",
		Status:        domain.BindingStatusEnabled,
	}})

	d.Ingest(testkit.RowsChangedEvent("crypto", "binance_spot_kline", "1m", now, symbols), now)
	tasks := d.Flush(now.Add(time.Second))
	if len(tasks) != len(symbols) {
		t.Fatalf("tasks = %d, want %d", len(tasks), len(symbols))
	}
	seen := map[string]struct{}{}
	for _, task := range tasks {
		if len(task.FactorIDs) != 1 || task.FactorIDs[0] != "bias" {
			t.Fatalf("factor ids = %#v", task.FactorIDs)
		}
		seen[task.SubjectID] = struct{}{}
	}
	if len(seen) != len(symbols) {
		t.Fatalf("unique subjects = %d, want %d", len(seen), len(symbols))
	}
}

func TestEventBatcherSplitsTasksByTargetDataset(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	d := NewEventBatcher(time.Second, []domain.FactorBinding{
		{
			BindingID:     "b1",
			FactorID:      "bias",
			SpaceID:       "crypto",
			SourceDataset: "binance_spot_kline",
			Freq:          "1m",
			SubjectMode:   domain.SubjectModeAll,
			SubjectsJSON:  "[]",
			TargetDataset: "binance_spot_factor",
			Status:        domain.BindingStatusEnabled,
		},
		{
			BindingID:     "b2",
			FactorID:      "volume",
			SpaceID:       "crypto",
			SourceDataset: "binance_spot_kline",
			Freq:          "1m",
			SubjectMode:   domain.SubjectModeAll,
			SubjectsJSON:  "[]",
			TargetDataset: "binance_spot_volume_factor",
			Status:        domain.BindingStatusEnabled,
		},
	})

	d.Ingest(testkit.RowsChangedEvent("crypto", "binance_spot_kline", "1m", now, []string{"BTC-USDT"}), now)
	tasks := d.Flush(now.Add(time.Second))
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2: %+v", len(tasks), tasks)
	}
	byTarget := map[string][]string{}
	for _, task := range tasks {
		byTarget[task.TargetDataset] = task.FactorIDs
	}
	if got := byTarget["binance_spot_factor"]; len(got) != 1 || got[0] != "bias" {
		t.Fatalf("binance_spot_factor ids = %#v", got)
	}
	if got := byTarget["binance_spot_volume_factor"]; len(got) != 1 || got[0] != "volume" {
		t.Fatalf("binance_spot_volume_factor ids = %#v", got)
	}
}
