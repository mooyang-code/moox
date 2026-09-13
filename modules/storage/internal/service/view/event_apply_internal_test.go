package view

import (
	"context"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// Direct index tests have no durable delivery whose readiness must be emitted.
func (s *Service) applyDatasetEvent(ctx context.Context, spaceID, datasetID string, rows []*pb.RowFieldUpsert) error {
	return s.applyDatasetEventWithOrigins(ctx, spaceID, datasetID, rows, nil)
}
