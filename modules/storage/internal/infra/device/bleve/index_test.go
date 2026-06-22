package bleve_test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/bleve"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestIndexSearchesOnlyIndexedTextColumns(t *testing.T) {
	ctx := context.Background()
	index, err := bleve.Open(bleve.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer index.Close()

	row := recordRow("news-1", "v1", []*pb.ColumnValue{
		testutil.StringValue("title", "APT market rallies"),
		testutil.StringValue("internal_note", "do not index this"),
	})
	err = index.IndexRows(ctx, []*pb.RecordRow{row}, map[string]bool{"title": true})
	require.NoError(t, err)

	got, _, err := index.SearchRecordRows(ctx, bleve.SearchRequest{SpaceID: "crypto", DatasetID: "news", TextQuery: "rallies"})
	require.NoError(t, err)
	require.Len(t, got, 1)

	got, _, err = index.SearchRecordRows(ctx, bleve.SearchRequest{SpaceID: "crypto", DatasetID: "news", TextQuery: "internal"})
	require.NoError(t, err)
	require.Len(t, got, 0)
}

func TestIndexFiltersByRecordIDAndVersionRange(t *testing.T) {
	ctx := context.Background()
	index, err := bleve.Open(bleve.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer index.Close()

	err = index.IndexRows(ctx, []*pb.RecordRow{
		recordRow("news-1", "v1", []*pb.ColumnValue{testutil.StringValue("title", "same token")}),
		recordRow("news-1", "v2", []*pb.ColumnValue{testutil.StringValue("title", "same token")}),
		recordRow("news-2", "v1", []*pb.ColumnValue{testutil.StringValue("title", "same token")}),
	}, map[string]bool{"title": true})
	require.NoError(t, err)

	got, page, err := index.SearchRecordRows(ctx, bleve.SearchRequest{
		SpaceID: "crypto", DatasetID: "news", RecordIDs: []string{"news-1"}, TextQuery: "token",
		VersionRange: &pb.VersionRange{StartVersion: "v2", EndVersion: "v2"},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(1), page.GetTotal())
	require.Len(t, got, 1)
	require.Equal(t, "v2", got[0].GetKey().GetVersion())
}

func TestIndexNormalizesTimeVersionsForRange(t *testing.T) {
	ctx := context.Background()
	index, err := bleve.Open(bleve.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer index.Close()

	err = index.IndexRows(ctx, []*pb.RecordRow{
		recordRow("news-1", "2026-01-01T00:00:00.000000000Z", []*pb.ColumnValue{testutil.StringValue("title", "same token")}),
		recordRow("news-1", "2026-01-01T00:01:00.000000000Z", []*pb.ColumnValue{testutil.StringValue("title", "same token")}),
	}, map[string]bool{"title": true})
	require.NoError(t, err)

	got, page, err := index.SearchRecordRows(ctx, bleve.SearchRequest{
		SpaceID: "crypto", DatasetID: "news", RecordIDs: []string{"news-1"}, TextQuery: "token",
		VersionRange: &pb.VersionRange{StartVersion: "2026-01-01T00:00:00Z", EndVersion: "2026-01-01T00:00:00Z"},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(1), page.GetTotal())
	require.Len(t, got, 1)
	require.Equal(t, "2026-01-01T00:00:00.000000000Z", got[0].GetKey().GetVersion())
}

func recordRow(recordID string, version string, columns []*pb.ColumnValue) *pb.RecordRow {
	return &pb.RecordRow{
		Key:     &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: recordID, Version: version},
		Columns: columns,
	}
}
