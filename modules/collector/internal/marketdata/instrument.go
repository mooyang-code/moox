package marketdata

import (
	"fmt"
	"strings"
	"time"
)

func ValidateInstrumentSnapshot(snapshot InstrumentSnapshot) error {
	if strings.TrimSpace(snapshot.SnapshotID) == "" {
		return fmt.Errorf("snapshot_id is required")
	}
	if strings.TrimSpace(snapshot.SourceProvider) == "" {
		return fmt.Errorf("source_provider is required")
	}
	if strings.TrimSpace(snapshot.MarketID) == "" {
		return fmt.Errorf("market_id is required")
	}
	if !isUTCTime(snapshot.FetchedAt) {
		return fmt.Errorf("fetched_at must be UTC")
	}
	if !snapshot.Complete {
		return fmt.Errorf("snapshot must be complete")
	}
	if snapshot.PageCount <= 0 {
		return fmt.Errorf("page_count must be positive")
	}
	if len(snapshot.Instruments) == 0 || len(snapshot.ExchangeCounts) == 0 {
		return fmt.Errorf("instruments and exchange_counts are required")
	}
	seen := make(map[string]struct{}, len(snapshot.Instruments))
	actualCounts := make(map[string]int, len(snapshot.ExchangeCounts))
	for _, instrument := range snapshot.Instruments {
		if strings.TrimSpace(instrument.SubjectID) == "" || strings.TrimSpace(instrument.ProviderSymbol) == "" {
			return fmt.Errorf("instrument subject_id and provider_symbol are required")
		}
		if strings.TrimSpace(instrument.Exchange) == "" {
			return fmt.Errorf("instrument exchange is required")
		}
		if _, ok := seen[instrument.SubjectID]; ok {
			return fmt.Errorf("duplicate instrument subject_id %q", instrument.SubjectID)
		}
		seen[instrument.SubjectID] = struct{}{}
		actualCounts[instrument.Exchange]++
	}
	if len(actualCounts) != len(snapshot.ExchangeCounts) {
		return fmt.Errorf("exchange counts mismatch")
	}
	for exchange, want := range snapshot.ExchangeCounts {
		if actualCounts[exchange] != want {
			return fmt.Errorf("exchange count mismatch for %s", exchange)
		}
	}
	return nil
}

func SnapshotID(providerID, marketID string, fetchedAt time.Time) string {
	return strings.TrimSpace(providerID) + ":" + strings.TrimSpace(marketID) + ":" + fetchedAt.UTC().Format(time.RFC3339Nano)
}
