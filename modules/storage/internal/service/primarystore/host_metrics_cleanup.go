package primarystore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// HostMetricsCleanupOptions bounds one cleanup run.
type HostMetricsCleanupOptions struct {
	SpaceID          string
	DatasetIDs       []string
	MaxAge           time.Duration
	BatchSize        uint32
	MaxBatchesPerRun int
	Now              time.Time
}

// HostMetricsCleanupResult reports successful work, including partial progress.
type HostMetricsCleanupResult struct {
	Deleted uint32
	Batches int
}

// CleanupExpiredHostMetrics deletes bounded pages of expired host metric facts.
func (s *Service) CleanupExpiredHostMetrics(ctx context.Context, opts HostMetricsCleanupOptions) (HostMetricsCleanupResult, error) {
	if err := validateHostMetricsCleanupOptions(opts); err != nil {
		return HostMetricsCleanupResult{}, err
	}
	deleteRows := s.DeleteTimeSeriesRows
	if s.cleanupDeleteRows != nil {
		deleteRows = s.cleanupDeleteRows
	}
	cutoff := opts.Now.UTC().Add(-opts.MaxAge).Add(-time.Nanosecond).Format(time.RFC3339Nano)
	var result HostMetricsCleanupResult
	var cleanupErrors []error
	for _, datasetID := range opts.DatasetIDs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		for batch := 0; batch < opts.MaxBatchesPerRun; batch++ {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			rsp, err := deleteRows(ctx, &pb.DeleteTimeSeriesRowsReq{
				SpaceId: opts.SpaceID, DatasetId: datasetID,
				TimeRange: &pb.TimeRange{EndTime: cutoff},
				Page:      &pb.Page{Page: 1, Size: opts.BatchSize},
			})
			if err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("cleanup host metrics dataset %q: %w", datasetID, err))
				break
			}
			if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
				message := "missing response"
				if rsp != nil && rsp.GetRetInfo() != nil && strings.TrimSpace(rsp.GetRetInfo().GetMsg()) != "" {
					message = rsp.GetRetInfo().GetMsg()
				}
				cleanupErrors = append(cleanupErrors, fmt.Errorf("cleanup host metrics dataset %q: %s", datasetID, message))
				break
			}
			result.Batches++
			result.Deleted += rsp.GetDeleted()
			if rsp.GetDeleted() == 0 {
				break
			}
		}
	}
	return result, errors.Join(cleanupErrors...)
}

func validateHostMetricsCleanupOptions(opts HostMetricsCleanupOptions) error {
	if strings.TrimSpace(opts.SpaceID) == "" {
		return fmt.Errorf("host metrics cleanup space ID is required")
	}
	if len(opts.DatasetIDs) == 0 {
		return fmt.Errorf("host metrics cleanup dataset IDs are required")
	}
	for _, datasetID := range opts.DatasetIDs {
		if strings.TrimSpace(datasetID) == "" {
			return fmt.Errorf("host metrics cleanup dataset IDs must not contain blanks")
		}
	}
	if opts.MaxAge <= 0 {
		return fmt.Errorf("host metrics cleanup max age must be positive")
	}
	if opts.BatchSize < 1 || opts.BatchSize > 1000 {
		return fmt.Errorf("host metrics cleanup batch size must be between 1 and 1000")
	}
	if opts.MaxBatchesPerRun <= 0 {
		return fmt.Errorf("host metrics cleanup max batches per run must be positive")
	}
	if opts.Now.IsZero() {
		return fmt.Errorf("host metrics cleanup current time is required")
	}
	return nil
}
