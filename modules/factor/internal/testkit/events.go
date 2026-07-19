package testkit

import (
	"fmt"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// RowsChangedEvent creates a Storage rows_committed event for a set of subjects.
func RowsChangedEvent(spaceID, datasetID, freq string, barTime time.Time, subjects []string) *storagepb.TimeSeriesRowsCommitted {
	rows := make([]*storagepb.TimeSeriesRow, 0, len(subjects))
	for _, subject := range subjects {
		rows = append(rows, &storagepb.TimeSeriesRow{Key: &storagepb.TimeSeriesKey{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subject, Freq: freq, DataTime: barTime.UTC().Format(time.RFC3339)}})
	}
	writes := make([]*storagepb.TimeSeriesRowWrite, 0, len(rows))
	for _, row := range rows {
		writes = append(writes, &storagepb.TimeSeriesRowWrite{Operation: storagepb.RowWriteOperation_ROW_WRITE_OPERATION_MERGE, Row: row})
	}
	return &storagepb.TimeSeriesRowsCommitted{ShardId: "shard-1", SpaceId: spaceID, DatasetId: datasetID, Writes: writes}
}

// Symbols returns deterministic synthetic symbols.
func Symbols(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("SYM-%03d", i))
	}
	return out
}
