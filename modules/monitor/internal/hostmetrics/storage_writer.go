package hostmetrics

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/url"
	"sort"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/storageauth"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"trpc.group/trpc-go/trpc-go/client"
)

const maxSeriesTagBytes = 128

type hostStorageAccess interface {
	UpsertFields(context.Context, *storagepb.PrimaryUpsertFieldsReq, ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error)
}

// StorageWriter converts one validated host snapshot into the four Host
// datasets. Storage keys are minute-granular, so retries are idempotent.
type StorageWriter struct {
	access hostStorageAccess
	auth   *commonpb.AuthInfo
	cfg    monconfig.HostStorageConfig
}

func NewStorageWriter(access hostStorageAccess, cfg monconfig.HostStorageConfig) *StorageWriter {
	return &StorageWriter{access: access, auth: storageauth.Primary(cfg.KeyID), cfg: cfg}
}

func (w *StorageWriter) WriteSnapshot(ctx context.Context, snapshot *hostmetricpb.HostSnapshot, agentID string, observedAt time.Time, messageID string) error {
	if w == nil || w.access == nil {
		return fmt.Errorf("host storage-primary client is nil")
	}
	if snapshot == nil || agentID == "" || messageID == "" {
		return fmt.Errorf("host snapshot, agent id, and message id are required")
	}
	bucket := observedAt.UTC().Truncate(time.Minute).Format(time.RFC3339Nano)
	rows := []struct {
		dataset string
		row     *storagepb.RowFieldUpsert
	}{
		{dataset: w.cfg.ResourceDatasetID, row: resourceRow(w.cfg.SpaceID, w.cfg.ResourceDatasetID, w.cfg.Frequency, bucket, snapshot, agentID)},
	}
	for _, fs := range sortedFilesystems(snapshot.GetFilesystems()) {
		rows = append(rows, struct {
			dataset string
			row     *storagepb.RowFieldUpsert
		}{w.cfg.FilesystemDatasetID, filesystemRow(w.cfg.SpaceID, w.cfg.FilesystemDatasetID, w.cfg.Frequency, bucket, fs, agentID)})
	}
	for _, disk := range sortedDisks(snapshot.GetDisks()) {
		rows = append(rows, struct {
			dataset string
			row     *storagepb.RowFieldUpsert
		}{w.cfg.DiskDatasetID, diskRow(w.cfg.SpaceID, w.cfg.DiskDatasetID, w.cfg.Frequency, bucket, disk, agentID)})
	}
	for _, network := range sortedNetworks(snapshot.GetNetworks()) {
		rows = append(rows, struct {
			dataset string
			row     *storagepb.RowFieldUpsert
		}{w.cfg.NetworkDatasetID, networkRow(w.cfg.SpaceID, w.cfg.NetworkDatasetID, w.cfg.Frequency, bucket, network, agentID)})
	}
	for _, group := range groupRows(rows) {
		writeCtx := ctx
		cancel := func() {}
		if w.cfg.WriteTimeout > 0 {
			writeCtx, cancel = context.WithTimeout(ctx, w.cfg.WriteTimeout)
		}
		datasetID := group[0].GetKey().GetDatasetId()
		rsp, err := w.access.UpsertFields(writeCtx, &storagepb.PrimaryUpsertFieldsReq{
			AuthInfo: w.auth, Rows: group,
			SourceEventId: messageID + ":" + datasetID,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("write host dataset %q: %w", datasetID, err)
		}
		if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			if rsp == nil || rsp.GetRetInfo() == nil {
				return fmt.Errorf("write host dataset %q returned empty response", datasetID)
			}
			return fmt.Errorf("write host dataset %q: %s", datasetID, rsp.GetRetInfo().GetMsg())
		}
	}
	return nil
}

func groupRows(rows []struct {
	dataset string
	row     *storagepb.RowFieldUpsert
}) [][]*storagepb.RowFieldUpsert {
	groups := make(map[string][]*storagepb.RowFieldUpsert)
	order := make([]string, 0, 4)
	for _, item := range rows {
		if _, ok := groups[item.dataset]; !ok {
			order = append(order, item.dataset)
		}
		groups[item.dataset] = append(groups[item.dataset], item.row)
	}
	sort.Strings(order)
	out := make([][]*storagepb.RowFieldUpsert, 0, len(order))
	for _, dataset := range order {
		out = append(out, groups[dataset])
	}
	return out
}

func baseKey(space, dataset, subject, freq, dataTime, seriesTag string) *storagepb.RowKey {
	return &storagepb.RowKey{SpaceId: space, DatasetId: dataset, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: subject, Freq: freq, DataTime: dataTime, SeriesTag: seriesTag}}}
}

func deviceSeriesTag(device string) string {
	tag := "device:" + url.QueryEscape(device)
	if len(tag) <= maxSeriesTagBytes {
		return tag
	}
	return hashedSeriesTag("device", device)
}

func filesystemSeriesTag(device, mountpoint string) string {
	tag := "filesystem:" + url.QueryEscape(device) + "|" + url.QueryEscape(mountpoint)
	if len(tag) <= maxSeriesTagBytes {
		return tag
	}
	return hashedSeriesTag("filesystem", device, mountpoint)
}

