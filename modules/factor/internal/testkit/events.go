package testkit

import (
	"fmt"
	"time"

	storagepb "github.com/mooyang-code/moox/packages/storagepb"
)

// RowsChangedEvent creates a Dataset field-change event for a set of subjects.
func RowsChangedEvent(spaceID, datasetID, freq string, barTime time.Time, subjects []string) *storagepb.DatasetRowsUpserted {
	rows := make([]*storagepb.RowUpsert, 0, len(subjects))
	for _, subject := range subjects {
		rows = append(rows, &storagepb.RowUpsert{Key: &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: subject, Freq: freq, DataTime: barTime.UTC().Format(time.RFC3339)}}}})
	}
	return &storagepb.DatasetRowsUpserted{SpaceId: spaceID, DatasetId: datasetID, Rows: rows}
}

// Symbols returns deterministic synthetic symbols.
func Symbols(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("SYM-%03d", i))
	}
	return out
}
