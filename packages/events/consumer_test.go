package events

import (
	"reflect"
	"strings"
	"testing"
)

func TestConsumerEventFiltersSupportOneDurableWithMultipleEvents(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	cfg := ConsumerConfig{Events: []Event{
		DatasetRowsUpserted,
		CollectorPeriodCompleted,
		FactorPeriodComputed,
		DatasetSyncPoint,
	}}
	stream, filters, err := consumerEventFilters(registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stream != "MOOX_STORAGE" {
		t.Fatalf("stream = %q", stream)
	}
	want := []string{
		"moox.event.storage.dataset.rows.upserted.v2.>",
		"moox.event.storage.collector.period.completed.v1.>",
		"moox.event.storage.dataset.factor_period.computed.v1.>",
		"moox.event.storage.dataset.sync_point.v1.>",
	}
	if !reflect.DeepEqual(filters, want) {
		t.Fatalf("filters = %v, want %v", filters, want)
	}
	transport := jetstreamConsumerConfig(cfg, stream, filters)
	if transport.FilterSubject != "" || !reflect.DeepEqual(transport.FilterSubjects, want) {
		t.Fatalf("transport filters = %q / %v", transport.FilterSubject, transport.FilterSubjects)
	}
}

func TestConsumerEventFiltersRejectAmbiguousOrCrossStreamEvents(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	tests := []ConsumerConfig{
		{Event: DatasetRowsUpserted, Events: []Event{CollectorPeriodCompleted}},
		{Events: []Event{DatasetRowsUpserted, DatasetRowsUpserted}},
		{Events: []Event{DatasetRowsUpserted, MarketFetchBatchCompleted}},
		{},
	}
	for _, cfg := range tests {
		if _, _, err := consumerEventFilters(registry, cfg); err == nil {
			t.Fatalf("consumerEventFilters(%+v) succeeded", cfg)
		}
	}
}

func TestConsumerEventFiltersAcceptExactSubjectPartition(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	row, err := registry.RenderSubject(DatasetRowsUpserted, "crypto", "dataset_binance_kline_1m")
	if err != nil {
		t.Fatal(err)
	}
	marker, err := registry.RenderSubject(CollectorPeriodCompleted, "crypto", "dataset_binance_kline_1m")
	if err != nil {
		t.Fatal(err)
	}
	cfg := ConsumerConfig{Stream: DatasetRowsUpserted.Stream(), FilterSubjects: []string{row, marker}}
	stream, filters, err := consumerEventFilters(registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stream != DatasetRowsUpserted.Stream() || !reflect.DeepEqual(filters, []string{row, marker}) {
		t.Fatalf("exact filters = %q/%v", stream, filters)
	}
}

func TestConsumerEventFiltersRejectExactSubjectWithoutStreamOrMixedMode(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []ConsumerConfig{
		{FilterSubjects: []string{"moox.event.storage.dataset.rows.upserted.v2.crypto.binance"}},
		{Stream: DatasetRowsUpserted.Stream(), FilterSubjects: []string{"moox.event.storage.dataset.rows.upserted.v2.crypto.binance"}, Event: DatasetRowsUpserted},
	} {
		if _, _, err := consumerEventFilters(registry, cfg); err == nil {
			t.Fatalf("consumerEventFilters(%+v) accepted invalid exact filter config", cfg)
		}
	}
}

func TestSpaceConsumerFilterOnlyIncludesOneSpace(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	got, err := spaceConsumerFilter(registry, SpaceConsumerConfig{
		ConsumerConfig: ConsumerConfig{Event: MarketFetchBatchCompleted},
		SpaceID:        "crypto",
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := registry.SpacePattern(MarketFetchBatchCompleted, "crypto")
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !strings.HasSuffix(got, ".>") {
		t.Fatalf("filter = %q, want %q", got, want)
	}
}

func TestSpaceConsumerFilterRejectsEmptySpaceID(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	_, err = spaceConsumerFilter(registry, SpaceConsumerConfig{ConsumerConfig: ConsumerConfig{Event: MarketFetchBatchCompleted}})
	if err == nil || !strings.Contains(err.Error(), "space_id") {
		t.Fatalf("error = %v, want space_id validation", err)
	}
}
