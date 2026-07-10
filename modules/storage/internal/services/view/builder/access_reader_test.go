package builder

import (
	"context"
	"errors"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestRemoteAccessReaderUsesCursorScanRPC(t *testing.T) {
	proxy := &scanAccessProxy{}
	reader := &remoteAccessReader{proxy: proxy, scanProxy: proxy}

	rows, page, err := reader.ScanTimeSeriesRows(context.Background(), "crypto", "kline", &pb.TimeRange{
		StartTime: "2026-07-01T00:00:00Z",
		EndTime:   "2026-07-10T00:00:00Z",
	}, []string{"close"}, &pb.Page{Size: 500, Cursor: "cursor-1"})
	if err != nil {
		t.Fatalf("ScanTimeSeriesRows: %v", err)
	}
	if proxy.readTimeSeriesCalled {
		t.Fatal("remote scan called public ReadTimeSeriesRows")
	}
	if proxy.timeSeriesScan == nil || proxy.timeSeriesScan.GetPage().GetCursor() != "cursor-1" {
		t.Fatalf("scan request = %+v", proxy.timeSeriesScan)
	}
	if len(rows) != 1 || page.GetNextCursor() != "cursor-2" {
		t.Fatalf("rows=%d page=%+v", len(rows), page)
	}
}

type scanAccessProxy struct {
	readTimeSeriesCalled bool
	timeSeriesScan       *pb.ScanTimeSeriesRowsReq
}

func (p *scanAccessProxy) WriteTimeSeriesRows(context.Context, *pb.WriteTimeSeriesRowsReq, ...client.Option) (*pb.WriteTimeSeriesRowsRsp, error) {
	return nil, errors.New("not used")
}

func (p *scanAccessProxy) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq, ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	p.readTimeSeriesCalled = true
	return nil, errors.New("public read must not be used for internal scan")
}

func (p *scanAccessProxy) WriteRecordRows(context.Context, *pb.WriteRecordRowsReq, ...client.Option) (*pb.WriteRecordRowsRsp, error) {
	return nil, errors.New("not used")
}

func (p *scanAccessProxy) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq, ...client.Option) (*pb.ReadRecordRowsRsp, error) {
	return nil, errors.New("not used")
}

func (p *scanAccessProxy) ScanTimeSeriesRows(_ context.Context, req *pb.ScanTimeSeriesRowsReq, _ ...client.Option) (*pb.ScanTimeSeriesRowsRsp, error) {
	p.timeSeriesScan = req
	return &pb.ScanTimeSeriesRowsRsp{
		RetInfo:    &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
		Rows:       []*pb.TimeSeriesRow{{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline"}}},
		PageResult: &pb.PageResult{Size: 500, HasMore: true, NextCursor: "cursor-2"},
	}, nil
}

func (p *scanAccessProxy) ScanRecordRows(context.Context, *pb.ScanRecordRowsReq, ...client.Option) (*pb.ScanRecordRowsRsp, error) {
	return nil, errors.New("not used")
}
