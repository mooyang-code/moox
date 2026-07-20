//go:build legacy_storage

package builder

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type timeSeriesDeleter interface {
	DeleteTimeSeriesRows(context.Context, string, []*pb.TimeSeriesRow) error
}

type recordDeleter interface {
	DeleteRecordRows(context.Context, string, []*pb.RecordRow) error
}

func (s *Service) processTimeSeriesDeletesBatch(ctx context.Context, rows []*pb.TimeSeriesRow) error {
	if len(rows) == 0 {
		return nil
	}
	grouped := make(map[projectionDatasetKey][]*pb.TimeSeriesRow)
	for _, row := range rows {
		if row != nil && row.GetKey() != nil {
			key := row.GetKey()
			grouped[projectionDatasetKey{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}] = append(grouped[projectionDatasetKey{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}], row)
		}
	}
	for key, datasetRows := range grouped {
		views, err := s.metadata.ListViewsByDataset(ctx, key.spaceID, key.datasetID)
		if err != nil {
			return err
		}
		for _, view := range views {
			if !strings.EqualFold(view.GetEngine(), "duckdb") {
				continue
			}
			engine, err := s.engine(view.GetEngine())
			if err != nil {
				return err
			}
			deleter, ok := engine.(timeSeriesDeleter)
			if !ok {
				return errors.New("duckdb view index does not support delete")
			}
			for _, indexID := range deleteIndexIDs(view) {
				if err := deleter.DeleteTimeSeriesRows(ctx, indexID, datasetRows); err != nil {
					return fmt.Errorf("delete engine=duckdb view_id=%s index_id=%s: %w", view.GetViewId(), indexID, err)
				}
			}
		}
	}
	return nil
}

func (s *Service) processRecordDeletesBatch(ctx context.Context, rows []*pb.RecordRow) error {
	if len(rows) == 0 {
		return nil
	}
	grouped := make(map[projectionDatasetKey][]*pb.RecordRow)
	for _, row := range rows {
		if row != nil && row.GetKey() != nil {
			key := row.GetKey()
			grouped[projectionDatasetKey{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}] = append(grouped[projectionDatasetKey{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}], row)
		}
	}
	for key, datasetRows := range grouped {
		views, err := s.metadata.ListViewsByDataset(ctx, key.spaceID, key.datasetID)
		if err != nil {
			return err
		}
		for _, view := range views {
			if !strings.EqualFold(view.GetEngine(), "bleve") {
				continue
			}
			engine, err := s.engine(view.GetEngine())
			if err != nil {
				return err
			}
			deleter, ok := engine.(recordDeleter)
			if !ok {
				return errors.New("bleve view index does not support delete")
			}
			for _, indexID := range deleteIndexIDs(view) {
				if err := deleter.DeleteRecordRows(ctx, indexID, datasetRows); err != nil {
					return fmt.Errorf("delete engine=bleve view_id=%s index_id=%s: %w", view.GetViewId(), indexID, err)
				}
			}
		}
	}
	return nil
}

func deleteIndexIDs(view *pb.View) []string {
	if view == nil {
		return nil
	}
	ids := make([]string, 0, 2)
	if id := view.GetActiveIndexId(); id != "" {
		ids = append(ids, id)
	}
	if build := view.GetIndexBuild(); build != nil && build.GetIndexId() != "" && build.GetIndexId() != view.GetActiveIndexId() && viewindex.BuildIndexWritable(view) {
		ids = append(ids, build.GetIndexId())
	}
	return ids
}
