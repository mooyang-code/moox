package view

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFixedViewFilterAcceptsEmptyAndValidJSON(t *testing.T) {
	filter, err := parseFixedViewFilter("")
	require.NoError(t, err)
	assert.Nil(t, filter)

	filter, err = parseFixedViewFilter(`{"subject_id":"BTC","freq":"1m"}`)
	require.NoError(t, err)
	require.NotNil(t, filter)
	assert.Equal(t, "BTC", filter.SubjectID)
}

func TestParseFixedViewFilterRejectsInvalidJSON(t *testing.T) {
	_, err := parseFixedViewFilter("{bad")
	require.Error(t, err)
}

func TestFilterRowsByViewJSONMatchesSubjectAndDimensions(t *testing.T) {
	view := &pb.View{FilterJson: `{"subject_id":"BTC","dimensions":{"venue":"binance"}}`}
	rows := []*pb.TimeSeriesRow{
		{Key: &pb.TimeSeriesKey{SubjectId: "BTC", Freq: "1m", Dimensions: map[string]string{"venue": "binance"}}},
		{Key: &pb.TimeSeriesKey{SubjectId: "ETH", Freq: "1m"}},
	}
	filtered, err := filterRowsByViewJSON(view, rows)
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, "BTC", filtered[0].GetKey().GetSubjectId())
}

func TestFilteredTimeSeriesRowsForViewAppliesFilterBeforeRowMapper(t *testing.T) {
	view := &pb.View{
		SpaceId: "crypto", ViewId: "combined", PrimaryDatasetId: "primary",
		FilterJson: `{"subject_id":"BTC"}`,
	}
	columns := []*pb.ViewColumn{projectionViewColumn("close", "primary.close")}
	rows := []*pb.TimeSeriesRow{
		projectionTimeRow("primary", "BTC", "close"),
		projectionTimeRow("primary", "ETH", "close"),
	}
	projected, ok, err := FilteredTimeSeriesRowsForView(context.Background(), view, columns, rows, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, projected, 1)
	assert.Equal(t, "BTC", projected[0].GetKey().GetSubjectId())
}
