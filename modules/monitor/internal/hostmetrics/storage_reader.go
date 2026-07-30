package hostmetrics

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/storageauth"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/trpcretry"
	"trpc.group/trpc-go/trpc-go/client"
)

type hostStorageRead interface {
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
}

// StorageReader reconstructs host history from Storage's four datasets.
type StorageReader struct {
	access hostStorageRead
	auth   *commonpb.AuthInfo
	cfg    monconfig.HostStorageConfig
}

// ForecastHistoryLimit covers seven days of one-minute samples plus a small
// boundary margin. It is deliberately independent from the RPC default page
// limit so Doctor can make a daily forecast without silently seeing only the
// last few hours.
const ForecastHistoryLimit = 7*24*60 + 8

const maxHistoryPageSize = 500

func NewStorageReader(access hostStorageRead, cfg monconfig.HostStorageConfig) *StorageReader {
	return &StorageReader{access: access, auth: storageauth.Primary(cfg.KeyID), cfg: cfg}
}

func (r *StorageReader) History(ctx context.Context, agentID string, start, end time.Time, limit int) ([]HistoryPoint, error) {
	return r.HistoryAt(ctx, agentID, start, end, time.Now().UTC(), limit)
}

func (r *StorageReader) HistoryAt(ctx context.Context, agentID string, start, end, now time.Time, limit int) ([]HistoryPoint, error) {
	if r == nil || r.access == nil {
		return nil, fmt.Errorf("host storage reader is not initialized")
	}
	if agentID == "" {
		return nil, fmt.Errorf("agent id is required")
	}
	if limit <= 0 {
		limit = r.cfg.ReadLimit
	}
	if limit <= 0 {
		limit = maxHistoryPageSize
	}
	if limit > ForecastHistoryLimit {
		limit = ForecastHistoryLimit
	}
	if end.Before(start) {
		return nil, fmt.Errorf("history end precedes start")
	}
	requestedStart, requestedEnd := start, end
	now = now.UTC()
	windowStart := now.Add(-7 * 24 * time.Hour)
	if end.After(now) {
		end = now
	}
	if start.Before(windowStart) {
		start = windowStart
	}
	if end.Before(start) {
		return []HistoryPoint{}, nil
	}
	if requestedEnd.Sub(requestedStart) > 7*24*time.Hour {
		start = end.Add(-7 * 24 * time.Hour)
		if start.Before(windowStart) {
			start = windowStart
		}
	}
	points := make(map[string]*HistoryPoint)
	for _, dataset := range []string{r.cfg.ResourceDatasetID, r.cfg.FilesystemDatasetID, r.cfg.DiskDatasetID, r.cfg.NetworkDatasetID} {
		rows, err := r.scan(ctx, dataset, agentID, start, end, limit)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row == nil || row.GetKey() == nil || row.GetKey().GetSubjectId() != agentID {
				continue
			}
			at := row.GetKey().GetDataTime()
			point := points[at]
			if point == nil {
				point = &HistoryPoint{AgentID: agentID, ObservedAt: at, Snapshot: &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{}, Memory: &hostmetricpb.MemoryMetric{}}}
				points[at] = point
			}
			if err := mergeRow(point.Snapshot, dataset, r.cfg, row); err != nil {
				return nil, err
			}
		}
	}
	keys := make([]string, 0, len(points))
	for key := range points {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[len(keys)-limit:]
	}
	out := make([]HistoryPoint, 0, len(keys))
	for _, key := range keys {
		out = append(out, *points[key])
	}
	return out, nil
}

func (r *StorageReader) scan(ctx context.Context, dataset, agentID string, start, end time.Time, limit int) ([]*storagepb.TimeSeriesRow, error) {
	if dataset == "" {
		return nil, nil
	}
	rows := make([]*storagepb.TimeSeriesRow, 0)
	pageSize := limit
	if pageSize > maxHistoryPageSize {
		pageSize = maxHistoryPageSize
	}
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := r.access.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
			AuthInfo:  r.auth,
			SpaceId:   r.cfg.SpaceID,
			DatasetId: dataset,
			Selectors: []*storagepb.TimeSeriesSelector{{SpaceId: r.cfg.SpaceID, DatasetId: dataset, SubjectId: agentID, Freq: r.cfg.Frequency}},
			TimeRange: &storagepb.TimeRange{StartTime: start.UTC().Format(time.RFC3339Nano), EndTime: end.UTC().Format(time.RFC3339Nano)},
			Order:     storagepb.SortOrder_SORT_ORDER_DESC,
			Page:      &commonpb.Page{Page: pageNo, Size: uint32(pageSize)},
		}, client.WithFilter(trpcretry.ReadOnly()))
		if err != nil {
			return nil, fmt.Errorf("read host dataset %q: %w", dataset, err)
		}
		if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			if rsp == nil || rsp.GetRetInfo() == nil {
				return nil, fmt.Errorf("read host dataset %q returned empty response", dataset)
			}
			return nil, fmt.Errorf("read host dataset %q: %s", dataset, rsp.GetRetInfo().GetMsg())
		}
		rows = append(rows, rsp.GetRows()...)
		page := rsp.GetPageResult()
		if page == nil || !page.GetHasMore() {
			break
		}
		if pageNo == ^uint32(0) {
			return nil, fmt.Errorf("host dataset %q pagination overflow", dataset)
		}
	}
	return rows, nil
}

