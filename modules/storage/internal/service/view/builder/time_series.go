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
	return s.processTimeSeriesRowsBatch(ctx, rows)
}

func (s *Service) processTimeSeriesRowsBatch(ctx context.Context, rows []*pb.TimeSeriesRow, deleteBatches ...[]*pb.TimeSeriesRow) error {
	if len(rows) == 0 {
		return nil
	}
	if s == nil || s.metadata == nil {
		return errors.New("view builder time-series processor requires metadata client")
	}
	var deletes []*pb.TimeSeriesRow
	if len(deleteBatches) > 0 {
		deletes = deleteBatches[0]
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
				mapped, ok, err := viewsvc.FilteredTimeSeriesRowsForView(ctx, item, activeColumns, datasetRows, s.readTimeSeriesProjectionRows)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("view " + item.GetViewId() + " active schema is not projectable")
				}
				if len(mapped) > 0 || len(datasetDeletes) > 0 {
					if err := applyViewIndexWithDeletes(ctx, engine, item.GetActiveIndexId(), viewIndexBatch(item, activeColumns, mapped, nil, false), datasetDeletes, nil); err != nil {
						return fmt.Errorf("derive engine=duckdb view_id=%s rows=%d active write: %w", item.GetViewId(), len(mapped), err)
					}
				}
			}
			if writable[item.GetIndexBuild().GetIndexId()] && item.GetIndexBuild().GetIndexId() != item.GetActiveIndexId() {
				mapped, ok, err := viewsvc.FilteredTimeSeriesRowsForView(ctx, item, columns, datasetRows, s.readTimeSeriesProjectionRows)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("view " + item.GetViewId() + " build schema is not projectable")
				}
				if len(mapped) > 0 || len(datasetDeletes) > 0 {
					if err := applyViewIndexWithDeletes(ctx, engine, item.GetIndexBuild().GetIndexId(), viewIndexBatch(item, columns, mapped, nil, true), datasetDeletes, nil); err != nil {
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
