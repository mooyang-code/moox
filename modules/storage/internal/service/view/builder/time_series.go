package builder

import (
	"context"
	"errors"
	"fmt"
	"strings"

	viewsvc "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func (s *Service) processTimeSeriesBatch(ctx context.Context, keys []*pb.TimeSeriesKey) error {
	if len(keys) == 0 {
		return nil
	}
	if s == nil || s.reader == nil || s.metadata == nil {
		return errors.New("view builder time-series processor requires reader and metadata client")
	}
	rows, err := s.currentTimeSeriesRows(ctx, keys)
	if err != nil {
		return err
	}
	return s.processTimeSeriesRowsBatch(ctx, rows, nil)
}

func (s *Service) processTimeSeriesRowsBatch(ctx context.Context, rows []*pb.TimeSeriesRow, deleteBatches [][]*pb.TimeSeriesRow, progressBatches ...applyProgress) error {
	if s == nil || s.metadata == nil {
		return errors.New("view builder time-series processor requires metadata client")
	}
	var deletes []*pb.TimeSeriesRow
	if len(deleteBatches) > 0 {
		deletes = deleteBatches[0]
	}
	var progress applyProgress
	if len(progressBatches) > 0 {
		progress = progressBatches[0]
	}
	if len(rows) == 0 && len(deletes) == 0 {
		if progress.shardID == "" || progress.sequence == 0 || progress.spaceID == "" || progress.datasetID == "" {
			return nil
		}
		return s.applyCheckpointOnly(ctx, progress, "duckdb")
	}
	grouped := make(map[projectionDatasetKey][]*pb.TimeSeriesRow)
	for _, row := range rows {
		key := row.GetKey()
		if key == nil {
			continue
		}
		groupKey := projectionDatasetKey{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}
		grouped[groupKey] = append(grouped[groupKey], row)
	}
	groupedDeletes := make(map[projectionDatasetKey][]*pb.TimeSeriesRow)
	for _, row := range deletes {
		if row != nil && row.GetKey() != nil {
			key := row.GetKey()
			groupedDeletes[projectionDatasetKey{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}] = append(groupedDeletes[projectionDatasetKey{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}], row)
		}
	}
	for key, datasetDeletes := range groupedDeletes {
		if _, ok := grouped[key]; !ok {
			grouped[key] = nil
		}
		_ = datasetDeletes
	}
	for key, datasetRows := range grouped {
		views, err := s.metadata.ListViewsByDataset(ctx, key.spaceID, key.datasetID)
		if err != nil {
			return err
		}
		for _, item := range views {
			if !strings.EqualFold(item.GetEngine(), "duckdb") {
				continue
			}
			engine, err := s.engine(item.GetEngine())
			if err != nil {
				return err
			}
			columns, _, err := s.metadata.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), &pb.Page{Size: 10000})
			if err != nil {
				return err
			}
			writable := writableIndexSet(item)
			datasetDeletes := groupedDeletes[key]
			if writable[item.GetActiveIndexId()] {
				activeColumns := item.GetActiveColumns()
				if len(activeColumns) == 0 {
					return errors.New("view " + item.GetViewId() + " has an active index without an active schema")
				}
				datasetRows, ok, err := viewsvc.FilteredTimeSeriesSourceRowsForView(item, datasetRows)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("view " + item.GetViewId() + " active schema is not projectable")
				}
				mapped := timeSeriesFragments(item, activeColumns, datasetRows)
				partialDeletes, fullDeletes := splitTimeSeriesDeletes(item, activeColumns, datasetDeletes)
				mapped = append(mapped, partialDeletes...)
				if len(mapped) > 0 || len(datasetDeletes) > 0 || progress.sequence != 0 {
					if err := s.applyTimeSeriesIndexWithRecovery(ctx, engine, item, item.GetActiveIndexId(), activeColumns, mapped, fullDeletes, false, progress); err != nil {
						return fmt.Errorf("derive engine=duckdb view_id=%s rows=%d active write: %w", item.GetViewId(), len(mapped), err)
					}
				}
			}
			if writable[item.GetIndexBuild().GetIndexId()] && item.GetIndexBuild().GetIndexId() != item.GetActiveIndexId() {
				datasetRows, ok, err := viewsvc.FilteredTimeSeriesSourceRowsForView(item, datasetRows)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("view " + item.GetViewId() + " build schema is not projectable")
				}
				mapped := timeSeriesFragments(item, columns, datasetRows)
				partialDeletes, fullDeletes := splitTimeSeriesDeletes(item, columns, datasetDeletes)
				mapped = append(mapped, partialDeletes...)
				if len(mapped) > 0 || len(datasetDeletes) > 0 || progress.sequence != 0 {
					if err := s.applyTimeSeriesIndexWithRecovery(ctx, engine, item, item.GetIndexBuild().GetIndexId(), columns, mapped, fullDeletes, true, progress); err != nil {
						return fmt.Errorf("derive engine=duckdb view_id=%s rows=%d build write: %w", item.GetViewId(), len(mapped), err)
					}
				}
			}
		}
	}
	return nil
}

func (s *Service) currentTimeSeriesRows(ctx context.Context, keys []*pb.TimeSeriesKey) ([]*pb.TimeSeriesRow, error) {
	queryKeys := make([]*pb.TimeSeriesKey, 0, len(keys))
	for _, key := range keys {
		if key == nil {
			continue
		}
		queryKeys = append(queryKeys, proto.Clone(key).(*pb.TimeSeriesKey))
	}
	return s.readTimeSeriesProjectionRows(ctx, queryKeys)
}

func (s *Service) readTimeSeriesProjectionRows(ctx context.Context, keys []*pb.TimeSeriesKey) ([]*pb.TimeSeriesRow, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rsp, err := s.reader.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{Keys: keys})
	if err != nil {
		return nil, err
	}
	if rsp == nil {
		return nil, errors.New("read time-series projection row returned nil response")
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp.GetRows(), nil
}
