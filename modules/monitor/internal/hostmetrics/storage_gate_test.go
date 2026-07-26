package hostmetrics

import (
	"context"
	"errors"
	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
	"trpc.group/trpc-go/trpc-go/client"
)

type gateMetadataFake struct {
	spaceErr   error
	space      *storagepb.Space
	datasetErr error
	dataset    *storagepb.Dataset
	nodeErr    error
	node       *storagepb.DataNode
	columnsErr error
	columns    []*storagepb.DatasetColumn
	spaceRet   *commonpb.RetInfo
	datasetRet *commonpb.RetInfo
	nodeRet    *commonpb.RetInfo
	columnsRet *commonpb.RetInfo
}

func (f *gateMetadataFake) GetSpace(_ context.Context, _ *storagepb.GetSpaceReq, _ ...client.Option) (*storagepb.GetSpaceRsp, error) {
	if f.spaceErr != nil {
		return nil, f.spaceErr
	}
	ret := f.spaceRet
	if ret == nil {
		ret = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
	}
	return &storagepb.GetSpaceRsp{RetInfo: ret, Space: f.space}, nil
}

func (f *gateMetadataFake) GetDataset(_ context.Context, _ *storagepb.GetDatasetReq, _ ...client.Option) (*storagepb.GetDatasetRsp, error) {
	if f.datasetErr != nil {
		return nil, f.datasetErr
	}
	ret := f.datasetRet
	if ret == nil {
		ret = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
	}
	return &storagepb.GetDatasetRsp{RetInfo: ret, Dataset: f.dataset}, nil
}

func (f *gateMetadataFake) ListDatasetColumns(_ context.Context, _ *storagepb.ListDatasetColumnsReq, _ ...client.Option) (*storagepb.ListDatasetColumnsRsp, error) {
	if f.columnsErr != nil {
		return nil, f.columnsErr
	}
	ret := f.columnsRet
	if ret == nil {
		ret = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
	}
	return &storagepb.ListDatasetColumnsRsp{RetInfo: ret, Columns: f.columns}, nil
}

func (f *gateMetadataFake) GetDataNode(_ context.Context, _ *storagepb.GetDataNodeReq, _ ...client.Option) (*storagepb.GetDataNodeRsp, error) {
	if f.nodeErr != nil {
		return nil, f.nodeErr
	}
	ret := f.nodeRet
	if ret == nil {
		ret = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
	}
	return &storagepb.GetDataNodeRsp{RetInfo: ret, Node: f.node}, nil
}

func hostGateCfg() monconfig.HostStorageConfig {
	return monconfig.Default().Metrics.HostStorage
}

func activeColumn(name string, vt storagepb.FieldValueType) *storagepb.DatasetColumn {
	return &storagepb.DatasetColumn{ColumnName: name, ValueType: vt, Status: "active"}
}