func mergeRow(snapshot *hostmetricpb.HostSnapshot, dataset string, cfg monconfig.HostStorageConfig, row *storagepb.TimeSeriesRow) error {
	values := make(map[string]*storagepb.TypedValue, len(row.GetFields()))
	for _, field := range row.GetFields() {
		if field != nil {
			name := field.GetFieldId()
			values[name] = field.GetValue()
			if prefix := dataset + "."; strings.HasPrefix(name, prefix) {
				values[strings.TrimPrefix(name, prefix)] = field.GetValue()
			}
		}
	}
	getInt := func(name string) uint64 { return uint64(values[name].GetIntValue()) }
	getFloat := func(name string) float64 { return values[name].GetDoubleValue() }
	getBool := func(name string) bool { return values[name].GetBoolValue() }
	getString := func(name string) string { return values[name].GetStringValue() }
	switch dataset {
	case cfg.ResourceDatasetID:
		snapshot.Cpu.LogicalCores = uint32(getInt("logical_cores"))
		snapshot.Cpu.UsagePercent, snapshot.Cpu.UsageAvailable = getFloat("cpu_usage_percent"), getBool("cpu_usage_available")
		snapshot.Memory.TotalBytes, snapshot.Memory.UsedBytes, snapshot.Memory.AvailableBytes = getInt("memory_total_bytes"), getInt("memory_used_bytes"), getInt("memory_available_bytes")
		snapshot.Memory.UsagePercent = getFloat("memory_usage_percent")
	case cfg.FilesystemDatasetID:
		fs := &hostmetricpb.FilesystemMetric{Device: getString("device"), Mountpoint: getString("mountpoint"), FsType: getString("fs_type"), TotalBytes: getInt("total_bytes"), UsedBytes: getInt("used_bytes"), AvailableBytes: getInt("available_bytes"), UsagePercent: getFloat("usage_percent"), ReadOnly: getBool("read_only")}
		snapshot.Filesystems = append(snapshot.Filesystems, fs)
	case cfg.DiskDatasetID:
		d := &hostmetricpb.DiskMetric{Device: getString("device"), ReadBytesTotal: getInt("read_bytes_total"), WriteBytesTotal: getInt("write_bytes_total"), ReadOpsTotal: getInt("read_ops_total"), WriteOpsTotal: getInt("write_ops_total"), IoTimeMsTotal: getInt("io_time_ms_total"), RateAvailable: getBool("rate_available"), ReadBytesPerSecond: getFloat("read_bytes_per_second"), WriteBytesPerSecond: getFloat("write_bytes_per_second"), ReadIops: getFloat("read_iops"), WriteIops: getFloat("write_iops"), UtilizationPercent: getFloat("utilization_percent")}
		snapshot.Disks = append(snapshot.Disks, d)
	case cfg.NetworkDatasetID:
		n := &hostmetricpb.NetworkMetric{Device: getString("device"), Operstate: getString("operstate"), ReceiveBytesTotal: getInt("receive_bytes_total"), TransmitBytesTotal: getInt("transmit_bytes_total"), ReceiveErrorsTotal: getInt("receive_errors_total"), TransmitErrorsTotal: getInt("transmit_errors_total"), ReceiveDroppedTotal: getInt("receive_dropped_total"), TransmitDroppedTotal: getInt("transmit_dropped_total"), RateAvailable: getBool("rate_available"), ReceiveBytesPerSecond: getFloat("receive_bytes_per_second"), TransmitBytesPerSecond: getFloat("transmit_bytes_per_second"), ReceiveErrorsPerSecond: getFloat("receive_errors_per_second"), TransmitErrorsPerSecond: getFloat("transmit_errors_per_second"), ErrorRateAvailable: getBool("error_rate_available")}
		snapshot.Networks = append(snapshot.Networks, n)
	default:
		return fmt.Errorf("unknown host dataset %q", dataset)
	}
	return nil
}
