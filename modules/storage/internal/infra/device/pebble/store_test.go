package pebble

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestReadRowsDescPageStopsAtRequestedWindow(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	for _, version := range []string{
		"2026-07-09T08:10:00.000000000Z",
		"2026-07-09T08:11:00.000000000Z",
		"2026-07-09T08:12:00.000000000Z",
		"2026-07-09T08:13:00.000000000Z",
		"2026-07-09T08:14:00.000000000Z",
	} {
		if err := store.WriteRows(ctx, []*pb.PrimaryStoreRow{testPrimaryTimeSeriesRow(version)}); err != nil {
			t.Fatalf("WriteRows %s: %v", version, err)
		}
	}

	rows, page, err := store.ReadRows(ctx, []*pb.PrimaryStoreKey{testPrimaryTimeSeriesKey("")}, nil, pb.SortOrder_SORT_ORDER_DESC, nil, &pb.Page{Page: 2, Size: 2})
	if err != nil {
		t.Fatalf("ReadRows: %v", err)
	}
	if len(rows) != 2 ||
		rows[0].GetKey().GetVersion() != "2026-07-09T08:12:00.000000000Z" ||
		rows[1].GetKey().GetVersion() != "2026-07-09T08:11:00.000000000Z" {
		t.Fatalf("versions = %v, want second descending page", primaryVersions(rows))
	}
	if page == nil || !page.GetHasMore() || page.GetTotal() != 0 || page.GetTotalState() != pb.TotalState_SKIPPED {
		t.Fatalf("page = %+v, want skipped total with has_more", page)
	}
}

func TestScanRowsFirstPageUsesBoundedCursor(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	for i := 0; i < 100; i++ {
		version := time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		if err := store.WriteRows(ctx, []*pb.PrimaryStoreRow{testPrimaryTimeSeriesRow(version)}); err != nil {
			t.Fatalf("WriteRows %s: %v", version, err)
		}
	}

	rows, page, err := store.ScanRows(ctx, &pb.PrimaryStoreTarget{
		SpaceId: "crypto", DatasetId: "binance_spot_kline",
	}, pb.DataKind_DATA_KIND_TIME_SERIES, nil, pb.SortOrder_SORT_ORDER_ASC, nil, &pb.Page{Size: 2})
	if err != nil {
		t.Fatalf("ScanRows: %v", err)
	}
	if len(rows) != 2 || page == nil || !page.GetHasMore() || page.GetNextCursor() == "" {
		t.Fatalf("rows=%d page=%+v, want bounded first page with cursor", len(rows), page)
	}
	rows, page, err = store.ScanRows(ctx, &pb.PrimaryStoreTarget{
		SpaceId: "crypto", DatasetId: "binance_spot_kline",
	}, pb.DataKind_DATA_KIND_TIME_SERIES, nil, pb.SortOrder_SORT_ORDER_ASC, nil, &pb.Page{Size: 2, Cursor: page.GetNextCursor()})
	if err != nil {
		t.Fatalf("ScanRows next page: %v", err)
	}
	if len(rows) != 2 || page == nil || page.GetNextCursor() == "" {
		t.Fatalf("next rows=%d page=%+v, want another bounded page", len(rows), page)
	}
}

func TestDeleteRowsRemovesExactPrimaryKeys(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := testPrimaryTimeSeriesRow("2026-07-09T08:10:00.000000000Z")
	newer := testPrimaryTimeSeriesRow("2026-07-09T08:11:00.000000000Z")
	if err := store.WriteRows(ctx, []*pb.PrimaryStoreRow{old, newer}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRows(ctx, []*pb.PrimaryStoreKey{old.GetKey()}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := store.ReadRows(ctx, []*pb.PrimaryStoreKey{testPrimaryTimeSeriesKey("")}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, &pb.Page{Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].GetKey().GetVersion() != newer.GetKey().GetVersion() {
		t.Fatalf("rows after delete=%v", primaryVersions(rows))
	}
}

func testPrimaryTimeSeriesRow(version string) *pb.PrimaryStoreRow {
	return &pb.PrimaryStoreRow{Key: testPrimaryTimeSeriesKey(version)}
}

func testPrimaryTimeSeriesKey(version string) *pb.PrimaryStoreKey {
	return &pb.PrimaryStoreKey{
		SpaceId:   "crypto",
		DatasetId: "binance_spot_kline",
		DataKind:  pb.DataKind_DATA_KIND_TIME_SERIES,
		Key:       "BTC-USDT|1m|_",
		Version:   version,
	}
}

func primaryVersions(rows []*pb.PrimaryStoreRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.GetKey().GetVersion())
	}
	return out
}
