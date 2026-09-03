package primarystore

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/report"
)

type recordingNode struct {
	write func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error)
	read  func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error)
}

type recordingMarkerNode struct {
	*recordingNode
	markerCalls []string
}

type recordingView struct {
	query func(context.Context, *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error)
}

func (v *recordingView) QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
	return v.query(ctx, req)
}

func (*recordingView) SearchRecordRows(context.Context, *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error) {
	return nil, errors.New("unexpected record query")
}

func (n *recordingNode) UpsertFields(ctx context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	return n.write(ctx, req)
}

func (n *recordingNode) ReadFields(ctx context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	return n.read(ctx, req)
}

func (n *recordingMarkerNode) AppendDatasetPeriodCollected(context.Context, *pb.AppendDatasetPeriodCollectedReq) (*pb.AppendDatasetPeriodCollectedRsp, error) {
	n.markerCalls = append(n.markerCalls, "dataset")
	return &pb.AppendDatasetPeriodCollectedRsp{RetInfo: successRetInfo()}, nil
}

func (n *recordingMarkerNode) AppendFactorPeriodComputed(context.Context, *pb.AppendFactorPeriodComputedReq) (*pb.AppendFactorPeriodComputedRsp, error) {
	n.markerCalls = append(n.markerCalls, "factor")
	return &pb.AppendFactorPeriodComputedRsp{RetInfo: successRetInfo()}, nil
}

func (n *recordingMarkerNode) AppendDatasetSyncPointMarker(context.Context, *pb.AppendDatasetSyncPointMarkerReq) (*pb.AppendDatasetSyncPointMarkerRsp, error) {
	n.markerCalls = append(n.markerCalls, "sync-point")
	return &pb.AppendDatasetSyncPointMarkerRsp{RetInfo: successRetInfo()}, nil
}

func (*recordingMarkerNode) GetFactorPeriodComputedMarker(context.Context, *pb.GetFactorPeriodComputedMarkerReq) (*pb.GetFactorPeriodComputedMarkerRsp, error) {
	return &pb.GetFactorPeriodComputedMarkerRsp{RetInfo: successRetInfo()}, nil
}

func (n *recordingNode) GetNodeState(context.Context, *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	return &pb.GetNodeStateRsp{}, nil
}

func (n *recordingNode) CleanupExpiredBuckets(context.Context, *pb.CleanupExpiredBucketsReq) (*pb.CleanupExpiredBucketsRsp, error) {
	return &pb.CleanupExpiredBucketsRsp{}, nil
}

func TestPrimaryRoutesAndValidatesBeforeDataNode(t *testing.T) {
	node, err := datanode.NewService(datanode.Options{NodeID: "node-a", AuthSecret: "node-secret", Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node")}})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	svc, err := New(Options{Node: node, AuthSigner: func(_ *pb.AuthInfo) (*pb.AuthInfo, error) {
		return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey("node-secret", "primary")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "ignored"}, Rows: []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.2}}}}}}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	bad, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{Rows: []*pb.RowFieldUpsert{{Key: key}}})
	if err != nil || bad.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("bad rsp=%v err=%v", bad, err)
	}
}

