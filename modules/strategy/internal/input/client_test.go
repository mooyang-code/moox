package input

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
)

// dataViewStub 按页返回预设响应。
type dataViewStub struct {
	storagepb.DataViewClientProxy
	pages    []*storagepb.QueryTimeSeriesRowsRsp
	requests []*storagepb.QueryTimeSeriesRowsReq
}

func (s *dataViewStub) QueryTimeSeriesRows(_ context.Context, req *storagepb.QueryTimeSeriesRowsReq, _ ...client.Option) (*storagepb.QueryTimeSeriesRowsRsp, error) {
	s.requests = append(s.requests, req)
	index := int(req.GetPage().GetPage()) - 1
	if index < 0 || index >= len(s.pages) {
		return nil, errors.New("页码越界")
	}
	return s.pages[index], nil
}

func page(hasMore bool, revision uint64, indexID string, rows ...*storagepb.TimeSeriesRow) *storagepb.QueryTimeSeriesRowsRsp {
	return &storagepb.QueryTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Rows: rows, PageResult: &commonpb.PageResult{HasMore: hasMore}, ServedActiveIndexRevision: revision, ServedActiveIndexId: indexID}
}

func tsRow(subjectID string, at time.Time, value *storagepb.TypedValue) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{Key: &storagepb.TimeSeriesKey{SubjectId: subjectID, DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: "venue:binance"}, Fields: []*storagepb.FieldValue{{FieldId: "close", Value: value}}}
}

func double(value float64) *storagepb.TypedValue {
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}}
}

func baseQuery() Query {
	return Query{ViewID: "view", DatasetID: "ds", Frequency: "1h", Subjects: []Subject{subject("BTC-USDT", true), subject("ETH-USDT", true)}, Start: barStart, End: barStart.Add(time.Nanosecond), Columns: []string{"close"}, ExpectedIndexID: "idx_a"}
}

// S11：分页读取中 revision 或 index 变化返回 ErrStale，调用方丢弃已读页面整体重读。
func TestQueryRowsDetectsRevisionAndIndexChanges(t *testing.T) {
	stub := &dataViewStub{pages: []*storagepb.QueryTimeSeriesRowsRsp{
		page(true, 5, "idx_a", tsRow("BTC-USDT", barStart, double(1))),
		page(false, 6, "idx_a", tsRow("ETH-USDT", barStart, double(2))),
	}}
	rpc := &RPCClient{DataView: stub, ViewAuth: &commonpb.AuthInfo{AppId: "strategy"}}
	if _, _, err := rpc.QueryRows(context.Background(), "space", baseQuery()); !errors.Is(err, ErrStale) {
		t.Fatalf("修订号变化应返回 ErrStale：%v", err)
	}
	if stub.requests[1].GetExpectedActiveIndexRevision() != 5 || stub.requests[0].GetExpectedActiveIndexId() != "idx_a" {
		t.Fatalf("第二页应固定第一页的修订号：%+v", stub.requests[1])
	}
	stub = &dataViewStub{pages: []*storagepb.QueryTimeSeriesRowsRsp{page(false, 5, "idx_b", tsRow("BTC-USDT", barStart, double(1)))}}
	rpc = &RPCClient{DataView: stub, ViewAuth: &commonpb.AuthInfo{AppId: "strategy"}}
	if _, _, err := rpc.QueryRows(context.Background(), "space", baseQuery()); !errors.Is(err, ErrStale) {
		t.Fatalf("索引变化应返回 ErrStale：%v", err)
	}
	stub = &dataViewStub{pages: []*storagepb.QueryTimeSeriesRowsRsp{{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_VIEW_NOT_READY, Msg: "active view index revision changed"}}}}
	rpc = &RPCClient{DataView: stub, ViewAuth: &commonpb.AuthInfo{AppId: "strategy"}}
	if _, _, err := rpc.QueryRows(context.Background(), "space", baseQuery()); !errors.Is(err, ErrStale) {
		t.Fatalf("Storage 报告索引变化应返回 ErrStale：%v", err)
	}
}

