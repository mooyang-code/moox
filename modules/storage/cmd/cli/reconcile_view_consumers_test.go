package main

import (
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/packages/events"
)

func TestViewConsumerFilterDriftOnlyResetsChangedConsumers(t *testing.T) {
	desired := map[string][]string{
		"storage_view_kline":   {"rows.crypto.kline", "period.crypto.kline"},
		"storage_view_factor":  {"rows.crypto.factor"},
		"storage_view_metrics": {"rows.mooxsys.metrics"},
	}
	actual := map[string]viewConsumerFilterState{
		"storage_view_kline":   {Exists: true, Filters: []string{"rows.crypto.kline", "period.crypto.kline"}},
		"storage_view_factor":  {Exists: true, Filters: []string{"rows.crypto.old-factor"}},
		"storage_view_metrics": {},
	}

	got := viewConsumerFilterDrift(desired, actual)
	if len(got) != 2 {
		t.Fatalf("drift = %v, want factor and missing consumer", got)
	}
	if !got["storage_view_factor"] || !got["storage_view_metrics"] {
		t.Fatalf("drift = %v, want changed and missing consumers", got)
	}
	if got["storage_view_kline"] {
		t.Fatal("unchanged consumer must not be reset")
	}
}

func TestViewConsumerFilterDriftTreatsSingleFilterAsDifferentRepresentation(t *testing.T) {
	desired := map[string][]string{"storage_view_kline": {"rows.crypto.kline"}}
	actual := map[string]viewConsumerFilterState{
		"storage_view_kline": {Exists: true, SingleFilter: "rows.crypto.kline"},
	}

	got := viewConsumerFilterDrift(desired, actual)
	if !got["storage_view_kline"] {
		t.Fatal("single-filter consumer must be recreated for the multi-filter contract")
	}
}

func TestViewConsumerFilterDriftIgnoresSubjectOrder(t *testing.T) {
	desired := map[string][]string{"storage_view_kline": {"rows.crypto.kline", "period.crypto.kline"}}
	actual := map[string]viewConsumerFilterState{
		"storage_view_kline": {Exists: true, Filters: []string{"period.crypto.kline", "rows.crypto.kline"}},
	}

	if got := viewConsumerFilterDrift(desired, actual); len(got) != 0 {
		t.Fatalf("subject order alone must not cause drift: %v", got)
	}
}

func TestDesiredStaticViewConsumerFiltersMatchServerContract(t *testing.T) {
	storage := storageconfig.StorageConfig{
		View: storageconfig.StorageView{ConsumerPartitions: []storageconfig.StorageViewConsumerPartition{
			{Durable: events.StorageViewKlineConsumer, Routes: []storageconfig.StorageViewConsumerRoute{{SpaceID: "crypto", DatasetIDs: []string{"dataset_binance_spot_kline_1m"}}}},
			{Durable: events.StorageViewFactorConsumer, Routes: []storageconfig.StorageViewConsumerRoute{{SpaceID: "crypto", DatasetIDs: []string{"dataset_crypto_spot_kline_1m_factor"}}}},
			{Durable: events.StorageViewMetricsConsumer, Routes: []storageconfig.StorageViewConsumerRoute{{SpaceID: "mooxsys", DatasetIDs: []string{"dataset_mooxsys_service_metrics"}}}},
			{Durable: events.StorageViewMiscConsumer, Routes: []storageconfig.StorageViewConsumerRoute{{SpaceID: "stockcn", DatasetIDs: []string{"*"}}}},
		}},
	}

	got, err := desiredStaticViewConsumerFilters(storage)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[events.StorageViewMiscConsumer]; ok {
		t.Fatal("misc wildcard partition must remain owned by the dynamic reconciler")
	}
	for _, want := range []struct {
		consumer string
		space    string
		dataset  string
	}{
		{events.StorageViewKlineConsumer, "crypto", "dataset_binance_spot_kline_1m"},
		{events.StorageViewFactorConsumer, "crypto", "dataset_crypto_spot_kline_1m_factor"},
		{events.StorageViewMetricsConsumer, "mooxsys", "dataset_mooxsys_service_metrics"},
	} {
		registry, err := events.DefaultRegistry()
		if err != nil {
			t.Fatal(err)
		}
		var expected []string
		for _, event := range []events.Event{events.DatasetRowsUpserted, events.DatasetPeriodCollected, events.FactorPeriodComputed, events.DatasetSyncPoint} {
			filter, err := registry.RenderSubject(event, want.space, want.dataset)
			if err != nil {
				t.Fatal(err)
			}
			expected = append(expected, filter)
		}
		if got[want.consumer] == nil || len(got[want.consumer]) != len(expected) {
			t.Fatalf("%s filters = %v, want %v", want.consumer, got[want.consumer], expected)
		}
		for i := range expected {
			if got[want.consumer][i] != expected[i] {
				t.Fatalf("%s filters = %v, want %v", want.consumer, got[want.consumer], expected)
			}
		}
	}
}
