package builder

import (
	"context"
	"errors"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestNewAccessReaderReturnsLocalWhenServiceNameEmpty(t *testing.T) {
	local := &buildingGuardReader{}
	got := NewAccessReader(local, "", "")
	if got != local {
		t.Fatal("expected local reader when service name is empty")
	}
}

func TestNewAccessReaderReturnsMissingReaderWhenUnset(t *testing.T) {
	got := NewAccessReader(nil, "", "")
	if _, ok := got.(missingAccessReader); !ok {
		t.Fatalf("reader type = %T, want missingAccessReader", got)
	}
}

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
	readRecordCalled     bool
	timeSeriesScan       *pb.ScanTimeSeriesRowsReq
	recordScan           *pb.ScanRecordRowsReq
}

func (p *scanAccessProxy) WriteTimeSeriesRows(context.Context, *pb.WriteTimeSeriesRowsReq, ...client.Option) (*pb.WriteTimeSeriesRowsRsp, error) {
	return nil, errors.New("not used")
}

func (p *scanAccessProxy) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq, ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	p.readTimeSeriesCalled = true
	return nil, errors.New("public read must not be used for internal scan")
}

func (p *scanAccessProxy) DeleteTimeSeriesRows(context.Context, *pb.DeleteTimeSeriesRowsReq, ...client.Option) (*pb.DeleteTimeSeriesRowsRsp, error) {
	return nil, errors.New("not used")
}

func (p *scanAccessProxy) WriteRecordRows(context.Context, *pb.WriteRecordRowsReq, ...client.Option) (*pb.WriteRecordRowsRsp, error) {
	return nil, errors.New("not used")
}

func (p *scanAccessProxy) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq, ...client.Option) (*pb.ReadRecordRowsRsp, error) {
	p.readRecordCalled = true
	return nil, errors.New("public read must not be used for internal scan")
}

func (p *scanAccessProxy) ScanTimeSeriesRows(_ context.Context, req *pb.ScanTimeSeriesRowsReq, _ ...client.Option) (*pb.ScanTimeSeriesRowsRsp, error) {
	p.timeSeriesScan = req
	return &pb.ScanTimeSeriesRowsRsp{
		RetInfo:    &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
		Rows:       []*pb.TimeSeriesRow{{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline"}}},
		PageResult: &pb.PageResult{Size: 500, HasMore: true, NextCursor: "cursor-2"},
	}, nil
}

func (p *scanAccessProxy) ScanRecordRows(_ context.Context, req *pb.ScanRecordRowsReq, _ ...client.Option) (*pb.ScanRecordRowsRsp, error) {
	p.recordScan = req
	return &pb.ScanRecordRowsRsp{
		RetInfo:    &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
		Rows:       []*pb.RecordRow{{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}}},
		PageResult: &pb.PageResult{Size: 500, HasMore: true, NextCursor: "cursor-2"},
	}, nil
}

func TestRemoteAccessReaderScanRecordRowsUsesCursor(t *testing.T) {
	proxy := &scanAccessProxy{}
	reader := &remoteAccessReader{proxy: proxy, scanProxy: proxy}

	rows, page, err := reader.ScanRecordRows(context.Background(), "crypto", "news", nil, []string{"title"}, &pb.Page{Cursor: "c1"})
	if err != nil {
		t.Fatalf("ScanRecordRows: %v", err)
	}
	if proxy.readRecordCalled {
		t.Fatal("remote scan called public ReadRecordRows")
	}
	if proxy.recordScan == nil || proxy.recordScan.GetPage().GetCursor() != "c1" {
		t.Fatalf("scan request = %+v", proxy.recordScan)
	}
	if len(rows) != 1 || page.GetNextCursor() != "cursor-2" {
		t.Fatalf("rows=%d page=%+v", len(rows), page)
	}
}