func TestMooxSkillWriteMethodsAreDeniedBeforeDataNodeResolution(t *testing.T) {
	resolved := 0
	svc, err := New(Options{Resolver: func(context.Context, string, string) (DataNodeClient, error) {
		resolved++
		return &recordingNode{
			write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
				return &pb.UpsertFieldsRsp{RetInfo: successRetInfo()}, nil
			},
			read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
				return &pb.ReadFieldsRsp{RetInfo: successRetInfo()}, nil
			},
		}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	auth := &pb.AuthInfo{AppId: "moox-skill", AppKey: "valid"}
	row := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "blocked"}}}},
	}
	tests := map[string]func() (*pb.RetInfo, error){
		"UpsertFields": func() (*pb.RetInfo, error) {
			rsp, callErr := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{AuthInfo: auth, Rows: []*pb.RowFieldUpsert{row}})
			return rsp.GetRetInfo(), callErr
		},
		"ReportDatasetPeriodCollected": func() (*pb.RetInfo, error) {
			rsp, callErr := svc.ReportDatasetPeriodCollected(context.Background(), &pb.ReportDatasetPeriodCollectedReq{
				AuthInfo: auth, SpaceId: "space", Marker: &pb.DatasetPeriodCollectedMarker{DatasetId: "dataset"},
			})
			return rsp.GetRetInfo(), callErr
		},
		"ReportFactorPeriodComputed": func() (*pb.RetInfo, error) {
			rsp, callErr := svc.ReportFactorPeriodComputed(context.Background(), &pb.ReportFactorPeriodComputedReq{
				AuthInfo: auth, SpaceId: "space", Marker: &pb.FactorPeriodComputedMarker{ResultDatasetId: "dataset"},
			})
			return rsp.GetRetInfo(), callErr
		},
		"AppendDatasetSyncPoint": func() (*pb.RetInfo, error) {
			rsp, callErr := svc.AppendDatasetSyncPoint(context.Background(), &pb.AppendDatasetSyncPointReq{
				AuthInfo: auth, SpaceId: "space", SyncPoint: &pb.DatasetSyncPointMarker{DatasetId: "dataset", RequestId: "request", Source: "import"},
			})
			return rsp.GetRetInfo(), callErr
		},
	}
	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			before := resolved
			info, callErr := call()
			if callErr != nil || info.GetCode() != pb.ErrorCode_NO_PERMISSION || !strings.Contains(info.GetMsg(), "read-only primary credential") {
				t.Fatalf("ret_info=%v err=%v", info, callErr)
			}
			if resolved != before {
				t.Fatalf("DataNode resolver called: before=%d after=%d", before, resolved)
			}
		})
	}
}

func TestPrimaryWriteMethodsStillAllowInternalCallers(t *testing.T) {
	node := &recordingMarkerNode{recordingNode: &recordingNode{
		write: func(_ context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return &pb.UpsertFieldsRsp{RetInfo: successRetInfo(), Keys: []*pb.RowKey{req.GetRows()[0].GetKey()}}, nil
		},
		read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			return &pb.ReadFieldsRsp{RetInfo: successRetInfo()}, nil
		},
	}}
	svc, err := New(Options{Node: node})
	if err != nil {
		t.Fatal(err)
	}
	row := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "allowed"}}}},
	}
	upsert, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "collector"}, Rows: []*pb.RowFieldUpsert{row},
	})
	if err != nil || upsert.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("upsert rsp=%v err=%v", upsert, err)
	}
	collected, err := svc.ReportDatasetPeriodCollected(context.Background(), &pb.ReportDatasetPeriodCollectedReq{
		AuthInfo: &pb.AuthInfo{AppId: "collector"}, SpaceId: "space", Marker: &pb.DatasetPeriodCollectedMarker{DatasetId: "dataset"},
	})
	if err != nil || collected.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("collected rsp=%v err=%v", collected, err)
	}
	computed, err := svc.ReportFactorPeriodComputed(context.Background(), &pb.ReportFactorPeriodComputedReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "space", Marker: &pb.FactorPeriodComputedMarker{ResultDatasetId: "dataset"},
	})
	if err != nil || computed.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("computed rsp=%v err=%v", computed, err)
	}
	syncPoint, err := svc.AppendDatasetSyncPoint(context.Background(), &pb.AppendDatasetSyncPointReq{
		AuthInfo: &pb.AuthInfo{AppId: "storage-view"}, SpaceId: "space",
		SyncPoint: &pb.DatasetSyncPointMarker{DatasetId: "dataset", RequestId: "request", Source: "import"},
	})
	if err != nil || syncPoint.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("sync point rsp=%v err=%v", syncPoint, err)
	}
	if !reflect.DeepEqual(node.markerCalls, []string{"dataset", "factor", "sync-point"}) {
		t.Fatalf("marker calls=%v", node.markerCalls)
	}
}

func TestPrimaryRangeReadsRequireCallerAuthorization(t *testing.T) {
	svc, err := New(Options{
		Node: &recordingNode{
			write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
				return &pb.UpsertFieldsRsp{}, nil
			},
			read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
				return &pb.ReadFieldsRsp{}, nil
			},
		},
		Authorizer: func(*pb.AuthInfo) error { return errors.New("denied") },
	})
	if err != nil {
		t.Fatal(err)
	}
	records, err := svc.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{
		Keys:         []*pb.RecordKey{{SpaceId: "space", DatasetId: "dataset", RecordId: "record"}},
		VersionRange: &pb.VersionRange{},
	})
	if err != nil || records.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("record range read rsp=%v err=%v", records, err)
	}
	timeSeries, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		SpaceId:   "space",
		DatasetId: "dataset",
		Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "dataset", SubjectId: "subject", Freq: "1m"}},
		TimeRange: &pb.TimeRange{},
	})
	if err != nil || timeSeries.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("time-series range read rsp=%v err=%v", timeSeries, err)
	}
}

