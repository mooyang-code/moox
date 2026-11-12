package primarystore

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// rangeStore 记录区间读取、计数与变更日志收到的请求。
type rangeStore struct {
	*hookStore
	read    *pb.ReadTimeSeriesRowsReq
	count   *pb.CountTimeSeriesRowsReq
	changes *pb.ReadDatasetChangesReq
}

func newRangeStore(t *testing.T) *rangeStore {
	s := &rangeStore{hookStore: newHookStore(t)}
	s.readChanges = func(_ context.Context, req *pb.ReadDatasetChangesReq) (*pb.ReadDatasetChangesRsp, error) {
		s.changes = req
		return &pb.ReadDatasetChangesRsp{NextCursor: &pb.DatasetChangeCursor{StoreId: "s", Sequence: 7}}, nil
	}
	s.readSeries = func(_ context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
		s.read = req
		return &pb.ReadTimeSeriesRowsRsp{PageResult: &pb.PageResult{NextCursor: "next"}}, nil
	}
	s.countSeries = func(_ context.Context, req *pb.CountTimeSeriesRowsReq) ([]*pb.SubjectRowCount, error) {
		s.count = req
		return []*pb.SubjectRowCount{{SubjectId: "BTC-USDT", Rows: 60}}, nil
	}
	return s
}

type freqSnapshot struct{ activeSnapshot }

func (freqSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	return &pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, Freq: "1m", Status: "active"}, true
}

func newRangeService(t *testing.T, store *rangeStore) *Service {
	t.Helper()
	svc, err := New(Options{
		Store:      store,
		Authorizer: func(*pb.AuthInfo) error { return nil },
		Snapshot:   func() metadata.RequestSnapshot { return freqSnapshot{} },
	})
	require.NoError(t, err)
	return svc
}

func validRangeReq(appID string) *pb.ReadTimeSeriesRowsReq {
	empty := ""
	return &pb.ReadTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: appID, AppKey: "key"}, SpaceId: "space", DatasetId: "market",
		Selectors:   []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", SeriesTag: &empty}},
		ColumnNames: []string{"close"},
		TimeRange:   &pb.TimeRange{StartTime: "2026-10-04T00:00:00Z", EndTime: "2026-10-04T01:00:00Z"},
		Page:        &pb.Page{Size: 2000, Cursor: "abc"},
	}
}

func TestEveryCallerRangeReadGoesToTheStore(t *testing.T) {
	for _, appID := range []string{"factor", "moox-factor", "storage-view", "collector", "moox-cli", "archive", "someone-else"} {
		node := newRangeStore(t)
		rsp, err := newRangeService(t, node).ReadTimeSeriesRows(context.Background(), validRangeReq(appID))
		require.NoError(t, err, appID)
		require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), appID)
		require.NotNil(t, node.read, appID)
		require.Equal(t, "abc", node.read.GetPage().GetCursor(), "游标原样转发")
		require.Equal(t, "next", rsp.GetPageResult().GetNextCursor())
		require.NotNil(t, node.read.GetSelectors()[0].SeriesTag, "精确的默认序列选择不能丢")
	}
}

func TestRangeReadRejectsInvalidRequestsBeforeTheStore(t *testing.T) {
	selector := func(freq string) []*pb.TimeSeriesSelector {
		return []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "S", Freq: freq}}
	}
	many := make([]*pb.TimeSeriesSelector, 101)
	for i := range many {
		many[i] = selector("1m")[0]
	}
	cases := map[string]func(*pb.ReadTimeSeriesRowsReq){
		"no columns":           func(r *pb.ReadTimeSeriesRowsReq) { r.ColumnNames = nil },
		"no range":             func(r *pb.ReadTimeSeriesRowsReq) { r.TimeRange = nil },
		"half open range":      func(r *pb.ReadTimeSeriesRowsReq) { r.TimeRange.EndTime = "" },
		"inverted range":       func(r *pb.ReadTimeSeriesRowsReq) { r.TimeRange.StartTime = "2026-10-05T00:00:00Z" },
		"both shapes":          func(r *pb.ReadTimeSeriesRowsReq) { r.LatestPerSeries = 5 },
		"page number":          func(r *pb.ReadTimeSeriesRowsReq) { r.Page = &pb.Page{Page: 2, Size: 10} },
		"range page too big":   func(r *pb.ReadTimeSeriesRowsReq) { r.Page.Size = 2001 },
		"too many selectors":   func(r *pb.ReadTimeSeriesRowsReq) { r.Selectors = many },
		"selector no subject":  func(r *pb.ReadTimeSeriesRowsReq) { r.Selectors = []*pb.TimeSeriesSelector{{Freq: "1m"}} },
		"selector other scope": func(r *pb.ReadTimeSeriesRowsReq) { r.Selectors[0].DatasetId = "other" },
		"selector wrong freq":  func(r *pb.ReadTimeSeriesRowsReq) { r.Selectors = selector("5m") },
		"latest too big":       func(r *pb.ReadTimeSeriesRowsReq) { r.TimeRange, r.LatestPerSeries = nil, 10001 },
		"latest page too big": func(r *pb.ReadTimeSeriesRowsReq) {
			r.TimeRange, r.LatestPerSeries, r.Page = nil, 5, &pb.Page{Size: 20001}
		},
	}
	for name, mutate := range cases {
		node := newRangeStore(t)
		req := validRangeReq("caller")
		mutate(req)
		rsp, err := newRangeService(t, node).ReadTimeSeriesRows(context.Background(), req)
		require.NoError(t, err, name)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), name)
		require.Nil(t, node.read, "%s: 非法请求不能到达存储层", name)
	}
	// 不带 selector 是合法的（整个数据集），最近 N 根的页大小上限更大。
	node := newRangeStore(t)
	req := validRangeReq("caller")
	req.Selectors, req.TimeRange, req.LatestPerSeries, req.Page = nil, nil, 5, &pb.Page{Size: 20000}
	rsp, err := newRangeService(t, node).ReadTimeSeriesRows(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.NotNil(t, node.read)
}

