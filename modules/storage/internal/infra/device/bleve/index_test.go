package bleve_test

import (
	"context"
	"testing"

	blevelib "github.com/blevesearch/bleve/v2"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/bleve"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestIndexSearchesOnlyIndexedTextColumns(t *testing.T) {
	ctx := context.Background()
	index, err := bleve.Open(bleve.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer index.Close()

	row := &pb.DataRow{
		Key: &pb.DataKey{Scope: &pb.DataScope{SpaceId: "crypto", DatasetId: "news", SubjectId: "coindesk"}, DataTime: "2026-06-15T00:00:00+08:00"},
		Columns: []*pb.ColumnValue{
			testutil.StringValue("title", "APT market rallies"),
			testutil.StringValue("internal_note", "do not index this"),
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
			Columns: []*pb.ColumnValue{testutil.StringValue("title", "same search token")},
		},
		{
			Key: &pb.DataKey{
				Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "news", SubjectId: "coindesk", Dimensions: map[string]string{"a": "b", "c": "d"}},
				DataTime: "2026-06-15T00:00:00+08:00",
				RowId:    "same-row",
			},
			Columns: []*pb.ColumnValue{testutil.StringValue("title", "same search token")},
		},
	}

	err = index.IndexRows(ctx, rows, map[string]bool{"title": true})
	require.NoError(t, err)

	got, page, err := index.SearchRows(ctx, bleve.SearchRequest{SpaceID: "crypto", DatasetID: "news", TextQuery: "token"})
	require.NoError(t, err)
	require.Equal(t, uint64(2), page.GetTotal())
	require.Len(t, got, 2)
}

func TestIndexSearchesLegacyAnalyzedScopeFields(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir()
	row := &pb.DataRow{
		Key: &pb.DataKey{
			Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "news", SubjectId: "APT-USDT"},
			DataTime: "2026-06-15T00:00:00Z",
		},
		Columns: []*pb.ColumnValue{testutil.StringValue("title", "legacy token")},
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(row)
	require.NoError(t, err)
	legacyMapping := blevelib.NewIndexMapping()
	legacyDocMapping := blevelib.NewDocumentMapping()
	rowMapping := blevelib.NewTextFieldMapping()
	rowMapping.Store = true
	rowMapping.Index = false
	legacyDocMapping.AddFieldMappingsAt("_row_json", rowMapping)
	legacyMapping.DefaultMapping = legacyDocMapping
	legacyIndex, err := blevelib.New(path, legacyMapping)
	require.NoError(t, err)
	require.NoError(t, legacyIndex.Index("legacy-row", map[string]any{
		"space_id":   "crypto",
		"dataset_id": "news",
		"subject_id": "APT-USDT",
		"data_time":  "2026-06-15T00:00:00Z",
		"title":      "legacy token",
		"_row_json":  string(raw),
	}))
	require.NoError(t, legacyIndex.Close())

	index, err := bleve.Open(bleve.Options{Path: path})
	require.NoError(t, err)
	defer index.Close()

	got, _, err := index.SearchRows(ctx, bleve.SearchRequest{
		SpaceID:    "crypto",
		DatasetID:  "news",
		SubjectIDs: []string{"APT-USDT"},
		TextQuery:  "legacy",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
}
