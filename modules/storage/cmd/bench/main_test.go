//go:build legacy_storage

package main

import "testing"

func TestBenchmarkIdentifiersFitMetadataLimits(t *testing.T) {
	if len(recordDatasetID) > 30 {
		t.Fatalf("record dataset ID %q has length %d; want <= 30", recordDatasetID, len(recordDatasetID))
	}
	for _, market := range []string{"spot", "swap"} {
		dataset := datasetID(market)
		if len(dataset) > 30 {
			t.Fatalf("dataset ID %q has length %d; want <= 30", dataset, len(dataset))
		}
		view := viewID(market)
		if len(view) > 30 {
			t.Fatalf("view ID %q has length %d; want <= 30", view, len(view))
		}
	}
}

func TestBenchmarkDatasetMarketRoundTrip(t *testing.T) {
	for _, market := range []string{"spot", "swap"} {
		id := datasetID(market)
		got := trimBenchmarkDatasetID(id)
		if got != market {
			t.Fatalf("trimBenchmarkDatasetID(%q) = %q, want %q", id, got, market)
		}
	}
}
