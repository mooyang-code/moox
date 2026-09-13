package storageio

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

func TestFullCacheWindowUsesFencedResponseSchema(t *testing.T) {
	base := time.Unix(600, 0).UTC()
	view := &windowRPCStub{rsp: &pb.QueryTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{}, ServedActiveIndexId: "idx", ServedInputContractVersion: "hash:1", Columns: cacheTestColumns()}}
	c := &Client{view: view}
	key := WindowKey{SpaceID: "s", SourceViewID: "v", SubjectID: "BTC", Freq: "1m", FilterSourceSeriesTag: true, ExpectedActiveIndexID: "idx", InputContractVersion: "hash:1"}
	span := &pb.TimeRange{EndTime: base.Add(time.Minute).Format(time.RFC3339Nano)}
	window, err := c.readFullCacheWindow(context.Background(), key, span, 20)
	require.NoError(t, err)
	require.Len(t, window.Columns, 5)
	require.Empty(t, window.Rows)
	require.Len(t, view.reqs, 1)
	require.Empty(t, view.reqs[0].ColumnNames)
	require.Nil(t, view.reqs[0].Page)
	require.EqualValues(t, 20, view.reqs[0].RowsPerSeries)
	view.rsp.Rows = []*pb.TimeSeriesRow{{Key: &pb.TimeSeriesKey{SubjectId: "BTC", Freq: "1m", DataTime: base.Format(time.RFC3339Nano)}, Fields: []*pb.FieldValue{{FieldId: "count", Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: 5}}}}}}
	window, err = c.readFullCacheWindow(context.Background(), key, span, 20)
	require.NoError(t, err)
	require.Equal(t, int64(5), window.Rows[0][4])
	view.rsp.ServedInputContractVersion = "hash:2"
	_, err = c.readFullCacheWindow(context.Background(), key, span, 20)
	require.ErrorContains(t, err, "contract changed")
	view.rsp.ServedInputContractVersion = "hash:1"
	view.rsp.Rows[0].Key.SubjectId = "ETH"
	_, err = c.readFullCacheWindow(context.Background(), key, span, 20)
	require.ErrorContains(t, err, "scope")
	view.rsp.Rows = nil
	view.rsp.Columns = nil
	_, err = c.readFullCacheWindow(context.Background(), key, span, 20)
	require.Error(t, err, "missing schema is not a cacheable empty result")
	view.rsp.Columns = cacheTestColumns()
	view.rsp.ServedInputContractVersion = ""
	key.InputContractVersion = ""
	calls := len(view.reqs)
	_, err = c.readFullCacheWindow(context.Background(), key, span, 20)
	require.Error(t, err, "unfenced reads must not populate cache")
	require.Len(t, view.reqs, calls)
}

func TestFullCacheWindowsBatchByAuthoritativeSchema(t *testing.T) {
	columns := cacheTestColumns()
	for i := 0; i < 45; i++ {
		columns = append(columns, &pb.ResultColumn{ColumnName: fmt.Sprintf("f%d", i), ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	}
	schema, _, err := decodeCacheRows(columns, nil)
	require.NoError(t, err)
	view := &windowRPCStub{rsp: &pb.QueryTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{}, ServedActiveIndexId: "idx", ServedInputContractVersion: "hash:1", Columns: columns}}
	c := &Client{view: view}
	key := WindowKey{SpaceID: "s", SourceViewID: "v", SubjectIDs: []string{"ETH", "BTC", "BTC"}, Freq: "1m", FilterSourceSeriesTag: true, ExpectedActiveIndexID: "idx", InputContractVersion: "hash:1"}
	span := &pb.TimeRange{EndTime: "2026-09-13T00:00:00Z"}
	delivered := 0
	consume := func(window fullCacheWindow) error {
		delivered++
		require.Len(t, window.Subjects, 1)
		require.Equal(t, []string{"BTC", "ETH"}[delivered-1], window.Subjects[0])
		require.Equal(t, schema, window.Columns)
		return nil
	}
	err = c.readFullCacheWindows(context.Background(), key, span, 10000, schema, consume)
	require.NoError(t, err)
	require.Equal(t, 2, delivered)
	require.Len(t, view.reqs, 2)
	for _, req := range view.reqs {
		require.Len(t, req.Selectors, 1)
		require.EqualValues(t, 10000, req.RowsPerSeries)
		require.Nil(t, req.Page)
	}
	err = c.readFullCacheWindows(context.Background(), key, span, 10000, append(schema, inputcache.Column{Name: "too_wide", Type: "DOUBLE"}), consume)
	require.ErrorContains(t, err, "budget")
	require.Len(t, view.reqs, 2)
	view.rsp.Columns = cacheTestColumns()
	err = c.readFullCacheWindows(context.Background(), key, span, 10000, schema, consume)
	require.ErrorContains(t, err, "schema")
	require.Equal(t, 2, delivered)
	view.rsp.Columns = columns
	before := len(view.reqs)
	stop := errors.New("stop batch")
	err = c.readFullCacheWindows(context.Background(), key, span, 10000, schema, func(fullCacheWindow) error { return stop })
	require.ErrorIs(t, err, stop)
	require.Len(t, view.reqs, before+1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = c.readFullCacheWindows(ctx, key, span, 10000, schema, consume)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, view.reqs, before+1)
}

func TestFullCacheWindowsDiscoversSchemaWithoutDeliveringProbeAsHistory(t *testing.T) {
	columns := cacheTestColumns()
	for i := 0; i < 45; i++ {
		columns = append(columns, &pb.ResultColumn{ColumnName: fmt.Sprintf("f%d", i), ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	}
	view := &windowRPCStub{rsp: &pb.QueryTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{}, ServedActiveIndexId: "idx", ServedInputContractVersion: "hash:1", Columns: columns}}
	c := &Client{view: view}
	key := WindowKey{SpaceID: "s", SourceViewID: "v", SubjectIDs: []string{"BTC", "ETH"}, Freq: "1m", FilterSourceSeriesTag: true, ExpectedActiveIndexID: "idx", InputContractVersion: "hash:1"}
	span := &pb.TimeRange{EndTime: "2026-09-13T00:00:00Z"}
	delivered := 0
	err := c.readFullCacheWindows(context.Background(), key, span, 10000, nil, func(window fullCacheWindow) error {
		delivered++
		require.Len(t, window.Columns, 50)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, delivered)
	require.Len(t, view.reqs, 3)
	require.EqualValues(t, 1, view.reqs[0].RowsPerSeries)
	require.Len(t, view.reqs[0].Selectors, 1)
	for _, req := range view.reqs[1:] {
		require.EqualValues(t, 10000, req.RowsPerSeries)
		require.Nil(t, req.Page)
	}
	view.reqs = nil
	delivered = 0
	err = c.readFullCacheWindows(context.Background(), key, span, 10000, nil, func(fullCacheWindow) error {
		delivered++
		view.rsp.Columns = cacheTestColumns()
		return nil
	})
	require.ErrorContains(t, err, "schema")
	require.Equal(t, 1, delivered, "a later invalid batch must not be delivered or reported as success")
	require.Len(t, view.reqs, 3)
	view.reqs = nil
	view.rsp.ServedInputContractVersion = "hash:2"
	err = c.readFullCacheWindows(context.Background(), key, span, 10000, nil, func(fullCacheWindow) error {
		t.Fatal("failed schema probe must not deliver data")
		return nil
	})
	require.ErrorContains(t, err, "contract changed")
	require.Len(t, view.reqs, 1)
}
