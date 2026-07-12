package view

import (
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestEngineIndexKey(t *testing.T) {
	assert.Equal(t, "duckdb\x00idx-1", engineIndexKey("DuckDB", "idx-1"))
}

func TestSourceColumns_FiltersPrimaryDataset(t *testing.T) {
	columns := []*pb.ViewColumn{
		{OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.open", ColumnName: "open"},
		{OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "other.close", ColumnName: "close"},
		{OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.open", ColumnName: "open"},
	}
	got := sourceColumns("kline", columns)
	assert.Equal(t, []string{"open"}, got)
}

func TestCloneColumns_SkipsNil(t *testing.T) {
	src := []*pb.ViewColumn{{ColumnName: "a"}, nil, {ColumnName: "b"}}
	got := cloneColumns(src)
	require.Len(t, got, 2)
	assert.NotSame(t, src[0], got[0])
	assert.True(t, proto.Equal(src[0], got[0]))
}

func TestParseDurationWindow(t *testing.T) {
	d, ok := parseDurationWindow("30m")
	require.True(t, ok)
	assert.Equal(t, 30*time.Minute, d)

	d, ok = parseDurationWindow("2d")
	require.True(t, ok)
	assert.Equal(t, 48*time.Hour, d)

	_, ok = parseDurationWindow("")
	assert.False(t, ok)
}

func TestParseFrequencyDuration(t *testing.T) {
	d, ok := parseFrequencyDuration("5m")
	require.True(t, ok)
	assert.Equal(t, 5*time.Minute, d)

	d, ok = parseFrequencyDuration("1w")
	require.True(t, ok)
	assert.Equal(t, 7*24*time.Hour, d)

	_, ok = parseFrequencyDuration("bad")
	assert.False(t, ok)
}

func TestParseIndexTime(t *testing.T) {
	got, ok := parseIndexTime("2026-07-11T00:00:00Z")
	require.True(t, ok)
	assert.Equal(t, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), got.UTC())
	_, ok = parseIndexTime("invalid")
	assert.False(t, ok)
}

func TestDurationSeconds(t *testing.T) {
	assert.Equal(t, uint32(90), durationSeconds(90*time.Second))
	assert.Equal(t, uint32(1), durationSeconds(0))
}

func TestMaintenanceViewKey(t *testing.T) {
	key := maintenanceViewKey(&pb.View{SpaceId: "crypto", ViewId: "kline_view"})
	assert.Equal(t, "crypto\x00kline_view", key)
}

func TestNewBuildID(t *testing.T) {
	id, err := newBuildID()
	require.NoError(t, err)
	assert.NotEmpty(t, id)
}