func TestReadTimeSeriesRowsUsesSelectorsAndCopiesViewCompleteness(t *testing.T) {
	var captured *pb.QueryTimeSeriesRowsReq
	view := &recordingView{query: func(_ context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
		captured = req
		return &pb.QueryTimeSeriesRowsRsp{
			RetInfo:           successRetInfo(),
			Rows:              []*pb.TimeSeriesRow{{Key: &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-29T00:00:00Z", SeriesTag: "venue:okx"}}},
			ServedIndexedFrom: "2026-07-29T00:00:00Z",
			ServedIndexedTo:   "2026-07-29T00:01:00Z",
			Complete:          true,
		}, nil
	}}
	node := &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return nil, errors.New("unexpected write")
		},
		read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			return nil, errors.New("ReadTimeSeriesRows must not use point ReadFields")
		},
	}
	svc, err := New(Options{
		Node: node,
		View: func(_ context.Context, spaceID, datasetID string) (pb.DataViewService, string, error) {
			if spaceID != "space" || datasetID != "market" {
				t.Fatalf("resolved scope=%s/%s", spaceID, datasetID)
			}
			return view, "prices", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	empty := ""
	okx := "venue:okx"
	selectors := []*pb.TimeSeriesSelector{
		{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"},
		{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", SeriesTag: &empty},
		{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", SeriesTag: &okx},
	}
	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		SpaceId:  "space", DatasetId: "market", Selectors: selectors,
		Order: pb.SortOrder_SORT_ORDER_DESC,
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
	if rsp.GetServedIndexedFrom() != "2026-07-29T00:00:00Z" ||
		rsp.GetServedIndexedTo() != "2026-07-29T00:01:00Z" || !rsp.GetComplete() {
		t.Fatalf("view completeness was not copied: %v", rsp)
	}
	if captured == nil || len(captured.GetSelectors()) != 3 {
		t.Fatalf("selectors not forwarded: %v", captured)
	}
	for i := range selectors {
		if (captured.GetSelectors()[i].SeriesTag == nil) != (selectors[i].SeriesTag == nil) ||
			captured.GetSelectors()[i].GetSeriesTag() != selectors[i].GetSeriesTag() {
			t.Fatalf("selector %d presence changed: got=%v want=%v", i, captured.GetSelectors()[i], selectors[i])
		}
	}
	wantSorts := []string{"subject_id", "freq", "data_time", "series_tag"}
	if len(captured.GetSorts()) != len(wantSorts) {
		t.Fatalf("sorts=%v", captured.GetSorts())
	}
	for i, field := range wantSorts {
		if captured.GetSorts()[i].GetFieldName() != field || !captured.GetSorts()[i].GetDesc() {
			t.Fatalf("sort %d=%v", i, captured.GetSorts()[i])
		}
	}
	if len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetKey().GetSeriesTag() != "venue:okx" {
		t.Fatalf("exact result tag lost: %v", rsp.GetRows())
	}
}

func TestMooxSkillReadIsBoundToKlineScope(t *testing.T) {
	resolved := 0
	svc, err := New(Options{Resolver: func(context.Context, string, string) (DataNodeClient, error) {
		resolved++
		return &recordingNode{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	validSelector := &pb.TimeSeriesSelector{
		SpaceId: "crypto", DatasetId: "binance_spot_kline_1m", SubjectId: "BTC-USDT",
		Freq: "1m", SeriesTag: stringPtr("venue:binance"),
	}
	tests := []struct {
		name string
		req  *pb.ReadTimeSeriesRowsReq
	}{
		{name: "dataset", req: &pb.ReadTimeSeriesRowsReq{
			AuthInfo: &pb.AuthInfo{AppId: "moox-skill"}, SpaceId: "other", DatasetId: "dataset",
			Selectors: []*pb.TimeSeriesSelector{validSelector}, Order: pb.SortOrder_SORT_ORDER_DESC,
		}},
		{name: "exact keys", req: &pb.ReadTimeSeriesRowsReq{
			AuthInfo: &pb.AuthInfo{AppId: "moox-skill"}, SpaceId: "crypto", DatasetId: "binance_spot_kline_1m",
			Selectors: []*pb.TimeSeriesSelector{validSelector}, Keys: []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "binance_spot_kline_1m"}}, Order: pb.SortOrder_SORT_ORDER_DESC,
		}},
		{name: "page size", req: &pb.ReadTimeSeriesRowsReq{
			AuthInfo: &pb.AuthInfo{AppId: "moox-skill"}, SpaceId: "crypto", DatasetId: "binance_spot_kline_1m",
			Selectors: []*pb.TimeSeriesSelector{validSelector}, Page: &commonpb.Page{Size: 1001}, Order: pb.SortOrder_SORT_ORDER_DESC,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rsp, err := svc.ReadTimeSeriesRows(context.Background(), test.req)
			if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
				t.Fatalf("rsp=%v err=%v", rsp, err)
			}
		})
	}
	if resolved != 0 {
		t.Fatalf("invalid moox-skill requests resolved a data node %d times", resolved)
	}
}

func TestValidateMooxSkillReadRequestAllowsOnlyExportedKlineSelectors(t *testing.T) {
	cryptoTag := "venue:binance"
	emptyTag := ""
	otherTag := "venue:okx"
	request := func(spaceID, datasetID, freq string, seriesTag *string) *pb.ReadTimeSeriesRowsReq {
		return &pb.ReadTimeSeriesRowsReq{
			SpaceId: spaceID, DatasetId: datasetID, Order: pb.SortOrder_SORT_ORDER_DESC,
			Selectors: []*pb.TimeSeriesSelector{{
				SpaceId: spaceID, DatasetId: datasetID, SubjectId: "subject", Freq: freq, SeriesTag: seriesTag,
			}},
		}
	}

	for _, tc := range []struct {
		name    string
		req     *pb.ReadTimeSeriesRowsReq
		wantErr bool
	}{
		{name: "crypto binance 1m", req: request("crypto", "binance_spot_kline_1m", "1m", &cryptoTag)},
		{name: "crypto wildcard series", req: request("crypto", "binance_spot_kline_1m", "1m", nil), wantErr: true},
		{name: "crypto empty series", req: request("crypto", "binance_spot_kline_1m", "1m", &emptyTag), wantErr: true},
		{name: "crypto other series", req: request("crypto", "binance_spot_kline_1m", "1m", &otherTag), wantErr: true},
		{name: "stock cn default series 1m", req: request("stock_cn", "stock_cn_kline", "1m", &emptyTag)},
		{name: "stock cn wildcard series", req: request("stock_cn", "stock_cn_kline", "1m", nil), wantErr: true},
		{name: "stock cn provider series", req: request("stock_cn", "stock_cn_kline", "1m", &cryptoTag), wantErr: true},
		{name: "stock cn other frequency", req: request("stock_cn", "stock_cn_kline", "1d", &emptyTag), wantErr: true},
		{name: "stock cn other dataset", req: request("stock_cn", "stock_kline", "1m", &emptyTag), wantErr: true},
		{name: "other space", req: request("stock_us", "stock_cn_kline", "1m", &emptyTag), wantErr: true},
		{name: "mixed allowed datasets", req: func() *pb.ReadTimeSeriesRowsReq {
			req := request("stock_cn", "stock_cn_kline", "1m", &emptyTag)
			req.Selectors[0].SpaceId = "crypto"
			req.Selectors[0].DatasetId = "binance_spot_kline_1m"
			req.Selectors[0].SeriesTag = &cryptoTag
			return req
		}(), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMooxSkillReadRequest(tc.req)
			if tc.wantErr && err == nil {
				t.Fatal("expected selector to be rejected")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected selector to be allowed: %v", err)
			}
		})
	}
}

func TestMooxSkillStockReadForwardsExactEmptySeriesToView(t *testing.T) {
	var captured *pb.QueryTimeSeriesRowsReq
	view := &recordingView{query: func(_ context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
		captured = req
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: successRetInfo()}, nil
	}}
	svc, err := New(Options{
		Node: &recordingNode{},
		View: func(_ context.Context, spaceID, datasetID string) (pb.DataViewService, string, error) {
			if spaceID != "stock_cn" || datasetID != "stock_cn_kline" {
				t.Fatalf("resolved scope=%s/%s", spaceID, datasetID)
			}
			return view, "stock_cn_kline_view", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	emptyTag := ""
	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "moox-skill"},
		SpaceId:  "stock_cn", DatasetId: "stock_cn_kline", Order: pb.SortOrder_SORT_ORDER_DESC,
		Selectors: []*pb.TimeSeriesSelector{{
			SpaceId: "stock_cn", DatasetId: "stock_cn_kline", SubjectId: "600000.SH", Freq: "1m", SeriesTag: &emptyTag,
		}},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
	if captured == nil || len(captured.GetSelectors()) != 1 || captured.GetSelectors()[0].SeriesTag == nil || captured.GetSelectors()[0].GetSeriesTag() != "" {
		t.Fatalf("exact empty series_tag was not forwarded: %v", captured)
	}
}

func stringPtr(value string) *string {
	return &value
}

func TestPrimaryExactTimeSeriesReadOmitsMissingRows(t *testing.T) {
	var existing *pb.RowKey
	node := &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return &pb.UpsertFieldsRsp{}, nil
		},
		read: func(_ context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
			for _, key := range req.GetKeys() {
				rows = append(rows, &pb.RowFieldValues{Key: key})
			}
			existing = req.GetKeys()[1]
			rows[1].Fields = []*pb.FieldValue{{
				FieldId: "close",
				Value:   &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}},
			}}
			return &pb.ReadFieldsRsp{
				RetInfo:      successRetInfo(),
				Rows:         rows,
				ExistingKeys: []*pb.RowKey{existing},
			}, nil
		},
	}
	svc, err := New(Options{Node: node})
	if err != nil {
		t.Fatal(err)
	}
	keys := []*pb.TimeSeriesKey{
		{SpaceId: "space", DatasetId: "dataset", SubjectId: "subject", Freq: "1H", DataTime: "2026-07-29T16:00:00Z", SeriesTag: "venue:okx"},
		{SpaceId: "space", DatasetId: "dataset", SubjectId: "subject", Freq: "1H", DataTime: "2026-07-29T15:00:00Z", SeriesTag: "venue:binance"},
	}
	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		AuthInfo:    &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		SpaceId:     "space",
		DatasetId:   "dataset",
		Keys:        keys,
		ColumnNames: []string{"close"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read rsp=%v err=%v", rsp, err)
	}
	if len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetKey().GetDataTime() != keys[1].GetDataTime() ||
		rsp.GetRows()[0].GetKey().GetSeriesTag() != "venue:binance" {
		t.Fatalf("rows=%v want only existing key %v", rsp.GetRows(), existing)
	}
}

func TestPrimaryTimeSeriesReadRejectsAmbiguousOrMismatchedScope(t *testing.T) {
	svc, err := New(Options{Node: &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return nil, errors.New("unexpected write")
		},
		read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			return nil, errors.New("invalid request reached DataNode")
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.TimeSeriesKey{
		SpaceId: "space", DatasetId: "dataset", SubjectId: "subject", Freq: "1m",
		DataTime: "2026-07-29T15:00:00Z", SeriesTag: "venue:binance",
	}
	selector := &pb.TimeSeriesSelector{
		SpaceId: "space", DatasetId: "dataset", SubjectId: "subject", Freq: "1m",
	}
	for _, tc := range []struct {
		name string
		req  *pb.ReadTimeSeriesRowsReq
	}{
		{
			name: "keys and selectors",
			req: &pb.ReadTimeSeriesRowsReq{
				SpaceId: "space", DatasetId: "dataset", Keys: []*pb.TimeSeriesKey{key},
				Selectors: []*pb.TimeSeriesSelector{selector}, ColumnNames: []string{"close"},
			},
		},
		{
			name: "key outside top-level scope",
			req: &pb.ReadTimeSeriesRowsReq{
				SpaceId: "other", DatasetId: "dataset", Keys: []*pb.TimeSeriesKey{key},
				ColumnNames: []string{"close"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.AuthInfo = &pb.AuthInfo{AppId: "caller", AppKey: "key"}
			rsp, readErr := svc.ReadTimeSeriesRows(context.Background(), tc.req)
			if readErr != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
				t.Fatalf("rsp=%v err=%v", rsp, readErr)
			}
		})
	}
}

func TestPrimaryExactRecordReadOmitsMissingRows(t *testing.T) {
	node := &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return &pb.UpsertFieldsRsp{}, nil
		},
		read: func(_ context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
			for _, key := range req.GetKeys() {
				rows = append(rows, &pb.RowFieldValues{Key: key})
			}
			rows[1].Fields = []*pb.FieldValue{{
				FieldId: "value",
				Value:   &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "present"}},
			}}
			return &pb.ReadFieldsRsp{
				RetInfo:      successRetInfo(),
				Rows:         rows,
				ExistingKeys: []*pb.RowKey{req.GetKeys()[1]},
			}, nil
		},
	}
	svc, err := New(Options{Node: node})
	if err != nil {
		t.Fatal(err)
	}
	keys := []*pb.RecordKey{
		{SpaceId: "space", DatasetId: "dataset", RecordId: "missing", Version: "1"},
		{SpaceId: "space", DatasetId: "dataset", RecordId: "present", Version: "1"},
	}
	rsp, err := svc.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{
		AuthInfo:    &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys:        keys,
		ColumnNames: []string{"value"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read rsp=%v err=%v", rsp, err)
	}
	if len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetKey().GetRecordId() != keys[1].GetRecordId() {
		t.Fatalf("rows=%v want only existing key %v", rsp.GetRows(), keys[1])
	}
}