func hashedSeriesTag(kind string, identityParts ...string) string {
	hash := sha256.New()
	var size [8]byte
	for _, part := range identityParts {
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(part))
	}
	return fmt.Sprintf("%s-sha256:%x", kind, hash.Sum(nil))
}

func intColumn(name string, value uint64) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: int64(value)}}}
}
func doubleColumn(name string, value float64) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}}}
}
func stringColumn(name, value string) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: name, Value: stringValue(value)}
}
func boolColumn(name string, value bool) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_BoolValue{BoolValue: value}}}
}

func resourceRow(space, dataset, freq, at string, snapshot *hostmetricpb.HostSnapshot, agentID string) *storagepb.RowFieldUpsert {
	cpu, memory := snapshot.GetCpu(), snapshot.GetMemory()
	return &storagepb.RowFieldUpsert{Key: baseKey(space, dataset, agentID, freq, at, ""), Fields: []*storagepb.FieldValue{
		stringColumn("agent_id", agentID), intColumn("logical_cores", uint64(cpu.GetLogicalCores())), doubleColumn("cpu_usage_percent", cpu.GetUsagePercent()), boolColumn("cpu_usage_available", cpu.GetUsageAvailable()), intColumn("memory_total_bytes", memory.GetTotalBytes()), intColumn("memory_used_bytes", memory.GetUsedBytes()), intColumn("memory_available_bytes", memory.GetAvailableBytes()), doubleColumn("memory_usage_percent", memory.GetUsagePercent()),
	}}
}

func filesystemRow(space, dataset, freq, at string, fs *hostmetricpb.FilesystemMetric, agentID string) *storagepb.RowFieldUpsert {
	return &storagepb.RowFieldUpsert{Key: baseKey(space, dataset, agentID, freq, at, filesystemSeriesTag(fs.GetDevice(), fs.GetMountpoint())), Fields: []*storagepb.FieldValue{stringColumn("device", fs.GetDevice()), stringColumn("mountpoint", fs.GetMountpoint()), stringColumn("fs_type", fs.GetFsType()), intColumn("total_bytes", fs.GetTotalBytes()), intColumn("used_bytes", fs.GetUsedBytes()), intColumn("available_bytes", fs.GetAvailableBytes()), doubleColumn("usage_percent", fs.GetUsagePercent()), boolColumn("read_only", fs.GetReadOnly())}}
}

func diskRow(space, dataset, freq, at string, disk *hostmetricpb.DiskMetric, agentID string) *storagepb.RowFieldUpsert {
	fields := []*storagepb.FieldValue{stringColumn("device", disk.GetDevice()), intColumn("read_bytes_total", disk.GetReadBytesTotal()), intColumn("write_bytes_total", disk.GetWriteBytesTotal()), intColumn("read_ops_total", disk.GetReadOpsTotal()), intColumn("write_ops_total", disk.GetWriteOpsTotal()), intColumn("io_time_ms_total", disk.GetIoTimeMsTotal()), boolColumn("rate_available", disk.GetRateAvailable())}
	if disk.GetRateAvailable() {
		fields = append(fields, doubleColumn("read_bytes_per_second", disk.GetReadBytesPerSecond()), doubleColumn("write_bytes_per_second", disk.GetWriteBytesPerSecond()), doubleColumn("read_iops", disk.GetReadIops()), doubleColumn("write_iops", disk.GetWriteIops()), doubleColumn("utilization_percent", disk.GetUtilizationPercent()))
	}
	return &storagepb.RowFieldUpsert{Key: baseKey(space, dataset, agentID, freq, at, deviceSeriesTag(disk.GetDevice())), Fields: fields}
}

func networkRow(space, dataset, freq, at string, network *hostmetricpb.NetworkMetric, agentID string) *storagepb.RowFieldUpsert {
	fields := []*storagepb.FieldValue{stringColumn("device", network.GetDevice()), stringColumn("operstate", network.GetOperstate()), intColumn("receive_bytes_total", network.GetReceiveBytesTotal()), intColumn("transmit_bytes_total", network.GetTransmitBytesTotal()), intColumn("receive_errors_total", network.GetReceiveErrorsTotal()), intColumn("transmit_errors_total", network.GetTransmitErrorsTotal()), intColumn("receive_dropped_total", network.GetReceiveDroppedTotal()), intColumn("transmit_dropped_total", network.GetTransmitDroppedTotal()), boolColumn("rate_available", network.GetRateAvailable()), boolColumn("error_rate_available", network.GetErrorRateAvailable())}
	if network.GetRateAvailable() {
		fields = append(fields, doubleColumn("receive_bytes_per_second", network.GetReceiveBytesPerSecond()), doubleColumn("transmit_bytes_per_second", network.GetTransmitBytesPerSecond()))
	}
	if network.GetErrorRateAvailable() {
		fields = append(fields, doubleColumn("receive_errors_per_second", network.GetReceiveErrorsPerSecond()), doubleColumn("transmit_errors_per_second", network.GetTransmitErrorsPerSecond()))
	}
	return &storagepb.RowFieldUpsert{Key: baseKey(space, dataset, agentID, freq, at, deviceSeriesTag(network.GetDevice())), Fields: fields}
}

func stringValue(value string) *storagepb.TypedValue {
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: value}}
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
