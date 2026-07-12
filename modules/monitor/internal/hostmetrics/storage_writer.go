package hostmetrics

import (
	"context"
	"fmt"
	"sort"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"trpc.group/trpc-go/trpc-go/client"
)

type hostStorageAccess interface {
	WriteTimeSeriesRows(context.Context, *storagepb.WriteTimeSeriesRowsReq, ...client.Option) (*storagepb.WriteTimeSeriesRowsRsp, error)
}

// StorageWriter converts one validated host snapshot into the four Host
// datasets. Storage keys are minute-granular, so retries are idempotent.
type StorageWriter struct {
	access hostStorageAccess
	cfg    monconfig.HostStorageConfig
}

func NewStorageWriter(access hostStorageAccess, cfg monconfig.HostStorageConfig) *StorageWriter {
	return &StorageWriter{access: access, cfg: cfg}
}

func (w *StorageWriter) WriteSnapshot(ctx context.Context, snapshot *hostmetricpb.HostSnapshot, agentID string, observedAt time.Time, messageID string) error {
	if w == nil || w.access == nil {
		return fmt.Errorf("host storage access client is nil")
	}
	if snapshot == nil || agentID == "" {
		return fmt.Errorf("host snapshot and agent id are required")
	}
	bucket := observedAt.UTC().Truncate(time.Minute).Format(time.RFC3339Nano)
	rows := []struct {
		dataset string
		row     *storagepb.TimeSeriesRow
	}{
		{dataset: w.cfg.ResourceDatasetID, row: resourceRow(w.cfg.SpaceID, w.cfg.ResourceDatasetID, w.cfg.Frequency, bucket, snapshot, agentID, messageID)},
	}
	for _, fs := range sortedFilesystems(snapshot.GetFilesystems()) {
		rows = append(rows, struct {
			dataset string
			row     *storagepb.TimeSeriesRow
		}{w.cfg.FilesystemDatasetID, filesystemRow(w.cfg.SpaceID, w.cfg.FilesystemDatasetID, w.cfg.Frequency, bucket, fs, agentID, messageID)})
	}
	for _, disk := range sortedDisks(snapshot.GetDisks()) {
		rows = append(rows, struct {
			dataset string
			row     *storagepb.TimeSeriesRow
		}{w.cfg.DiskDatasetID, diskRow(w.cfg.SpaceID, w.cfg.DiskDatasetID, w.cfg.Frequency, bucket, disk, agentID, messageID)})
	}
	for _, network := range sortedNetworks(snapshot.GetNetworks()) {
		rows = append(rows, struct {
			dataset string
			row     *storagepb.TimeSeriesRow
		}{w.cfg.NetworkDatasetID, networkRow(w.cfg.SpaceID, w.cfg.NetworkDatasetID, w.cfg.Frequency, bucket, network, agentID, messageID)})
	}
	for _, group := range groupRows(rows) {
		writeCtx := ctx
		cancel := func() {}
		if w.cfg.WriteTimeout > 0 {
			writeCtx, cancel = context.WithTimeout(ctx, w.cfg.WriteTimeout)
		}
		rsp, err := w.access.WriteTimeSeriesRows(writeCtx, &storagepb.WriteTimeSeriesRowsReq{Rows: group})
		cancel()
		if err != nil {
			return fmt.Errorf("write host dataset %q: %w", group[0].GetKey().GetDatasetId(), err)
		}
		if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			if rsp == nil || rsp.GetRetInfo() == nil {
				return fmt.Errorf("write host dataset %q returned empty response", group[0].GetKey().GetDatasetId())
			}
			return fmt.Errorf("write host dataset %q: %s", group[0].GetKey().GetDatasetId(), rsp.GetRetInfo().GetMsg())
		}
	}
	return nil
}

func groupRows(rows []struct {
	dataset string
	row     *storagepb.TimeSeriesRow
}) [][]*storagepb.TimeSeriesRow {
	groups := make(map[string][]*storagepb.TimeSeriesRow)
	order := make([]string, 0, 4)
	for _, item := range rows {
		if _, ok := groups[item.dataset]; !ok {
			order = append(order, item.dataset)
		}
		groups[item.dataset] = append(groups[item.dataset], item.row)
	}
	sort.Strings(order)
	out := make([][]*storagepb.TimeSeriesRow, 0, len(order))
	for _, dataset := range order {
		out = append(out, groups[dataset])
	}
	return out
}

func baseKey(space, dataset, subject, freq, dataTime string, dimensions map[string]string) *storagepb.TimeSeriesKey {
	return &storagepb.TimeSeriesKey{SpaceId: space, DatasetId: dataset, SubjectId: subject, Freq: freq, DataTime: dataTime, Dimensions: dimensions}
}
func attrs(agentID, messageID string) map[string]string {
	return map[string]string{"agent_id": agentID, "message_id": messageID}
}
func intColumn(name string, value uint64) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{ColumnName: name, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: int64(value)}}}
}
func doubleColumn(name string, value float64) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{ColumnName: name, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}}}
}
func stringColumn(name, value string) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{ColumnName: name, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: value}}}
}
func boolColumn(name string, value bool) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{ColumnName: name, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_BoolValue{BoolValue: value}}}
}

