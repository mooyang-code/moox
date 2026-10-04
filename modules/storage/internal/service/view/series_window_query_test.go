package view

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestFullSeriesWindowRejectsUnrepresentableSchema(t *testing.T) {
	valid := &pb.ViewColumn{ColumnName: "bars.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}
	for _, columns := range [][]*pb.ViewColumn{
		{{ColumnName: "bad-name", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
		{{ColumnName: "unknown", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED}},
		{valid, valid},
	} {
		_, err := fullSeriesWindowColumns(columns)
		require.Error(t, err)
	}
}

func TestSeriesWindowBoundsCellsBeforeQuery(t *testing.T) {
	e := &queryEngine{}
	s, auth := queryTestService(e, true)
	s.engines["duckdb"], s.indexEngine["prices-index"] = e, "duckdb"
	schema := s.schemas["prices-index"]
	schema.SchemaHash, schema.ViewVersion = "hash", 1
	schema.Columns = nil
	for i := range 6 {
		schema.Columns = append(schema.Columns, &pb.ViewColumn{ColumnName: fmt.Sprintf("value%d", i), ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	}
	s.schemas["prices-index"] = schema
	tag := ""
	req := &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, SpaceId: "space", ViewId: "prices", RowsPerSeries: 10000, TotalMode: pb.TotalMode_NONE, ExpectedActiveIndexId: "prices-index", ExpectedInputContractVersion: "hash:1", TimeRange: &pb.TimeRange{EndTime: "2026-07-29T00:01:00Z"}}
	for i := range 5 {
		req.Selectors = append(req.Selectors, &pb.TimeSeriesSelector{SubjectId: fmt.Sprintf("subject%d", i), Freq: "1m", SeriesTag: &tag})
	}
	rsp, err := s.QueryTimeSeriesRows(context.Background(), req)
	require.NoError(t, err)
	require.Zero(t, rsp.GetRetInfo().GetCode())
	require.Equal(t, 1, e.calls)
	schema.Columns = append(schema.Columns, &pb.ViewColumn{ColumnName: "extra", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	s.schemas["prices-index"] = schema
	rsp, err = s.QueryTimeSeriesRows(context.Background(), req)
	require.NoError(t, err)
	require.EqualValues(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Equal(t, 1, e.calls, "wide window must reject before allocating result cells")
	for _, column := range schema.Columns {
		req.ColumnNames = append(req.ColumnNames, column.ColumnName)
	}
	rsp, err = s.QueryTimeSeriesRows(context.Background(), req)
	require.NoError(t, err)
	require.EqualValues(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Equal(t, 1, e.calls, "explicit projection must obey the same cell budget")
}

type blockingWindowEngine struct {
	*queryEngine
	entered chan struct{}
	release chan struct{}
}

func (e *blockingWindowEngine) Query(ctx context.Context, _ string, _ viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	close(e.entered)
	select {
	case <-e.release:
		return nil, 0, nil
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
}

func TestSeriesWindowRPCHoldsPhysicalGenerationUntilQueryCompletes(t *testing.T) {
	e := &blockingWindowEngine{queryEngine: &queryEngine{}, entered: make(chan struct{}), release: make(chan struct{})}
	s, auth := queryTestService(e, true)
	s.engines["duckdb"], s.indexEngine["prices-index"] = e, "duckdb"
	schema := s.schemas["prices-index"]
	schema.SchemaHash, schema.ViewVersion = "hash", 1
	s.schemas["prices-index"] = schema
	tag := ""
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan *pb.QueryTimeSeriesRowsRsp, 1)
	go func() {
		rsp, _ := s.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, SpaceId: "space", ViewId: "prices", RowsPerSeries: 20, TotalMode: pb.TotalMode_NONE, ExpectedActiveIndexId: "prices-index", ExpectedInputContractVersion: "hash:1", TimeRange: &pb.TimeRange{EndTime: "2026-07-29T00:01:00Z"}, Selectors: []*pb.TimeSeriesSelector{{SubjectId: "BTC", Freq: "1m", SeriesTag: &tag}}})
		done <- rsp
	}()
	select {
	case <-e.entered:
	case <-ctx.Done():
		t.Fatal("query did not start")
	}
	runtime := s.views[viewRef{spaceID: "space", viewID: "prices"}]
	if runtime.mu.TryLock() {
		runtime.mu.Unlock()
		t.Fatal("active generation can switch during window query")
	}
	gateCtx, gateCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer gateCancel()
	release, err := s.indexWriteGate("prices-index").lock(gateCtx)
	if release != nil {
		release()
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
	close(e.release)
	select {
	case rsp := <-done:
		require.NotNil(t, rsp)
		require.Zero(t, rsp.GetRetInfo().GetCode())
	case <-ctx.Done():
		t.Fatal("query did not finish")
	}
	release, err = s.indexWriteGate("prices-index").lock(ctx)
	require.NoError(t, err)
	release()
	require.True(t, runtime.mu.TryLock())
	runtime.mu.Unlock()
}

func TestSeriesWindowReportsCurrentGenerationWhenExpectedIndexRetired(t *testing.T) {
	e := &queryEngine{}
	s, auth := queryTestService(e, true)
	s.engines["duckdb"], s.indexEngine["prices-index"] = e, "duckdb"
	schema := s.schemas["prices-index"]
	schema.SchemaHash, schema.ViewVersion = "hash", 1
	s.schemas["prices-index"] = schema
	tag := ""
	req := &pb.QueryTimeSeriesRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "prices", RowsPerSeries: 20, TotalMode: pb.TotalMode_NONE,
		ExpectedActiveIndexId: "retired-b", ExpectedInputContractVersion: "hash:1",
		TimeRange: &pb.TimeRange{EndTime: "2026-07-29T00:01:00Z"},
		Selectors: []*pb.TimeSeriesSelector{{SubjectId: "BTC", Freq: "1m", SeriesTag: &tag}},
	}
	rsp, err := s.QueryTimeSeriesRows(context.Background(), req)
	require.NoError(t, err)
	require.Zero(t, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().String())
	require.Equal(t, 1, e.calls)
	require.Equal(t, "hash:1", rsp.GetServedInputContractVersion())
	req.ExpectedActiveIndexId = ""
	rsp, err = s.QueryTimeSeriesRows(context.Background(), req)
	require.NoError(t, err)
	require.Zero(t, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().String())
	require.Equal(t, 2, e.calls)
}

func TestSeriesWindowRPCFencesContractWithoutGlobalRevisionProbe(t *testing.T) {
	e := &queryEngine{statErr: errors.New("global revision must not be probed")}
	s, auth := queryTestService(e, true)
	s.engines["duckdb"], s.indexEngine["prices-index"] = e, "duckdb"
	schema := s.schemas["prices-index"]
	schema.SchemaHash, schema.ViewVersion = "hash", 1
	s.schemas["prices-index"] = schema
	tag := ""
	req := &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, SpaceId: "space", ViewId: "prices", RowsPerSeries: 20, TotalMode: pb.TotalMode_NONE, ExpectedActiveIndexId: "prices-index", ExpectedInputContractVersion: "hash:1", TimeRange: &pb.TimeRange{EndTime: "2026-07-29T00:01:00Z"}, Selectors: []*pb.TimeSeriesSelector{{SubjectId: "BTC", Freq: "1m", SeriesTag: &tag}}}
	rsp, err := s.QueryTimeSeriesRows(context.Background(), req)
	require.NoError(t, err)
	require.Zero(t, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().String())
	require.Equal(t, "hash:1", rsp.GetServedInputContractVersion())
	require.Equal(t, "prices-index", rsp.GetServedActiveIndexId())
	require.Equal(t, 20, e.spec.RowsPerSeries)
	require.Zero(t, e.spec.Limit)
	require.Zero(t, e.statCalls)
	require.Equal(t, "market", e.spec.Selectors[0].DatasetID)
	req.ExpectedInputContractVersion = "hash:2"
	rsp, err = s.QueryTimeSeriesRows(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_VIEW_NOT_READY, rsp.GetRetInfo().GetCode())
	require.Equal(t, 1, e.calls)
	req.ExpectedInputContractVersion = "hash:1"
	s.preparingIndexes = make(map[string]uint64)
	s.markIndexPreparing("prices-index", 42)
	rsp, err = s.QueryTimeSeriesRows(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_VIEW_NOT_READY, rsp.GetRetInfo().GetCode())
	require.Equal(t, 1, e.calls, "preparing physical file must not be served with the previous contract")
	s.clearIndexPreparing("prices-index", 42)
	for _, mutate := range []func(*pb.QueryTimeSeriesRowsReq){
		func(r *pb.QueryTimeSeriesRowsReq) { r.Limit = 1 },
		func(r *pb.QueryTimeSeriesRowsReq) { r.RowsPerSeries = 10001 },
		func(r *pb.QueryTimeSeriesRowsReq) { r.ExpectedActiveIndexRevision = 1 },
		func(r *pb.QueryTimeSeriesRowsReq) { r.ExpectedInputContractVersion = "" },
		func(r *pb.QueryTimeSeriesRowsReq) { r.TimeRange.EndTime = "bad" },
		func(r *pb.QueryTimeSeriesRowsReq) { r.Selectors = nil },
		func(r *pb.QueryTimeSeriesRowsReq) { r.Selectors = []*pb.TimeSeriesSelector{nil} },
		func(r *pb.QueryTimeSeriesRowsReq) { r.Selectors[0].SeriesTag = nil },
		func(r *pb.QueryTimeSeriesRowsReq) { r.Selectors[0].DatasetId = "other" },
	} {
		invalid := proto.Clone(req).(*pb.QueryTimeSeriesRowsReq)
		mutate(invalid)
		rsp, err := s.QueryTimeSeriesRows(context.Background(), invalid)
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	}
	require.Equal(t, 1, e.calls, "invalid requests must not execute SQL")
}
