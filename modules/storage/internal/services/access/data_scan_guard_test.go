package access

import (
	"context"
	"fmt"
	"strings"
	"testing"

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
			Key:       fmt.Sprintf("row-%d-%d", pageNo, i),
			Version:   fmt.Sprintf("%d", pageNo),
		}}
	}
	next := pageNo + 1
	return rows, &pb.PageResult{
		HasMore:    int(pageNo) < f.pages,
		NextCursor: fmt.Sprint(next),
	}, nil
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
