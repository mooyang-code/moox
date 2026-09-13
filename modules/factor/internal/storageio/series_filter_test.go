package storageio

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestPeriodReadFiltersExplicitSourceSeriesIncludingEmptyTag(t *testing.T) {
	for _, tag := range []string{"venue:binance", ""} {
		t.Run(tag, func(t *testing.T) {
			view := &fakeViewClient{}
			client := &Client{view: view}
			base := time.Unix(60, 0)
			_, err := client.ReadPeriodChunks(context.Background(), WindowKey{
				SpaceID: "space", SourceViewID: "prices", Freq: "1m", SourceSeriesTag: tag, FilterSourceSeriesTag: true,
			}, []string{"BTC"}, base, base.Add(time.Minute), 2, []string{"close"})
			require.NoError(t, err)
			require.NotEmpty(t, view.reqs)
			for _, req := range view.reqs {
				found := false
				for _, cond := range req.GetFilter().GetGroups()[0].GetConds() {
					if cond.GetColumn() == "series_tag" {
						found = true
						require.Equal(t, storagepb.FilterOp_FILTER_OP_EQ, cond.GetOp())
						require.Len(t, cond.GetValues(), 1)
						require.Equal(t, tag, cond.GetValues()[0].GetStringValue())
					}
				}
				require.True(t, found)
			}
		})
	}
}
