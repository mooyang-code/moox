package source

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/proto"
)

type fakeGateway struct {
	columns  []*storagepb.ListDatasetColumnsRsp
	reads    []*storagepb.ReadTimeSeriesRowsRsp
	calls    []string
	lastRead *storagepb.ReadTimeSeriesRowsReq
	fail     error
}

func (f *fakeGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	f.calls = append(f.calls, service+"/"+method)
	if f.fail != nil {
		return f.fail
	}
	switch r := req.(type) {
	case *storagepb.ListDatasetColumnsReq:
		page := int(r.GetPage().GetPage()) - 1
		proto.Merge(rsp.(*storagepb.ListDatasetColumnsRsp), f.columns[page])
	case *storagepb.ReadTimeSeriesRowsReq:
		f.lastRead = r
		proto.Merge(rsp.(*storagepb.ReadTimeSeriesRowsRsp), f.reads[0])
		f.reads = f.reads[1:]
	}
	return nil
}

func ok() *commonpb.RetInfo { return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS} }

func col(name string, origin storagepb.DatasetColumnOriginType, typ storagepb.FieldValueType, status string) *storagepb.DatasetColumn {
	return &storagepb.DatasetColumn{ColumnName: name, OriginType: origin, ValueType: typ, Status: status}
}

func TestColumnsKeepsFieldAndCustomColumnsSortedAndSkipsSystemAndDisabled(t *testing.T) {
	field, custom, system := storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_CUSTOM, storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_SYSTEM
	gw := &fakeGateway{columns: []*storagepb.ListDatasetColumnsRsp{
		{RetInfo: ok(), PageResult: &commonpb.PageResult{HasMore: true}, Columns: []*storagepb.DatasetColumn{
			col("volume", field, storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "active"),
			col("subject_id", system, storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "active"),
			col("old", field, storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "disabled"),
		}},
		{RetInfo: ok(), Columns: []*storagepb.DatasetColumn{
			col("close", field, storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, ""),
			col("zscore", custom, storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "active"),
			col("tag", field, storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "active"),
			col("n", field, storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, "active"),
		}},
	}}
	got, err := New(gw, "").Columns(context.Background(), "crypto", "ds")
	if err != nil {
		t.Fatal(err)
	}
	want := []staging.Column{{Name: "close", Type: "double"}, {Name: "n", Type: "int"}, {Name: "tag", Type: "string"}, {Name: "volume", Type: "double"}, {Name: "zscore", Type: "double"}}
	if len(got) != len(want) {
		t.Fatalf("columns = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("columns[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if len(gw.calls) != 2 {
		t.Fatalf("calls = %v", gw.calls)
	}
}

func TestColumnsRejectsEmptyUnsupportedAndFailedCalls(t *testing.T) {
	field := storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD
	empty := &fakeGateway{columns: []*storagepb.ListDatasetColumnsRsp{{RetInfo: ok()}}}
	if _, err := New(empty, "").Columns(context.Background(), "crypto", "ds"); err == nil || !strings.Contains(err.Error(), "no value columns") {
		t.Fatalf("err = %v", err)
	}
	weird := &fakeGateway{columns: []*storagepb.ListDatasetColumnsRsp{{RetInfo: ok(), Columns: []*storagepb.DatasetColumn{col("x", field, storagepb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED, "active")}}}}
	if _, err := New(weird, "").Columns(context.Background(), "crypto", "ds"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("err = %v", err)
	}
	denied := &fakeGateway{columns: []*storagepb.ListDatasetColumnsRsp{{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NO_PERMISSION, Msg: "denied"}}}}
	if _, err := New(denied, "").Columns(context.Background(), "crypto", "ds"); err == nil {
		t.Fatal("denied")
	}
	if _, err := New(&fakeGateway{fail: errors.New("EOF")}, "").Columns(context.Background(), "crypto", "ds"); err == nil {
		t.Fatal("transport error")
	}
}

func tv(v any) *storagepb.TypedValue {
	switch x := v.(type) {
	case float64:
		return &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: x}}
	case int64:
		return &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: x}}
	case string:
		return &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: x}}
	case bool:
		return &storagepb.TypedValue{Value: &storagepb.TypedValue_BoolValue{BoolValue: x}}
	}
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_NullValue{}}
}