func TestRangeReadRequiresCallerAuthorization(t *testing.T) {
	node := newRangeStore(t)
	svc, err := New(Options{Store: node, Authorizer: func(*pb.AuthInfo) error { return errors.New("denied") }})
	require.NoError(t, err)
	rsp, err := svc.ReadTimeSeriesRows(context.Background(), validRangeReq("caller"))
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, rsp.GetRetInfo().GetCode())
	count, err := svc.CountTimeSeriesRows(context.Background(), &pb.CountTimeSeriesRowsReq{SpaceId: "space", DatasetId: "market", Selectors: validRangeReq("x").GetSelectors(), TimeRange: validRangeReq("x").GetTimeRange()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, count.GetRetInfo().GetCode())
	require.Nil(t, node.read)
	require.Nil(t, node.count)
}

func TestCountTimeSeriesRowsReachesTheStore(t *testing.T) {
	node := newRangeStore(t)
	svc := newRangeService(t, node)
	req := &pb.CountTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"}, SpaceId: "space", DatasetId: "market",
		Selectors: validRangeReq("x").GetSelectors(), TimeRange: validRangeReq("x").GetTimeRange(),
	}
	rsp, err := svc.CountTimeSeriesRows(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, uint32(60), rsp.GetCounts()[0].GetRows())
	require.Equal(t, req.GetTimeRange().GetEndTime(), node.count.GetTimeRange().GetEndTime())
	invalid, err := svc.CountTimeSeriesRows(context.Background(), &pb.CountTimeSeriesRowsReq{AuthInfo: req.GetAuthInfo(), SpaceId: "space", DatasetId: "market"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, invalid.GetRetInfo().GetCode())
}

func TestReadRowsOmitsMissingRowsAndKeepsRequestOrder(t *testing.T) {
	var asked []*pb.RowKey
	store := newHookStore(t)
	store.readRows = func(_ context.Context, keys []*pb.RowKey, _, _ []string) ([]*pb.RowFieldValues, error) {
		asked = keys
		// 存储层只返回存在的行（第二个键不存在）。
		return []*pb.RowFieldValues{{Key: keys[0]}, {Key: keys[2]}}, nil
	}
	svc, err := New(Options{Store: store, Snapshot: activeRequestSnapshot})
	require.NoError(t, err)
	key := func(subject string) *pb.RowKey {
		return timeSeriesRowKey(&pb.TimeSeriesKey{SpaceId: "space", DatasetId: "dataset", SubjectId: subject, Freq: "1m", DataTime: "2026-07-29T15:00:00Z"})
	}
	rsp, err := svc.ReadRows(context.Background(), &pb.PrimaryReadRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"}, Keys: []*pb.RowKey{key("A"), key("missing"), key("B")}, ColumnNames: []string{"close"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, asked, 3)
	require.Len(t, rsp.GetRows(), 2)
	require.Equal(t, "A", rsp.GetRows()[0].GetKey().GetTimeSeries().GetSubjectId())
	require.Equal(t, "B", rsp.GetRows()[1].GetKey().GetTimeSeries().GetSubjectId())

	tooMany := make([]*pb.RowKey, maxReadRowsKeys+1)
	for i := range tooMany {
		tooMany[i] = key("A")
	}
	for name, req := range map[string]*pb.PrimaryReadRowsReq{
		"no keys":    {AuthInfo: &pb.AuthInfo{AppId: "c"}, ColumnNames: []string{"close"}},
		"no columns": {AuthInfo: &pb.AuthInfo{AppId: "c"}, Keys: []*pb.RowKey{key("A")}},
		"too many":   {AuthInfo: &pb.AuthInfo{AppId: "c"}, Keys: tooMany, ColumnNames: []string{"close"}},
	} {
		bad, err := svc.ReadRows(context.Background(), req)
		require.NoError(t, err, name)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, bad.GetRetInfo().GetCode(), name)
	}
}

func TestReadDatasetChangesReachesTheStore(t *testing.T) {
	node := newRangeStore(t)
	svc := newRangeService(t, node)
	cursor := &pb.DatasetChangeCursor{StoreId: "s", Sequence: 3}
	rsp, err := svc.ReadDatasetChanges(context.Background(), &pb.ReadDatasetChangesReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor", AppKey: "key"}, SpaceId: "space", DatasetId: "market", Cursor: cursor, Limit: 100,
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, uint64(7), rsp.GetNextCursor().GetSequence())
	require.NotNil(t, node.changes)
	require.Equal(t, cursor.GetSequence(), node.changes.GetCursor().GetSequence())
	require.Equal(t, "market", node.changes.GetDatasetId())

	// 取基线位点：不需要游标。
	node.changes = nil
	rsp, err = svc.ReadDatasetChanges(context.Background(), &pb.ReadDatasetChangesReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor", AppKey: "key"}, SpaceId: "space", DatasetId: "market", FromHead: true,
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.True(t, node.changes.GetFromHead())

	// 缺游标又不是取基线：参数错误，不会走到存储层。
	node.changes = nil
	rsp, err = svc.ReadDatasetChanges(context.Background(), &pb.ReadDatasetChangesReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor", AppKey: "key"}, SpaceId: "space", DatasetId: "market",
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Nil(t, node.changes)
}
