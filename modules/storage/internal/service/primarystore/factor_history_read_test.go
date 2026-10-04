package primarystore

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

type factorHistoryReadNode struct {
	req *pb.ReadTimeSeriesRowsReq
}

func (*factorHistoryReadNode) UpsertFields(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	return &pb.UpsertFieldsRsp{RetInfo: successRetInfo()}, nil
}

func (*factorHistoryReadNode) ReadFields(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	return nil, nil
}

func (n *factorHistoryReadNode) ReadTimeSeriesRows(_ context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	n.req = req
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: successRetInfo()}, nil
}

func TestFactorRangeReadUsesDataNodeHistoryAndForwardsCursor(t *testing.T) {
	node := &factorHistoryReadNode{}
	var signed bool
	svc, err := New(Options{
		Resolver:   func(context.Context, string, string) (DataNodeClient, error) { return node, nil },
		Authorizer: func(*pb.AuthInfo) error { return nil },
		AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
			signed = true
			return &pb.AuthInfo{AppId: "storage", AppKey: "signed"}, nil
		},
		View: func(context.Context, string, string) (pb.DataViewService, string, error) {
			t.Fatal("factor range reads must not use a DataView")
			return nil, "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cursor := []byte("encoded-row-key")
	selectors := []*pb.TimeSeriesSelector{{
		SpaceId: "crypto", DatasetId: "dataset_binance_kline_1m", SubjectId: "BTC-USDT", Freq: "1m",
	}}
	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor", AppKey: "factor-key"},
		SpaceId:  "crypto", DatasetId: "dataset_binance_kline_1m", Selectors: selectors,
		TimeRange: &pb.TimeRange{StartTime: "2026-10-04T00:00:00Z", EndTime: "2026-10-04T00:01:00Z"},
		Order:     pb.SortOrder_SORT_ORDER_ASC, ColumnNames: []string{"close"},
		Page: &commonpb.Page{Page: 1, Size: 2000}, AfterKey: cursor,
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("response=%v error=%v", rsp, err)
	}
	if !signed || node.req == nil {
		t.Fatalf("signed=%t history request=%v", signed, node.req)
	}
	if node.req.GetAuthInfo().GetAppKey() != "signed" || node.req.GetSpaceId() != "crypto" ||
		node.req.GetDatasetId() != "dataset_binance_kline_1m" || len(node.req.GetSelectors()) != 1 ||
		node.req.GetTimeRange().GetStartTime() != "2026-10-04T00:00:00Z" ||
		node.req.GetPage().GetSize() != 2000 || string(node.req.GetAfterKey()) != string(cursor) {
		t.Fatalf("history request lost factor read contract: %v", node.req)
	}
}

func TestFactorRangeReadRequiresBoundedPagedRequest(t *testing.T) {
	node := &factorHistoryReadNode{}
	svc, err := New(Options{
		Node:       node,
		Authorizer: func(*pb.AuthInfo) error { return nil },
		View: func(context.Context, string, string) (pb.DataViewService, string, error) {
			t.Fatal("invalid factor history read must not fall through to a DataView")
			return nil, "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"},
		SpaceId:  "crypto", DatasetId: "dataset_binance_kline_1m",
		Selectors: []*pb.TimeSeriesSelector{{
			SpaceId: "crypto", DatasetId: "dataset_binance_kline_1m", SubjectId: "BTC-USDT", Freq: "1m",
		}},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("response=%v error=%v, want INVALID_PARAM", rsp, err)
	}
	if node.req != nil {
		t.Fatalf("invalid factor request reached DataNode: %v", node.req)
	}
}