func resourceColumns(cfg monconfig.HostStorageConfig) []*storagepb.DatasetColumn {
	_ = cfg
	return []*storagepb.DatasetColumn{
		activeColumn("agent_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING),
		activeColumn("logical_cores", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("memory_total_bytes", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("memory_used_bytes", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("memory_available_bytes", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("cpu_usage_available", storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL),
		activeColumn("memory_usage_percent", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE),
	}
}

func filesystemColumns() []*storagepb.DatasetColumn {
	return []*storagepb.DatasetColumn{
		activeColumn("device", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING),
		activeColumn("mountpoint", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING),
		activeColumn("fs_type", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING),
		activeColumn("total_bytes", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("used_bytes", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("available_bytes", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("usage_percent", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE),
		activeColumn("read_only", storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL),
	}
}

func diskColumns() []*storagepb.DatasetColumn {
	return []*storagepb.DatasetColumn{
		activeColumn("device", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING),
		activeColumn("read_bytes_total", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("write_bytes_total", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("read_ops_total", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("write_ops_total", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("io_time_ms_total", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("rate_available", storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL),
	}
}

func networkColumns() []*storagepb.DatasetColumn {
	return []*storagepb.DatasetColumn{
		activeColumn("device", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING),
		activeColumn("operstate", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING),
		activeColumn("receive_bytes_total", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("transmit_bytes_total", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("receive_errors_total", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("transmit_errors_total", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("receive_dropped_total", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("transmit_dropped_total", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT),
		activeColumn("rate_available", storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL),
	}
}

func validGateFake(cfg monconfig.HostStorageConfig) *gateMetadataFake {
	cols := map[string][]*storagepb.DatasetColumn{
		cfg.ResourceDatasetID:   resourceColumns(cfg),
		cfg.FilesystemDatasetID: filesystemColumns(),
		cfg.DiskDatasetID:       diskColumns(),
		cfg.NetworkDatasetID:    networkColumns(),
	}
	return &gateMetadataFake{
		space:   &storagepb.Space{SpaceId: cfg.SpaceID, Status: "active"},
		dataset: &storagepb.Dataset{Status: "active", BindingLocked: true, DataNodeId: "storage-node-0", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}},
		node:    &storagepb.DataNode{NodeId: "storage-node-0", Status: "active"},
		columns: append(append(append(cols[cfg.ResourceDatasetID], cols[cfg.FilesystemDatasetID]...), cols[cfg.DiskDatasetID]...), cols[cfg.NetworkDatasetID]...),
	}
}

// columnsFor returns the column set for the dataset currently being validated.
// Validate walks datasets in order; the fake returns the full union so hasHostColumns
// still succeeds for each dataset (extra columns are ignored).
func TestStorageGateValidateSuccess(t *testing.T) {
	cfg := hostGateCfg()
	fake := validGateFake(cfg)
	gate := NewStorageGate(fake, cfg)
	require.NoError(t, gate.Validate(context.Background()))
	assert.True(t, gate.Ready())
	status := gate.Status()
	assert.True(t, status.Valid)
	assert.Empty(t, status.Error)
	assert.False(t, status.CheckedAt.IsZero())
}

func TestStorageGateNilAndContractErrors(t *testing.T) {
	assert.Error(t, (*StorageGate)(nil).Validate(context.Background()))
	assert.False(t, (*StorageGate)(nil).Ready())
	assert.Contains(t, (*StorageGate)(nil).Status().Error, "nil")

	cfg := hostGateCfg()
	gate := NewStorageGate(nil, cfg)
	assert.Error(t, gate.Validate(context.Background()))
	assert.False(t, gate.Ready())

	bad := cfg
	bad.SpaceID = "crypto"
	gate = NewStorageGate(&gateMetadataFake{}, bad)
	err := gate.Validate(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "space")
	assert.False(t, gate.Ready())
}

func TestStorageGatePropagatesMetadataFailures(t *testing.T) {
	cfg := hostGateCfg()
	ctx := context.Background()

	t.Run("get_space_error", func(t *testing.T) {
		fake := validGateFake(cfg)
		fake.spaceErr = errors.New("space down")
		gate := NewStorageGate(fake, cfg)
		require.Error(t, gate.Validate(ctx))
		assert.False(t, gate.Ready())
	})
	t.Run("inactive_space", func(t *testing.T) {
		fake := validGateFake(cfg)
		fake.space = &storagepb.Space{SpaceId: cfg.SpaceID, Status: "disabled"}
		gate := NewStorageGate(fake, cfg)
		require.Error(t, gate.Validate(ctx))
	})
	t.Run("get_dataset_error", func(t *testing.T) {
		fake := validGateFake(cfg)
		fake.datasetErr = errors.New("dataset down")
		gate := NewStorageGate(fake, cfg)
		require.Error(t, gate.Validate(ctx))
	})
	t.Run("bad_freq", func(t *testing.T) {
		fake := validGateFake(cfg)
		fake.dataset = &storagepb.Dataset{Status: "active", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"5m"}}
		gate := NewStorageGate(fake, cfg)
		require.Error(t, gate.Validate(ctx))
	})
	t.Run("missing_columns", func(t *testing.T) {
		fake := validGateFake(cfg)
		fake.columns = nil
		gate := NewStorageGate(fake, cfg)
		require.Error(t, gate.Validate(ctx))
	})
	t.Run("data_node_error", func(t *testing.T) {
		fake := validGateFake(cfg)
		fake.nodeErr = errors.New("data node down")
		gate := NewStorageGate(fake, cfg)
		require.Error(t, gate.Validate(ctx))
	})
	t.Run("disabled_data_node", func(t *testing.T) {
		fake := validGateFake(cfg)
		fake.node = &storagepb.DataNode{NodeId: "storage-node-0", Status: "disabled"}
		gate := NewStorageGate(fake, cfg)
		require.Error(t, gate.Validate(ctx))
	})
	t.Run("unlocked_dataset", func(t *testing.T) {
		fake := validGateFake(cfg)
		fake.dataset.BindingLocked = false
		gate := NewStorageGate(fake, cfg)
		require.Error(t, gate.Validate(ctx))
	})
}

func TestCheckRetAndContainsFreq(t *testing.T) {
	assert.Error(t, checkRet(nil))
	assert.Error(t, checkRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "boom"}))
	assert.NoError(t, checkRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}))
	assert.True(t, containsFreq([]string{"1m", "5m"}, "1m"))
	assert.False(t, containsFreq([]string{"5m"}, "1m"))
}

func TestHasHostColumnsIgnoresInactive(t *testing.T) {
	cfg := hostGateCfg()
	cols := resourceColumns(cfg)
	cols = append(cols, &storagepb.DatasetColumn{ColumnName: "logical_cores", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, Status: "disabled"})
	assert.True(t, hasHostColumns(cfg.ResourceDatasetID, cfg, cols))
	assert.False(t, hasHostColumns(cfg.ResourceDatasetID, cfg, cols[:2]))
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

func (f *writerAccessFailFake) UpsertFields(_ context.Context, _ *storagepb.PrimaryUpsertFieldsReq, _ ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.badRet {
		return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: "no"}}, nil
	}
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
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
			for _, field := range row.GetFields() {
				if field.GetFieldId() == "read_bytes_per_second" || field.GetFieldId() == "receive_bytes_per_second" {
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
		readRow(resourceRow(SpaceID, cfg.ResourceDatasetID, "1m", at, &hostmetricpb.HostSnapshot{
			Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 8, UsagePercent: 11, UsageAvailable: true},
			Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 40, AvailableBytes: 60, UsagePercent: 40},
		}, "agent-1", "m1")),
		readRow(diskRow(SpaceID, cfg.DiskDatasetID, "1m", at, &hostmetricpb.DiskMetric{Device: "sda", ReadBytesTotal: 9, RateAvailable: true}, "agent-1", "m1")),
		readRow(networkRow(SpaceID, cfg.NetworkDatasetID, "1m", at, &hostmetricpb.NetworkMetric{Device: "eth0", Operstate: "up", ReceiveBytesTotal: 1}, "agent-1", "m1")),
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
