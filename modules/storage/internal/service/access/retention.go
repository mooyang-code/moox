package access

import (
	"context"
	"fmt"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// PruneHostDatasets deletes one bounded page per configured host dataset. It
// is called by the Storage maintenance loop, not by the Monitor process.
func (s *Service) PruneHostDatasets(ctx context.Context, spaceID string, datasets []string, retention time.Duration, now time.Time) (uint32, error) {
	if retention <= 0 {
		return 0, fmt.Errorf("retention must be positive")
	}
	cutoff := now.UTC().Add(-retention).Add(-time.Nanosecond).Format(time.RFC3339Nano)
	var deleted uint32
	for _, dataset := range datasets {
		if dataset == "" {
			continue
		}
		rsp, err := s.DeleteTimeSeriesRows(ctx, &pb.DeleteTimeSeriesRowsReq{SpaceId: spaceID, DatasetId: dataset, TimeRange: &pb.TimeRange{EndTime: cutoff}, Page: &pb.Page{Page: 1, Size: 1000}})
		if err != nil {
			return deleted, err
		}
		if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return deleted, fmt.Errorf("prune dataset %q failed", dataset)
		}
		deleted += rsp.GetDeleted()
	}
	return deleted, nil
}