func TestPrimaryRejectsWritesFromSCFMarketCanaryCredential(t *testing.T) {
	svc, err := New(Options{
		Node: &recordingNode{
			write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
				t.Fatal("read-only SCF credential reached DataNode")
				return nil, nil
			},
			read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
				return &pb.ReadFieldsRsp{}, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "scf-market-canary", AppKey: "valid"},
		Rows: []*pb.RowFieldUpsert{{
			Key: &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}},
		}},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestPrimaryRoutesSameDatasetInDifferentSpacesSeparately(t *testing.T) {
	var resolved []string
	node := &recordingNode{
		write: func(_ context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			keys := make([]*pb.RowKey, 0, len(req.GetRows()))
			for _, row := range req.GetRows() {
				keys = append(keys, row.GetKey())
			}
			return &pb.UpsertFieldsRsp{RetInfo: successRetInfo(), Keys: keys}, nil
		},
		read: func(_ context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
			for _, key := range req.GetKeys() {
				rows = append(rows, &pb.RowFieldValues{Key: key})
			}
			return &pb.ReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
		},
	}
	svc, err := New(Options{
		Resolver: func(_ context.Context, spaceID, datasetID string) (DataNodeClient, error) {
			resolved = append(resolved, spaceID+"/"+datasetID)
			return node, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := func(space, record string) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key:    &pb.RowKey{SpaceId: space, DatasetId: "shared", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}},
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: record}}}},
		}
	}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Rows:     []*pb.RowFieldUpsert{row("space-a", "a"), row("space-b", "b")},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	if !reflect.DeepEqual(resolved, []string{"space-a/shared", "space-b/shared"}) {
		t.Fatalf("resolved=%v", resolved)
	}
}