func TestQueryRowsConcatenatesPagesAndConvertsValues(t *testing.T) {
	text := &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: "3.5"}}
	bad := &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: "n/a"}}
	stub := &dataViewStub{pages: []*storagepb.QueryTimeSeriesRowsRsp{
		page(true, 5, "idx_a", tsRow("BTC-USDT", barStart, double(1))),
		page(false, 5, "idx_a", tsRow("ETH-USDT", barStart, text), tsRow("SOL-USDT", barStart, bad)),
	}}
	rpc := &RPCClient{DataView: stub, ViewAuth: &commonpb.AuthInfo{AppId: "strategy"}}
	rows, revision, err := rpc.QueryRows(context.Background(), "space", baseQuery())
	if err != nil || revision != 5 || len(rows) != 3 {
		t.Fatalf("分页拼接失败：rows=%d revision=%d err=%v", len(rows), revision, err)
	}
	if rows[0].Values["close"] != 1 || rows[1].Values["close"] != 3.5 {
		t.Fatalf("数值转换不符：%+v", rows)
	}
	if _, ok := rows[2].Values["close"]; ok {
		t.Fatalf("无法解析的值应视为缺失：%+v", rows[2])
	}
	if got := stub.requests[0].GetSelectors(); len(got) != 2 || got[0].GetSeriesTag() != "venue:binance" || got[0].GetDatasetId() != "ds" || got[0].GetFreq() != "1h" {
		t.Fatalf("选择器不符：%+v", got)
	}
	if stub.requests[0].GetAuthInfo().GetAppId() != "strategy" || len(stub.requests[0].GetColumnNames()) != 1 {
		t.Fatalf("请求头不符：%+v", stub.requests[0])
	}
	empty, _, err := rpc.QueryRows(context.Background(), "space", Query{ViewID: "view", ExpectedIndexID: "idx_a"})
	if err != nil || len(empty) != 0 {
		t.Fatalf("没有标的时应直接返回空：%v err=%v", empty, err)
	}
}

// 列不存在是确定性的配置错误，不能按基础设施错误重试。
func TestQueryRowsMapsMissingColumnToConfigError(t *testing.T) {
	stub := &dataViewStub{pages: []*storagepb.QueryTimeSeriesRowsRsp{{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_VIEW_COLUMN_NOT_FOUND, Msg: "column ma_20 not found"}}}}
	client := &RPCClient{DataView: stub}
	query := baseQuery()
	query.Columns = []string{"close", "ma_20"}
	_, _, err := client.QueryRows(context.Background(), "space", query)
	var skip *SkipError
	if !errors.As(err, &skip) || skip.Reason != SkipConfigError || skip.Detail != "View view 缺少列 ma_20" {
		t.Fatalf("列不存在应记为 config_error，说明里只有中文与列名：%v", err)
	}
	if raw := RawCause(err); raw == nil || raw.Error() != "column ma_20 not found" {
		t.Fatalf("Storage 的原文应留给日志：%v", raw)
	}
}

// 调用 Storage 失败时错误信息不带服务地址，原始错误只经 RawCause 留给日志；Limit 只读第一页。
func TestQueryRowsHidesTransportDetailsAndHonorsLimit(t *testing.T) {
	failing := &RPCClient{DataView: &failingDataView{err: errors.New("dial tcp 10.0.0.1:443: connect: connection refused")}}
	_, _, err := failing.QueryRows(context.Background(), "space", baseQuery())
	var transportErr *TransportError
	if !errors.As(err, &transportErr) || strings.Contains(err.Error(), "10.0.0.1") || !strings.Contains(RawCause(err).Error(), "10.0.0.1") {
		t.Fatalf("传输错误应只给概述并保留原始原因：%v", err)
	}
	stub := &dataViewStub{pages: []*storagepb.QueryTimeSeriesRowsRsp{page(true, 3, "idx_a", tsRow("BTC-USDT", barStart, double(1)))}}
	query := baseQuery()
	query.Limit = 1
	rows, _, err := (&RPCClient{DataView: stub}).QueryRows(context.Background(), "space", query)
	if err != nil || len(rows) != 1 || len(stub.requests) != 1 || stub.requests[0].GetPage().GetSize() != 1 {
		t.Fatalf("Limit 应只读一页且页大小为 Limit：rows=%d requests=%d err=%v", len(rows), len(stub.requests), err)
	}
}

// failingDataView 让每次查询都返回传输错误。
type failingDataView struct {
	storagepb.DataViewClientProxy
	err error
}

func (f *failingDataView) QueryTimeSeriesRows(context.Context, *storagepb.QueryTimeSeriesRowsReq, ...client.Option) (*storagepb.QueryTimeSeriesRowsRsp, error) {
	return nil, f.err
}
