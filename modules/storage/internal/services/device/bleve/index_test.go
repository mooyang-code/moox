package bleve_test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/services/device/bleve"
	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestIndexSearchesOnlyIndexedTextColumns(t *testing.T) {
	ctx := context.Background()
	index, err := bleve.Open(bleve.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer index.Close()

	row := &pb.DataRow{
		Key: &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "news", SubjectId: "coindesk"}, DataTime: "2026-06-15T00:00:00+08:00"},
		Columns: []*pb.ColumnValue{
			quantstore.StringValue("title", "APT market rallies"),
			quantstore.StringValue("internal_note", "do not index this"),
		},
	}

	err = index.IndexRows(ctx, []*pb.DataRow{row}, map[string]bool{"title": true})
	require.NoError(t, err)

	got, _, err := index.SearchRows(ctx, bleve.SearchRequest{SpaceID: "crypto", DatasetID: "news", TextQuery: "rallies"})
	require.NoError(t, err)
	require.Len(t, got, 1)

	got, _, err = index.SearchRows(ctx, bleve.SearchRequest{SpaceID: "crypto", DatasetID: "news", TextQuery: "internal"})
	require.NoError(t, err)
	require.Len(t, got, 0)
}

func TestIndexKeepsRowsWithDifferentDimensionsSeparate(t *testing.T) {
	ctx := context.Background()
	index, err := bleve.Open(bleve.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer index.Close()

	rows := []*pb.DataRow{
		{
			Key: &pb.DataKey{
				Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "news", SubjectId: "coindesk", Dimensions: map[string]string{"a": "b&c=d"}},
				DataTime: "2026-06-15T00:00:00+08:00",
				RowId:    "same-row",
			},
			Columns: []*pb.ColumnValue{quantstore.StringValue("title", "same search token")},
		},
		{
			Key: &pb.DataKey{
				Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "news", SubjectId: "coindesk", Dimensions: map[string]string{"a": "b", "c": "d"}},
				DataTime: "2026-06-15T00:00:00+08:00",
				RowId:    "same-row",
			},
			Columns: []*pb.ColumnValue{quantstore.StringValue("title", "same search token")},
		},
	}

	err = index.IndexRows(ctx, rows, map[string]bool{"title": true})
	require.NoError(t, err)

	got, page, err := index.SearchRows(ctx, bleve.SearchRequest{SpaceID: "crypto", DatasetID: "news", TextQuery: "token"})
	require.NoError(t, err)
	require.Equal(t, uint64(2), page.GetTotal())
	require.Len(t, got, 2)
}