func TestReadRangeConvertsRowsAndPassesTheCursorAndRange(t *testing.T) {
	cols := []staging.Column{{Name: "close", Type: "double"}, {Name: "n", Type: "int"}, {Name: "tag", Type: "string"}, {Name: "ok", Type: "bool"}}
	key := func(subject, at, series string) *storagepb.TimeSeriesKey {
		return &storagepb.TimeSeriesKey{SubjectId: subject, DataTime: at, SeriesTag: series, Freq: "1m"}
	}
	gw := &fakeGateway{reads: []*storagepb.ReadTimeSeriesRowsRsp{{
		RetInfo: ok(), PageResult: &commonpb.PageResult{HasMore: true, NextCursor: "c2"},
		Rows: []*storagepb.TimeSeriesRow{
			{Key: key("BTC", "2026-10-15T01:00:00Z", ""), Fields: []*storagepb.FieldValue{{FieldId: "close", Value: tv(1.5)}, {FieldId: "n", Value: tv(int64(3))}, {FieldId: "tag", Value: tv("")}, {FieldId: "ok", Value: tv(true)}}},
			{Key: key("ETH", "2026-10-15T01:00:00.000Z", "x"), Fields: []*storagepb.FieldValue{{FieldId: "close", Value: tv(int64(2))}, {FieldId: "tag", Value: tv(nil)}, {FieldId: "ignored", Value: tv(9.0)}}},
		},
	}}}
	start, end := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	page, err := New(gw, "secret-secret-secret-secret-1234").ReadRange(context.Background(), Range{SpaceID: "crypto", DatasetID: "ds", Columns: cols, Start: start, End: end, Cursor: "c1", PageRows: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != "c2" || len(page.Rows) != 2 || page.Bytes <= 0 {
		t.Fatalf("page = %+v", page)
	}
	r0, r1 := page.Rows[0], page.Rows[1]
	if r0.SubjectID != "BTC" || !r0.DataTime.Equal(start.Add(time.Hour)) || r0.SeriesTag != "" || r0.Values[0] != 1.5 || r0.Values[1] != int64(3) || r0.Values[2] != "" || r0.Values[3] != true {
		t.Fatalf("row0 = %+v", r0)
	}
	if r1.SeriesTag != "x" || r1.Values[0] != 2.0 || r1.Values[1] != nil || r1.Values[2] != nil || r1.Values[3] != nil {
		t.Fatalf("row1 = %+v", r1)
	}
	req := gw.lastRead
	if req.GetTimeRange().GetStartTime() != "2026-10-15T00:00:00Z" || req.GetTimeRange().GetEndTime() != "2026-10-16T00:00:00Z" || req.GetPage().GetCursor() != "c1" || req.GetPage().GetSize() != 2000 ||
		len(req.GetSelectors()) != 0 || strings.Join(req.GetColumnNames(), ",") != "close,n,tag,ok" || req.GetLatestPerSeries() != 0 {
		t.Fatalf("request = %v", req)
	}
	if auth := req.GetAuthInfo(); auth.GetAppId() != "archive" || auth.GetAppKey() == "" {
		t.Fatalf("auth = %v", auth)
	}
}

func TestReadRangeRejectsBadResponses(t *testing.T) {
	cols := []staging.Column{{Name: "close", Type: "double"}}
	call := func(rsp *storagepb.ReadTimeSeriesRowsRsp) error {
		gw := &fakeGateway{reads: []*storagepb.ReadTimeSeriesRowsRsp{rsp}}
		_, err := New(gw, "").ReadRange(context.Background(), Range{SpaceID: "c", DatasetID: "d", Columns: cols, PageRows: 10})
		return err
	}
	if err := call(&storagepb.ReadTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "boom"}}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	badTime := &storagepb.ReadTimeSeriesRowsRsp{RetInfo: ok(), Rows: []*storagepb.TimeSeriesRow{{Key: &storagepb.TimeSeriesKey{SubjectId: "A", DataTime: "yesterday"}}}}
	if err := call(badTime); err == nil {
		t.Fatal("坏的 data_time 必须报错")
	}
	mismatch := &storagepb.ReadTimeSeriesRowsRsp{RetInfo: ok(), Rows: []*storagepb.TimeSeriesRow{{Key: &storagepb.TimeSeriesKey{SubjectId: "A", DataTime: "2026-10-15T00:00:00Z"}, Fields: []*storagepb.FieldValue{{FieldId: "close", Value: tv("text")}}}}}
	if err := call(mismatch); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("类型不符必须报错: %v", err)
	}
	noCursor := &storagepb.ReadTimeSeriesRowsRsp{RetInfo: ok(), PageResult: &commonpb.PageResult{HasMore: true}}
	if err := call(noCursor); err == nil {
		t.Fatal("还有更多但没有游标必须报错")
	}
	last := &storagepb.ReadTimeSeriesRowsRsp{RetInfo: ok()}
	if err := call(last); err != nil {
		t.Fatalf("最后一页: %v", err)
	}
}