type recordingDatasetMetrics struct {
	observations []report.DatasetObservation
}

func (m *recordingDatasetMetrics) ObserveRun(observation report.DatasetObservation) error {
	m.observations = append(m.observations, observation)
	return nil
}

func TestPrimaryObservesCommittedTimeSeriesWatermarkByFrequency(t *testing.T) {
	node := &recordingNode{
		write: func(_ context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			keys := make([]*pb.RowKey, 0, len(req.GetRows()))
			for _, row := range req.GetRows() {
				keys = append(keys, row.GetKey())
			}
			return &pb.UpsertFieldsRsp{RetInfo: successRetInfo(), Keys: keys}, nil
		},
	}
	metrics := &recordingDatasetMetrics{}
	svc, err := New(Options{Node: node, DatasetMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	row := func(freq string, at time.Time) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key: &pb.RowKey{
				SpaceId: "crypto", DatasetId: "market_kline",
				Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
					SubjectId: "BTC-USDT", Freq: freq, DataTime: at.UTC().Format(time.RFC3339Nano),
				}},
			},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{
				Value: &pb.TypedValue_DoubleValue{DoubleValue: 1},
			}}},
		}
	}
	t1003 := time.Date(2026, 7, 28, 10, 3, 0, 0, time.UTC)
	t1005 := t1003.Add(2 * time.Minute)
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "test", AppKey: "test"},
		Rows:     []*pb.RowFieldUpsert{row("1m", t1003), row("1m", t1005), row("5m", t1003)},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	if len(metrics.observations) != 2 {
		t.Fatalf("observations=%v", metrics.observations)
	}
	got := map[string]report.DatasetObservation{}
	for _, observation := range metrics.observations {
		got[observation.Key.Freq] = observation
	}
	if got["1m"].Rows != 2 || !got["1m"].OutputWatermark.Equal(t1005) || got["1m"].Result != "success" {
		t.Fatalf("1m observation=%+v", got["1m"])
	}
	if got["5m"].Rows != 1 || !got["5m"].OutputWatermark.Equal(t1003) || got["5m"].Result != "success" {
		t.Fatalf("5m observation=%+v", got["5m"])
	}
}