func resourceRow(space, dataset, freq, at string, snapshot *hostmetricpb.HostSnapshot, agentID, messageID string) *storagepb.TimeSeriesRow {
	cpu, memory := snapshot.GetCpu(), snapshot.GetMemory()
	return &storagepb.TimeSeriesRow{Key: baseKey(space, dataset, agentID, freq, at, nil), Attributes: attrs(agentID, messageID), Columns: []*storagepb.ColumnValue{
		stringColumn("agent_id", agentID), intColumn("logical_cores", uint64(cpu.GetLogicalCores())), doubleColumn("cpu_usage_percent", cpu.GetUsagePercent()), boolColumn("cpu_usage_available", cpu.GetUsageAvailable()), intColumn("memory_total_bytes", memory.GetTotalBytes()), intColumn("memory_used_bytes", memory.GetUsedBytes()), intColumn("memory_available_bytes", memory.GetAvailableBytes()), doubleColumn("memory_usage_percent", memory.GetUsagePercent()),
	}}
}

func filesystemRow(space, dataset, freq, at string, fs *hostmetricpb.FilesystemMetric, agentID, messageID string) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{Key: baseKey(space, dataset, agentID, freq, at, map[string]string{"device": fs.GetDevice(), "mountpoint": fs.GetMountpoint()}), Attributes: attrs(agentID, messageID), Columns: []*storagepb.ColumnValue{stringColumn("device", fs.GetDevice()), stringColumn("mountpoint", fs.GetMountpoint()), stringColumn("fs_type", fs.GetFsType()), intColumn("total_bytes", fs.GetTotalBytes()), intColumn("used_bytes", fs.GetUsedBytes()), intColumn("available_bytes", fs.GetAvailableBytes()), doubleColumn("usage_percent", fs.GetUsagePercent()), boolColumn("read_only", fs.GetReadOnly())}}
}

func diskRow(space, dataset, freq, at string, disk *hostmetricpb.DiskMetric, agentID, messageID string) *storagepb.TimeSeriesRow {
	columns := []*storagepb.ColumnValue{stringColumn("device", disk.GetDevice()), intColumn("read_bytes_total", disk.GetReadBytesTotal()), intColumn("write_bytes_total", disk.GetWriteBytesTotal()), intColumn("read_ops_total", disk.GetReadOpsTotal()), intColumn("write_ops_total", disk.GetWriteOpsTotal()), intColumn("io_time_ms_total", disk.GetIoTimeMsTotal()), boolColumn("rate_available", disk.GetRateAvailable())}
	if disk.GetRateAvailable() {
		columns = append(columns, doubleColumn("read_bytes_per_second", disk.GetReadBytesPerSecond()), doubleColumn("write_bytes_per_second", disk.GetWriteBytesPerSecond()), doubleColumn("read_iops", disk.GetReadIops()), doubleColumn("write_iops", disk.GetWriteIops()), doubleColumn("utilization_percent", disk.GetUtilizationPercent()))
	}
	return &storagepb.TimeSeriesRow{Key: baseKey(space, dataset, agentID, freq, at, map[string]string{"device": disk.GetDevice()}), Attributes: attrs(agentID, messageID), Columns: columns}
}

func networkRow(space, dataset, freq, at string, network *hostmetricpb.NetworkMetric, agentID, messageID string) *storagepb.TimeSeriesRow {
	columns := []*storagepb.ColumnValue{stringColumn("device", network.GetDevice()), stringColumn("operstate", network.GetOperstate()), intColumn("receive_bytes_total", network.GetReceiveBytesTotal()), intColumn("transmit_bytes_total", network.GetTransmitBytesTotal()), intColumn("receive_errors_total", network.GetReceiveErrorsTotal()), intColumn("transmit_errors_total", network.GetTransmitErrorsTotal()), intColumn("receive_dropped_total", network.GetReceiveDroppedTotal()), intColumn("transmit_dropped_total", network.GetTransmitDroppedTotal()), boolColumn("rate_available", network.GetRateAvailable())}
	if network.GetRateAvailable() {
		columns = append(columns, doubleColumn("receive_bytes_per_second", network.GetReceiveBytesPerSecond()), doubleColumn("transmit_bytes_per_second", network.GetTransmitBytesPerSecond()))
	}
	return &storagepb.TimeSeriesRow{Key: baseKey(space, dataset, agentID, freq, at, map[string]string{"device": network.GetDevice()}), Attributes: attrs(agentID, messageID), Columns: columns}
}

func sortedFilesystems(items []*hostmetricpb.FilesystemMetric) []*hostmetricpb.FilesystemMetric {
	out := append([]*hostmetricpb.FilesystemMetric(nil), items...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].GetDevice()+"\x00"+out[i].GetMountpoint() < out[j].GetDevice()+"\x00"+out[j].GetMountpoint()
	})
	return out
}
func sortedDisks(items []*hostmetricpb.DiskMetric) []*hostmetricpb.DiskMetric {
	out := append([]*hostmetricpb.DiskMetric(nil), items...)
	sort.Slice(out, func(i, j int) bool { return out[i].GetDevice() < out[j].GetDevice() })
	return out
}
func sortedNetworks(items []*hostmetricpb.NetworkMetric) []*hostmetricpb.NetworkMetric {
	out := append([]*hostmetricpb.NetworkMetric(nil), items...)
	sort.Slice(out, func(i, j int) bool { return out[i].GetDevice() < out[j].GetDevice() })
	return out
}
