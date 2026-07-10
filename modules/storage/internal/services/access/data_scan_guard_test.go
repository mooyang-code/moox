package access

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestScanAllPrimaryRowsStopsAtGuardLimit(t *testing.T) {
	svc := &Service{primary: fakePrimaryScanner{rowsPerPage: 1000, pages: (maxDatasetScanRows / 1000) + 2}}

	_, err := svc.scanAllPrimaryRows(context.Background(), nil, &pb.PrimaryStoreTarget{},
		pb.DataKind_DATA_KIND_TIME_SERIES, nil, nil)
	if err == nil {
		t.Fatal("scanAllPrimaryRows() error = nil, want broad scan guard error")
	}
	if got := err.Error(); got == "" || !containsAll(got, "dataset scan", fmt.Sprint(maxDatasetScanRows)) {
		t.Fatalf("scanAllPrimaryRows() error = %q, want dataset scan guard with limit", got)
	}
}

func TestScanTimeSeriesRowsPagesPrimaryStoreWithoutDatasetGuard(t *testing.T) {
	svc := &Service{
		primary: fakePrimaryScanner{rowsPerPage: 1000, pages: (maxDatasetScanRows / 1000) + 2},
		router:  router.NewResolver(fakeRouteReader{}),
	}

	rows, page, err := svc.FactReader().ScanTimeSeriesRows(context.Background(), "crypto", "kline",
		&pb.TimeRange{StartTime: "2026-07-09T00:00:00Z"}, nil, &pb.Page{Size: 1000})
	if err != nil {
		t.Fatalf("ScanTimeSeriesRows() error = %v, want paged scan to bypass broad dataset guard", err)
	}
	if len(rows) != 1000 {
		t.Fatalf("ScanTimeSeriesRows() rows = %d, want one page of 1000 rows", len(rows))
	}
	if page == nil || !page.GetHasMore() || page.GetNextCursor() == "" {
		t.Fatalf("ScanTimeSeriesRows() page = %+v, want has_more with next cursor", page)
	}
}

type fakePrimaryScanner struct {
	rowsPerPage int
	pages       int
}

func (f fakePrimaryScanner) WriteRows(context.Context, *pb.PrimaryStoreTarget, []*pb.PrimaryStoreRow) error {
	return nil
}

func (f fakePrimaryScanner) ReadRows(context.Context, *pb.PrimaryStoreTarget, *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func (f fakePrimaryScanner) ScanRows(_ context.Context, _ *pb.PrimaryStoreTarget, req *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	pageNo := uint32(1)
	if req.GetPage().GetCursor() != "" {
		_, _ = fmt.Sscanf(req.GetPage().GetCursor(), "%d", &pageNo)
	}
	rows := make([]*pb.PrimaryStoreRow, f.rowsPerPage)
	for i := range rows {
		rows[i] = &pb.PrimaryStoreRow{Key: &pb.PrimaryStoreKey{
			SpaceId:   "crypto",
			DatasetId: "kline",
			DataKind:  req.GetDataKind(),
			Key:       factkey.BuildTimeSeriesDataKey(fmt.Sprintf("sub-%d-%d", pageNo, i), "1m", nil),
			Version:   fmt.Sprintf("2026-07-09T00:00:%02dZ", i%60),
		}}
	}
	next := pageNo + 1
	return rows, &pb.PageResult{
		HasMore:    int(pageNo) < f.pages,
		NextCursor: fmt.Sprint(next),
	}, nil
}

type fakeRouteReader struct{}

func (fakeRouteReader) ListPrimaryStoreRoutes(context.Context, string, string, string, string, *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error) {
	return []*pb.PrimaryStoreRoute{{
		SpaceId:        "crypto",
		DatasetId:      "kline",
		RouteId:        "route-1",
		SubjectPattern: "*",
		NodeId:         "node-1",
		Status:         "active",
	}}, &pb.PageResult{}, nil
}

func (fakeRouteReader) GetPrimaryStoreNode(context.Context, string) (*pb.PrimaryStoreNode, error) {
	return &pb.PrimaryStoreNode{NodeId: "node-1", Status: "active"}, nil
}

func (fakeRouteReader) ListDevices(context.Context, string, string, *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	return []*pb.Device{{DeviceId: "dev-1", Engine: "pebble", Status: "active"}}, &pb.PageResult{}, nil
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