func TestPrimaryWriteFailureReportsErrorWithoutWatermark(t *testing.T) {
	node := &recordingNode{write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
		return nil, errors.New("write failed")
	}}
	metrics := &recordingDatasetMetrics{}
	svc, err := New(Options{Node: node, DatasetMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 28, 10, 5, 0, 0, time.UTC)
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "test", AppKey: "test"},
		Rows: []*pb.RowFieldUpsert{{
			Key: &pb.RowKey{
				SpaceId: "crypto", DatasetId: "market_kline",
				Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
					SubjectId: "BTC-USDT", Freq: "1m", DataTime: at.Format(time.RFC3339Nano),
				}},
			},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{
				Value: &pb.TypedValue_DoubleValue{DoubleValue: 1},
			}}},
		}}})
	if err != nil || rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	if len(metrics.observations) != 1 {
		t.Fatalf("observations=%v", metrics.observations)
	}
	observation := metrics.observations[0]
	if observation.Result != "error" || !observation.OutputWatermark.IsZero() {
		t.Fatalf("observation=%+v", observation)
	}
}

func TestPrimaryDoesNotObserveRecordDataset(t *testing.T) {
	node := &recordingNode{write: func(_ context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
		return &pb.UpsertFieldsRsp{RetInfo: successRetInfo(), Keys: []*pb.RowKey{req.GetRows()[0].GetKey()}}, nil
	}}
	metrics := &recordingDatasetMetrics{}
	svc, err := New(Options{Node: node, DatasetMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "test", AppKey: "test"},
		Rows: []*pb.RowFieldUpsert{{
			Key: &pb.RowKey{
				SpaceId: "crypto", DatasetId: "symbols",
				Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "BTC-USDT", Version: "1"}},
			},
			Fields: []*pb.FieldValue{{FieldId: "symbol", Value: &pb.TypedValue{
				Value: &pb.TypedValue_StringValue{StringValue: "BTC-USDT"},
			}}},
		}},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	if len(metrics.observations) != 0 {
		t.Fatalf("record dataset observations=%v", metrics.observations)
	}
}

