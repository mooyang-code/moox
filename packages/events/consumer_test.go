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
		DatasetPeriodCompleted,
		ViewDataReady,
	}}
	stream, filters, err := consumerEventFilters(registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stream != "MOOX_STORAGE" {
		t.Fatalf("stream = %q", stream)
	}
	want := []string{
		"moox.event.storage.dataset.period.completed.v1.>",
		"moox.event.storage.view.data.ready.v2.>",
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
		{Event: ViewDataReady, Events: []Event{DatasetPeriodCompleted}},
		{Events: []Event{ViewDataReady, ViewDataReady}},
		{Events: []Event{ViewDataReady, ObservabilityHostSnapshotReported}},
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
	row, err := registry.RenderSubject(ViewDataReady, "crypto", "dataset_binance_kline_1m")
	if err != nil {
		t.Fatal(err)
	}
	marker, err := registry.RenderSubject(DatasetPeriodCompleted, "crypto", "dataset_binance_kline_1m")
	if err != nil {
		t.Fatal(err)
	}
	cfg := ConsumerConfig{Stream: ViewDataReady.Stream(), FilterSubjects: []string{row, marker}}
	stream, filters, err := consumerEventFilters(registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stream != ViewDataReady.Stream() || !reflect.DeepEqual(filters, []string{row, marker}) {
		t.Fatalf("exact filters = %q/%v", stream, filters)
	}
}

func TestConsumerEventFiltersRejectExactSubjectWithoutStreamOrMixedMode(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []ConsumerConfig{
		{FilterSubjects: []string{"moox.event.storage.view.data.ready.v2.crypto.binance"}},
		{Stream: ViewDataReady.Stream(), FilterSubjects: []string{"moox.event.storage.view.data.ready.v2.crypto.binance"}, Event: ViewDataReady},
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
		ConsumerConfig: ConsumerConfig{Event: ViewDataReady},
		SpaceID:        "crypto",
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := registry.SpacePattern(ViewDataReady, "crypto")
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
	_, err = spaceConsumerFilter(registry, SpaceConsumerConfig{ConsumerConfig: ConsumerConfig{Event: ViewDataReady}})
	if err == nil || !strings.Contains(err.Error(), "space_id") {
		t.Fatalf("error = %v, want space_id validation", err)
	}
}
