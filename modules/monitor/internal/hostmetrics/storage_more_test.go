package hostmetrics

import (
	"context"
	"errors"
	"testing"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type deleteAccessFake struct {
	err      error
	deleted  uint32
	failCode bool
	calls    int
}

func (f *deleteAccessFake) DeleteTimeSeriesRows(_ context.Context, _ *storagepb.DeleteTimeSeriesRowsReq, _ ...client.Option) (*storagepb.DeleteTimeSeriesRowsRsp, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.failCode {
		return &storagepb.DeleteTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: "fail"}}, nil
	}
	return &storagepb.DeleteTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Deleted: f.deleted}, nil
}

func TestPruneDeletesAcrossDatasets(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	fake := &deleteAccessFake{deleted: 3}
	n, err := Prune(context.Background(), fake, cfg, time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, uint32(12), n)
	assert.Equal(t, 4, fake.calls)
}

func TestPruneRejectsBadAccess(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	_, err := Prune(context.Background(), nil, cfg, time.Now().UTC())
	require.Error(t, err)
	_, err = Prune(context.Background(), "not-client", cfg, time.Now().UTC())
	require.Error(t, err)
}

func TestPrunePropagatesErrors(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	_, err := Prune(context.Background(), &deleteAccessFake{err: errors.New("down")}, cfg, time.Now().UTC())
	require.Error(t, err)
	_, err = Prune(context.Background(), &deleteAccessFake{failCode: true}, cfg, time.Now().UTC())
	require.Error(t, err)
}

func TestStorageWriterNilAndErrorPaths(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	require.Error(t, (*StorageWriter)(nil).WriteSnapshot(context.Background(), &hostmetricpb.HostSnapshot{}, "a", time.Now(), "m"))
	require.Error(t, NewStorageWriter(nil, cfg).WriteSnapshot(context.Background(), &hostmetricpb.HostSnapshot{}, "a", time.Now(), "m"))
	require.Error(t, NewStorageWriter(&writerAccessFake{}, cfg).WriteSnapshot(context.Background(), nil, "a", time.Now(), "m"))
	require.Error(t, NewStorageWriter(&writerAccessFake{}, cfg).WriteSnapshot(context.Background(), &hostmetricpb.HostSnapshot{}, "", time.Now(), "m"))

	failing := &writerAccessFailFake{err: errors.New("write failed")}
	err := NewStorageWriter(failing, cfg).WriteSnapshot(context.Background(), &hostmetricpb.HostSnapshot{
		Cpu: &hostmetricpb.CpuMetric{}, Memory: &hostmetricpb.MemoryMetric{},
	}, "agent-1", time.Now().UTC(), "m1")
	require.Error(t, err)

	failing = &writerAccessFailFake{badRet: true}
	err = NewStorageWriter(failing, cfg).WriteSnapshot(context.Background(), &hostmetricpb.HostSnapshot{
		Cpu: &hostmetricpb.CpuMetric{}, Memory: &hostmetricpb.MemoryMetric{},
	}, "agent-1", time.Now().UTC(), "m1")
	require.Error(t, err)
}

type writerAccessFailFake struct {
	err    error
	badRet bool
}