func TestPrimaryReadPreservesRequestOrderAcrossDatasets(t *testing.T) {
	node := &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return &pb.UpsertFieldsRsp{RetInfo: successRetInfo()}, nil
		},
		read: func(_ context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
			for _, key := range req.GetKeys() {
				rows = append(rows, &pb.RowFieldValues{Key: key})
			}
			return &pb.ReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
		},
	}
	svc, err := New(Options{Node: node})
	if err != nil {
		t.Fatal(err)
	}
	key := func(dataset, record string) *pb.RowKey {
		return &pb.RowKey{SpaceId: "s", DatasetId: dataset, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}}
	}
	keys := []*pb.RowKey{key("b", "first"), key("a", "second"), key("b", "third")}
	rsp, err := svc.ReadFields(context.Background(), &pb.PrimaryReadFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys:     keys,
		FieldIds: []string{"value"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read rsp=%v err=%v", rsp, err)
	}
	for i, row := range rsp.GetRows() {
		if row.GetKey().GetRecord().GetRecordId() != keys[i].GetRecord().GetRecordId() {
			t.Fatalf("row %d=%v want=%v", i, row.GetKey(), keys[i])
		}
	}
}

func TestPrimaryReadFieldsUsesOneSnapshotAcrossDatasetGroups(t *testing.T) {
	provider := &mutableSnapshotProvider{current: &testRequestSnapshot{generation: "generation-a"}}
	var generations []string
	node := &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return &pb.UpsertFieldsRsp{RetInfo: successRetInfo()}, nil
		},
		read: func(ctx context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			snapshot, ok := metadata.RequestSnapshotFromContext(ctx).(*testRequestSnapshot)
			if !ok || snapshot == nil {
				t.Fatalf("request snapshot missing for %s", req.GetDatasetId())
			}
			generations = append(generations, snapshot.generation)
			return &pb.ReadFieldsRsp{RetInfo: successRetInfo(), Rows: []*pb.RowFieldValues{{Key: req.GetKeys()[0]}}}, nil
		},
	}
	svc, err := New(Options{
		Node:     node,
		Snapshot: provider.RequestSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := func(dataset, record string) *pb.RowKey {
		return &pb.RowKey{SpaceId: "space", DatasetId: dataset, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}}
	}
	rsp, err := svc.ReadFields(context.Background(), &pb.PrimaryReadFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys:     []*pb.RowKey{key("dataset-a", "a"), key("dataset-b", "b")},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read rsp=%v err=%v", rsp, err)
	}
	if provider.calls != 1 {
		t.Fatalf("snapshot provider calls=%d want 1", provider.calls)
	}
	if !reflect.DeepEqual(generations, []string{"generation-a", "generation-a"}) {
		t.Fatalf("generations=%v", generations)
	}
}

