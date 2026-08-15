package hostmetrics

import (
	"context"
	"testing"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type readerAccessFake struct {
	calls     int
	rows      []*storagepb.TimeSeriesRow
	lastStart string
	lastEnd   string
}

func (f *readerAccessFake) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	f.calls++
	f.lastStart, f.lastEnd = req.GetTimeRange().GetStartTime(), req.GetTimeRange().GetEndTime()
	if len(req.GetSelectors()) != 1 || req.GetSelectors()[0].SeriesTag != nil {
		panic("host history must use one wildcard series-tag selector")
	}
	dataset := req.GetSelectors()[0].GetDatasetId()
	if req.GetSpaceId() != req.GetSelectors()[0].GetSpaceId() || req.GetDatasetId() != dataset {
		panic("host history must repeat the selector scope at request level")
	}
	var rows []*storagepb.TimeSeriesRow
	for _, row := range f.rows {
		if row.GetKey().GetDatasetId() == dataset {
			rows = append(rows, row)
		}
	}
	pageNo := int(req.GetPage().GetPage())
	if pageNo <= 0 {
		pageNo = 1
	}
	start, end := pageNo-1, len(rows)
	if start >= len(rows) {
		return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
	}
	if end > start+1 {
		end = start + 1
	}
	page := &storagepb.PageResult{Page: uint32(pageNo), Size: 1, HasMore: end < len(rows)}
	return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Rows: rows[start:end], PageResult: page}, nil
}

func TestStorageReaderPaginatesAndRebuildsEntities(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	at := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	fake := &readerAccessFake{rows: []*storagepb.TimeSeriesRow{
		readRow(resourceRow(SpaceID, cfg.ResourceDatasetID, "1m", at, &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 4}, Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100}}, "agent-1")),
		readRow(filesystemRow(SpaceID, cfg.FilesystemDatasetID, "1m", at, &hostmetricpb.FilesystemMetric{Device: "sda1", Mountpoint: "/", UsagePercent: 70}, "agent-1")),
		readRow(filesystemRow(SpaceID, cfg.FilesystemDatasetID, "1m", at, &hostmetricpb.FilesystemMetric{Device: "sdb1", Mountpoint: "/data", UsagePercent: 20}, "agent-1")),
	}}
	reader := NewStorageReader(fake, cfg)
	points, err := reader.History(context.Background(), "agent-1", time.Unix(0, 0), time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || len(points[0].Snapshot.GetFilesystems()) != 2 || fake.calls < 5 {
		t.Fatalf("points=%+v calls=%d", points, fake.calls)
	}
	start, _ := time.Parse(time.RFC3339Nano, fake.lastStart)
	end, _ := time.Parse(time.RFC3339Nano, fake.lastEnd)
	if end.Sub(start) > 7*24*time.Hour {
		t.Fatalf("reader exceeded seven-day bound: %s to %s", fake.lastStart, fake.lastEnd)
	}
}

func TestStorageReaderDoesNotTruncateLongMultiEntityHistory(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	now := time.Now().UTC().Truncate(time.Minute)
	rows := make([]*storagepb.TimeSeriesRow, 0, 30)
	for minute := 0; minute < 30; minute++ {
		at := now.Add(time.Duration(minute-30) * time.Minute).Format(time.RFC3339Nano)
		rows = append(rows, readRow(resourceRow(SpaceID, cfg.ResourceDatasetID, "1m", at, &hostmetricpb.HostSnapshot{
			Cpu: &hostmetricpb.CpuMetric{LogicalCores: 4}, Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100},
		}, "agent-1")))
	}
	fake := &readerAccessFake{rows: rows}
	points, err := NewStorageReader(fake, cfg).History(
		context.Background(), "agent-1",
		now.Add(-time.Hour),
		now,
		100,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 30 {
		t.Fatalf("points=%d, want 30", len(points))
	}
}

func TestStorageReaderIncludesLegacyRowsForCompactAgentID(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	at := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	legacyID := "550e8400-e29b-41d4-a716-446655440000"
	fake := &readerAccessFake{rows: []*storagepb.TimeSeriesRow{
		readRow(resourceRow(SpaceID, cfg.ResourceDatasetID, "1m", at, &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 4}, Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100}}, legacyID)),
	}}
	reader := NewStorageReader(fake, cfg)
	reader.SetAgentAliases(func(context.Context, string) ([]string, error) {
		return []string{"aB3x", legacyID}, nil
	})
	points, err := reader.History(context.Background(), "aB3x", time.Unix(0, 0), time.Now().UTC(), 10)
	require.NoError(t, err)
	require.Len(t, points, 1)
	require.Equal(t, "aB3x", points[0].AgentID)
}

func readRow(row *storagepb.RowFieldUpsert) *storagepb.TimeSeriesRow {
	key := row.GetKey()
	attributes := make(map[string]string, len(row.GetAttributes()))
	for name, value := range row.GetAttributes() {
		attributes[name] = value.GetStringValue()
	}
	return &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{
			SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(),
			SubjectId: key.GetTimeSeries().GetSubjectId(), Freq: key.GetTimeSeries().GetFreq(), DataTime: key.GetTimeSeries().GetDataTime(), SeriesTag: key.GetTimeSeries().GetSeriesTag(),
		},
		Fields: row.GetFields(), Attributes: attributes,
	}
}
