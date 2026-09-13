package storageio

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestBulkLookbackCountsPeriodsPerSubject(t *testing.T) {
	base := time.Unix(600, 0).UTC()
	view := &fakeViewClient{rows: [][]*storagepb.TimeSeriesRow{{
		klineRowFor("A", base, 1), klineRowFor("B", base, 1),
		klineRowFor("A", base.Add(-time.Minute), 1), klineRowFor("A", base.Add(-2*time.Minute), 1),
		klineRowFor("B", base.Add(-3*time.Minute), 1), klineRowFor("B", base.Add(-4*time.Minute), 1),
	}}}
	chunks, err := (&Client{view: view}).ReadPeriodChunks(context.Background(), WindowKey{SourceViewID: "view", Freq: "1m"}, []string{"A", "B", "MISSING"}, base, base.Add(time.Minute), 3, []string{"close"})
	require.NoError(t, err)
	require.Len(t, chunks["A"].Frame.Rows, 3)
	require.Len(t, chunks["B"].Frame.Rows, 3)
	require.Empty(t, chunks["MISSING"].TargetPeriods)
}