func TestPrimaryRejectsRecordWriteWithoutVersion(t *testing.T) {
	node := &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			t.Fatal("write should not reach DataNode")
			return nil, nil
		},
		read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			return nil, nil
		},
	}
	svc, err := New(Options{Node: node})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Rows: []*pb.RowFieldUpsert{{
			Key:    &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}}},
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "x"}}}},
		}},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestPrimaryReadFieldsReturnsResolvedLatestRecordVersion(t *testing.T) {
	node, err := datanode.NewService(datanode.Options{
		NodeID: "node-a", AuthSecret: "node-secret",
		Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	svc, err := New(Options{
		Node: node,
		AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey("node-secret", "primary")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1", "2"} {
		key := &pb.RowKey{SpaceId: "space", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: version}}}
		rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
			AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
			Rows: []*pb.RowFieldUpsert{{
				Key: key,
				Fields: []*pb.FieldValue{{
					FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: version}},
				}},
			}},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("write version=%s rsp=%v err=%v", version, rsp, err)
		}
	}
	rsp, err := svc.ReadFields(context.Background(), &pb.PrimaryReadFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys: []*pb.RowKey{{
			SpaceId: "space", DatasetId: "records",
			Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}},
		}},
		FieldIds: []string{"value"},
	})
	if err != nil || len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetKey().GetRecord().GetVersion() != "2" {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestPrimaryExactTimeSeriesReadCrossesDataNodePebble(t *testing.T) {
	node, err := datanode.NewService(datanode.Options{
		NodeID: "node-a", AuthSecret: "node-secret",
		Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	svc, err := New(Options{
		Node: node,
		AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey("node-secret", "primary")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.TimeSeriesKey{
		SpaceId: "crypto", DatasetId: "spot_kline_1h", SubjectId: "BTC-USDT", Freq: "1h",
		DataTime: "2026-07-29T15:00:00Z", SeriesTag: "venue:binance",
	}
	rowKey := timeSeriesRowKey(key)
	writeRsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Rows: []*pb.RowFieldUpsert{{
			Key: rowKey,
			Fields: []*pb.FieldValue{{
				FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 101.25}},
			}},
		}},
	})
	if err != nil || writeRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", writeRsp, err)
	}
	readRsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		SpaceId:  "crypto", DatasetId: "spot_kline_1h",
		Keys: []*pb.TimeSeriesKey{key}, ColumnNames: []string{"close"},
	})
	if err != nil || readRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS ||
		len(readRsp.GetRows()) != 1 || readRsp.GetRows()[0].GetKey().GetSeriesTag() != "venue:binance" ||
		len(readRsp.GetRows()[0].GetFields()) != 1 ||
		readRsp.GetRows()[0].GetFields()[0].GetValue().GetDoubleValue() != 101.25 {
		t.Fatalf("read rsp=%v err=%v", readRsp, err)
	}
}

func successRetInfo() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}
}

type mutableSnapshotProvider struct {
	current metadata.RequestSnapshot
	calls   int
}

func (p *mutableSnapshotProvider) RequestSnapshot() metadata.RequestSnapshot {
	p.calls++
	snapshot := p.current
	p.current = &testRequestSnapshot{generation: "generation-b"}
	return snapshot
}

type testRequestSnapshot struct {
	generation string
}

func (*testRequestSnapshot) GetDataset(string, string) (*pb.Dataset, bool) {
	return nil, false
}

func (*testRequestSnapshot) GetDataNode(string) (*pb.DataNode, bool) {
	return nil, false
}

func (*testRequestSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}
