package hostmetrics

import (
	"context"
	"fmt"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
)

type hostStorageDelete interface {
	DeleteTimeSeriesRows(context.Context, *storagepb.DeleteTimeSeriesRowsReq, ...client.Option) (*storagepb.DeleteTimeSeriesRowsRsp, error)
}

// Prune deletes one bounded page per dataset. A later tick retries while
// HasMore data remains, and no rows-updated event is emitted by Storage.
func Prune(ctx context.Context, rawAccess any, cfg monconfig.HostStorageConfig, now time.Time) (uint32, error) {
	access, ok := rawAccess.(hostStorageDelete)
	if !ok || access == nil {
		return 0, fmt.Errorf("host retention access client is nil")
	}
	cutoff := now.UTC().Add(-cfg.Retention).Add(-time.Nanosecond).Format(time.RFC3339Nano)
	var total uint32
	for _, dataset := range []string{cfg.ResourceDatasetID, cfg.FilesystemDatasetID, cfg.DiskDatasetID, cfg.NetworkDatasetID} {
		rsp, err := access.DeleteTimeSeriesRows(ctx, &storagepb.DeleteTimeSeriesRowsReq{SpaceId: cfg.SpaceID, DatasetId: dataset, TimeRange: &storagepb.TimeRange{EndTime: cutoff}, Page: &commonpb.Page{Page: 1, Size: 1000}})
		if err != nil {
			return total, fmt.Errorf("prune host dataset %q: %w", dataset, err)
		}
		if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return total, fmt.Errorf("prune host dataset %q failed", dataset)
		}
		total += rsp.GetDeleted()
	}
	return total, nil
}
