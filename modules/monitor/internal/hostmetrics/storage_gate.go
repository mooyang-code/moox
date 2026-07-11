package hostmetrics

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
)

type hostStorageMetadata interface {
	GetSpace(context.Context, *storagepb.GetSpaceReq, ...client.Option) (*storagepb.GetSpaceRsp, error)
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	ListDatasetColumns(context.Context, *storagepb.ListDatasetColumnsReq, ...client.Option) (*storagepb.ListDatasetColumnsRsp, error)
	ListPrimaryStoreRoutes(context.Context, *storagepb.ListPrimaryStoreRoutesReq, ...client.Option) (*storagepb.ListPrimaryStoreRoutesRsp, error)
}

type SchemaStatus struct {
	Valid     bool
	CheckedAt time.Time
	Error     string
}

// StorageGate performs a read-only metadata check. It is deliberately kept
// separate from the write path: a temporary metadata outage marks ingestion
// degraded but never causes monitor's SQLite control plane to fail startup.
type StorageGate struct {
	metadata hostStorageMetadata
	cfg      monconfig.HostStorageConfig
	mu       sync.RWMutex
	status   SchemaStatus
}

func NewStorageGate(metadata hostStorageMetadata, cfg monconfig.HostStorageConfig) *StorageGate {
	return &StorageGate{metadata: metadata, cfg: cfg, status: SchemaStatus{Error: "host storage schema has not been checked"}}
}

func (g *StorageGate) Validate(ctx context.Context) error {
	if g == nil || g.metadata == nil {
		return fmt.Errorf("host storage metadata client is not initialized")
	}
	if g.cfg.SpaceID != SpaceID || g.cfg.Frequency != "1m" {
		return g.setStatus(fmt.Errorf("host storage must use space %q and frequency 1m", SpaceID))
	}
	space, err := g.metadata.GetSpace(ctx, &storagepb.GetSpaceReq{SpaceId: g.cfg.SpaceID})
	if err != nil {
		return g.setStatus(fmt.Errorf("get host storage space: %w", err))
	}
	if err := checkRet(space.GetRetInfo()); err != nil || space.GetSpace() == nil || !strings.EqualFold(space.GetSpace().GetStatus(), "active") {
		if err == nil {
			err = fmt.Errorf("space %q is missing or inactive", g.cfg.SpaceID)
		}
		return g.setStatus(err)
	}
	for _, dataset := range []string{g.cfg.ResourceDatasetID, g.cfg.FilesystemDatasetID, g.cfg.DiskDatasetID, g.cfg.NetworkDatasetID} {
		item, callErr := g.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: g.cfg.SpaceID, DatasetId: dataset})
		if callErr != nil {
			return g.setStatus(fmt.Errorf("get host dataset %q: %w", dataset, callErr))
		}
		if err := checkRet(item.GetRetInfo()); err != nil || item.GetDataset() == nil || !strings.EqualFold(item.GetDataset().GetStatus(), "active") {
			if err == nil {
				err = fmt.Errorf("dataset %q is missing or inactive", dataset)
			}
			return g.setStatus(err)
		}
		if item.GetDataset().GetDataKind() != storagepb.DataKind_DATA_KIND_TIME_SERIES || !containsFreq(item.GetDataset().GetFreqs(), g.cfg.Frequency) {
			return g.setStatus(fmt.Errorf("dataset %q does not support time-series frequency %q", dataset, g.cfg.Frequency))
		}
		columns, callErr := g.metadata.ListDatasetColumns(ctx, &storagepb.ListDatasetColumnsReq{SpaceId: g.cfg.SpaceID, DatasetId: dataset, Page: &commonpb.Page{Page: 1, Size: 100}})
		if callErr != nil {
			return g.setStatus(fmt.Errorf("list host columns %q: %w", dataset, callErr))
		}
		if err := checkRet(columns.GetRetInfo()); err != nil || !hasHostColumns(dataset, columns.GetColumns()) {
			if err == nil {
				err = fmt.Errorf("dataset %q is missing writer columns", dataset)
			}
			return g.setStatus(err)
		}
		routes, callErr := g.metadata.ListPrimaryStoreRoutes(ctx, &storagepb.ListPrimaryStoreRoutesReq{SpaceId: g.cfg.SpaceID, DatasetId: dataset, Page: &commonpb.Page{Page: 1, Size: 100}})
		if callErr != nil {
			return g.setStatus(fmt.Errorf("list host routes %q: %w", dataset, callErr))
		}
		if err := checkRet(routes.GetRetInfo()); err != nil {
			return g.setStatus(err)
		}
		found := false
		for _, route := range routes.GetPrimaryStoreRoutes() {
			if route.GetDatasetId() == dataset && route.GetSubjectPattern() == "*" && strings.EqualFold(route.GetStatus(), "active") {
				found = true
				break
			}
		}
		if !found {
			return g.setStatus(fmt.Errorf("dataset %q has no active wildcard route", dataset))
		}
	}
	return g.setStatus(nil)
}

func hasHostColumns(dataset string, columns []*storagepb.DatasetColumn) bool {
	want := map[string]struct{}{}
	switch dataset {
	case "host_resource_v1":
		for _, name := range []string{"agent_id", "logical_cores", "cpu_usage_available", "memory_total_bytes", "memory_used_bytes", "memory_available_bytes", "memory_usage_percent"} {
			want[name] = struct{}{}
		}
	case "host_fs_v1":
		for _, name := range []string{"device", "mountpoint", "fs_type", "total_bytes", "used_bytes", "available_bytes", "usage_percent", "read_only"} {
			want[name] = struct{}{}
		}
	case "host_disk_v1":
		for _, name := range []string{"device", "read_bytes_total", "write_bytes_total", "read_ops_total", "write_ops_total", "io_time_ms_total", "rate_available"} {
			want[name] = struct{}{}
		}
	case "host_net_v1":
		for _, name := range []string{"device", "operstate", "receive_bytes_total", "transmit_bytes_total", "receive_errors_total", "transmit_errors_total", "receive_dropped_total", "transmit_dropped_total", "rate_available"} {
			want[name] = struct{}{}
		}
	}
	for _, column := range columns {
		delete(want, column.GetColumnName())
	}
	return len(want) == 0
}

func (g *StorageGate) Status() SchemaStatus {
	if g == nil {
		return SchemaStatus{Error: "host storage gate is nil"}
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.status
}

func (g *StorageGate) Ready() bool { return g != nil && g.Status().Valid }

func (g *StorageGate) setStatus(err error) error {
	g.mu.Lock()
	g.status = SchemaStatus{Valid: err == nil, CheckedAt: time.Now().UTC()}
	if err != nil {
		g.status.Error = err.Error()
	}
	g.mu.Unlock()
	return err
}

func checkRet(ret *commonpb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("empty ret_info")
	}
	if ret.GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("storage returned %s", ret.GetMsg())
	}
	return nil
}

func containsFreq(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