func (f *writerAccessFailFake) WriteTimeSeriesRows(_ context.Context, _ *storagepb.WriteTimeSeriesRowsReq, _ ...client.Option) (*storagepb.WriteTimeSeriesRowsRsp, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.badRet {
		return &storagepb.WriteTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: "no"}}, nil
	}
	return &storagepb.WriteTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func TestStorageWriterIncludesRateColumnsWhenAvailable(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	fake := &writerAccessFake{}
	writer := NewStorageWriter(fake, cfg)
	snapshot := &hostmetricpb.HostSnapshot{
		Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 2, UsagePercent: 10, UsageAvailable: true},
		Memory: &hostmetricpb.MemoryMetric{TotalBytes: 10, UsedBytes: 4, AvailableBytes: 6, UsagePercent: 40},
		Disks: []*hostmetricpb.DiskMetric{{
			Device: "sda", RateAvailable: true, ReadBytesPerSecond: 1.5, WriteBytesPerSecond: 2.5,
			ReadIops: 3, WriteIops: 4, UtilizationPercent: 5,
		}},
		Networks: []*hostmetricpb.NetworkMetric{{
			Device: "eth0", RateAvailable: true, ReceiveBytesPerSecond: 6, TransmitBytesPerSecond: 7,
		}},
		Filesystems: []*hostmetricpb.FilesystemMetric{{Device: "sda1", Mountpoint: "/", FsType: "ext4"}},
	}
	require.NoError(t, writer.WriteSnapshot(context.Background(), snapshot, "agent-1", time.Now().UTC(), "m1"))
	require.Len(t, fake.requests, 4)
	foundRate := false
	for _, req := range fake.requests {
		for _, row := range req.GetRows() {
			for _, col := range row.GetColumns() {
				if col.GetColumnName() == "read_bytes_per_second" || col.GetColumnName() == "receive_bytes_per_second" {
					foundRate = true
				}
			}
		}
	}
	assert.True(t, foundRate)
}

func TestStorageReaderValidationAndMerge(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	_, err := (*StorageReader)(nil).History(context.Background(), "a", time.Now().Add(-time.Hour), time.Now(), 10)
	require.Error(t, err)
	_, err = NewStorageReader(nil, cfg).History(context.Background(), "a", time.Now().Add(-time.Hour), time.Now(), 10)
	require.Error(t, err)
	_, err = NewStorageReader(&readerAccessFake{}, cfg).History(context.Background(), "", time.Now().Add(-time.Hour), time.Now(), 10)
	require.Error(t, err)
	_, err = NewStorageReader(&readerAccessFake{}, cfg).History(context.Background(), "a", time.Now(), time.Now().Add(-time.Hour), 10)
	require.Error(t, err)

	at := time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339Nano)
	fake := &readerAccessFake{rows: []*storagepb.TimeSeriesRow{
		resourceRow(SpaceID, cfg.ResourceDatasetID, "1m", at, &hostmetricpb.HostSnapshot{
			Cpu: &hostmetricpb.CpuMetric{LogicalCores: 8, UsagePercent: 11, UsageAvailable: true},
			Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 40, AvailableBytes: 60, UsagePercent: 40},
		}, "agent-1", "m1"),
		diskRow(SpaceID, cfg.DiskDatasetID, "1m", at, &hostmetricpb.DiskMetric{Device: "sda", ReadBytesTotal: 9, RateAvailable: true}, "agent-1", "m1"),
		networkRow(SpaceID, cfg.NetworkDatasetID, "1m", at, &hostmetricpb.NetworkMetric{Device: "eth0", Operstate: "up", ReceiveBytesTotal: 1}, "agent-1", "m1"),
	}}
	points, err := NewStorageReader(fake, cfg).History(context.Background(), "agent-1", time.Now().Add(-time.Hour), time.Now().UTC(), 0)
	require.NoError(t, err)
	require.Len(t, points, 1)
	assert.Equal(t, uint32(8), points[0].Snapshot.GetCpu().GetLogicalCores())
	require.Len(t, points[0].Snapshot.GetDisks(), 1)
	require.Len(t, points[0].Snapshot.GetNetworks(), 1)

	errFake := &readerAccessErrFake{err: errors.New("read fail")}
	_, err = NewStorageReader(errFake, cfg).History(context.Background(), "agent-1", time.Now().Add(-time.Hour), time.Now().UTC(), 10)
	require.Error(t, err)

	snap := &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{}, Memory: &hostmetricpb.MemoryMetric{}}
	require.Error(t, mergeRow(snap, "unknown", cfg, &storagepb.TimeSeriesRow{Key: &storagepb.TimeSeriesKey{}}))
}

type readerAccessErrFake struct{ err error }

func (f *readerAccessErrFake) ReadTimeSeriesRows(_ context.Context, _ *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	return nil, f.err
}
