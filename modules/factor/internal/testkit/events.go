package testkit

import (
	"fmt"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// RowsChangedEvent creates a Storage rows_changed event for a set of subjects.
func RowsChangedEvent(spaceID, datasetID, freq string, barTime time.Time, subjects []string) *storagepb.TimeSeriesRowsChangedEvent {
	keys := make([]*storagepb.TimeSeriesKey, 0, len(subjects))
	for _, subject := range subjects {
		keys = append(keys, &storagepb.TimeSeriesKey{
			SpaceId:   spaceID,
			DatasetId: datasetID,
			SubjectId: subject,
			Freq:      freq,
			DataTime:  barTime.UTC().Format(time.RFC3339),
		})
	}
	return &storagepb.TimeSeriesRowsChangedEvent{Keys: keys}
}

// Symbols returns deterministic synthetic symbols.
func Symbols(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("SYM-%03d", i))
	}
	return out
}
