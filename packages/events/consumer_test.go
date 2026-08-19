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
		DatasetPeriodCollected,
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
		"moox.storage.dataset.rows.upserted.v2.>",
		"moox.storage.dataset.period.collected.v1.>",
		"moox.storage.dataset.factor_period.computed.v1.>",
		"moox.storage.dataset.sync_point.v1.>",
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
		{Event: DatasetRowsUpserted, Events: []Event{DatasetPeriodCollected}},
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
	row, err := registry.RenderSubject(DatasetRowsUpserted, "crypto_market", "binance_spot_kline_1m")
	if err != nil {
		t.Fatal(err)
	}
	marker, err := registry.RenderSubject(DatasetPeriodCollected, "crypto_market", "binance_spot_kline_1m")
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
		{FilterSubjects: []string{"moox.storage.dataset.rows.upserted.v2.crypto.binance"}},
		{Stream: DatasetRowsUpserted.Stream(), FilterSubjects: []string{"moox.storage.dataset.rows.upserted.v2.crypto.binance"}, Event: DatasetRowsUpserted},
	} {
		if _, _, err := consumerEventFilters(registry, cfg); err == nil {
			t.Fatalf("consumerEventFilters(%+v) accepted invalid exact filter config", cfg)
		}
	}
}

func TestSubjectConsumerFilterUsesRegistryIdentity(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	cfg := SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{
			Name:  "cloudnode-route",
			Event: CloudJobExecutionRequested,
		},
		SpaceID:   "crypto",
		SubjectID: "collector.pkg/collect.kline",
	}
	got, err := subjectConsumerFilter(registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want, err := registry.RenderSubject(
		CloudJobExecutionRequested,
		"crypto",
		"collector.pkg/collect.kline",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || strings.Contains(got, ">") {
		t.Fatalf("filter = %q, want exact %q", got, want)
	}
}

func TestSubjectConsumerFilterSeparatesRoutes(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	first, err := subjectConsumerFilter(registry, SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{Event: CloudJobExecutionRequested},
		SpaceID:        "crypto",
		SubjectID:      "collector.pkg/collect.kline",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := subjectConsumerFilter(registry, SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{Event: CloudJobExecutionRequested},
		SpaceID:        "crypto",
		SubjectID:      "collector.pkg/collect.symbol",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("distinct routes rendered the same subject %q", first)
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

func TestSubjectConsumerFilterRejectsEmptySpaceID(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	_, err = subjectConsumerFilter(registry, SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{Event: CloudJobExecutionRequested},
		SubjectID:      "collector.pkg/collect.kline",
	})
	if err == nil || !strings.Contains(err.Error(), "space_id") {
		t.Fatalf("error = %v, want space_id validation", err)
	}
}

func TestSubjectConsumerFilterRejectsEmptySubjectID(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	_, err = subjectConsumerFilter(registry, SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{Event: CloudJobExecutionRequested},
		SpaceID:        "crypto",
	})
	if err == nil || !strings.Contains(err.Error(), "subject_id") {
		t.Fatalf("error = %v, want subject_id validation", err)
	}
}

func TestSubjectConsumerFilterRejectsUnregisteredEvent(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	_, err = subjectConsumerFilter(registry, SubjectConsumerConfig{
		ConsumerConfig: ConsumerConfig{Event: Event{name: "unknown", version: 1}},
		SpaceID:        "crypto",
		SubjectID:      "collector.pkg/collect.kline",
	})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("error = %v, want unregistered event validation", err)
	}
}
