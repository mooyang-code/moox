package builder

import (
	"context"
	"errors"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

func (s *Service) processRecordBatch(ctx context.Context, rows []*pb.RecordRow) error {
	rows = mergeRecordRowsLatestWins(rows)
	if len(rows) == 0 {
		return nil
	}
	if s == nil || s.metadata == nil || s.search == nil {
		return errors.New("view builder record processor requires metadata client and record indexer")
	}
	grouped := make(map[projectionDatasetKey][]*pb.RecordRow)
	for _, row := range rows {
		key := row.GetKey()
		if key == nil {
			continue
		}
		groupKey := projectionDatasetKey{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}
		grouped[groupKey] = append(grouped[groupKey], row)
	}
	for key, datasetRows := range grouped {
		views, err := s.metadata.ListViewsByDataset(ctx, key.spaceID, key.datasetID)
		if err != nil {
			return err
		}
		for _, item := range views {
			if !strings.EqualFold(item.GetEngine(), "bleve") {
				continue
			}
			columns, _, err := s.metadata.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), &pb.Page{Size: 10000})
			if err != nil {
				return err
			}
			if hasUnsupportedSteadyColumns(columns) {
				if err := markPending(ctx, s.metadata, item); err != nil {
					return err
				}
			}
			projected := MapRecordColumnsToView(item, columns, key.datasetID, datasetRows)
			if len(projected) == 0 {
				continue
			}
			writtenViews := 0
			if item.GetActiveResult() != "" {
				if err := s.indexRecordRows(ctx, item.GetActiveResult(), columns, projected); err != nil {
					return err
				}
				writtenViews++
			}
			if item.GetBuildingResult() != "" {
				if err := s.indexRecordRows(ctx, item.GetBuildingResult(), columns, projected); err != nil {
					return err
				}
				writtenViews++
			}
			log.InfoContextf(ctx, "[ViewBuilder] record journal applied dataset=%s/%s views=%d input_rows=%d merged_rows=%d", key.spaceID, key.datasetID, writtenViews, len(datasetRows), len(projected))
		}
	}
	return nil
}
