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

func (s *Service) processRecordBatch(ctx context.Context, keys []*pb.RecordKey) error {
	if len(keys) == 0 {
		return nil
	}
	if s == nil || s.reader == nil || s.metadata == nil {
		return errors.New("view builder record processor requires reader and metadata client")
	}
	rows, err := s.currentRecordRows(ctx, keys)
	if err != nil {
		return err
	}
	return s.processRecordRowsBatch(ctx, rows, nil)
}

func (s *Service) processRecordRowsBatch(ctx context.Context, rows []*pb.RecordRow, deleteBatches [][]*pb.RecordRow, progressBatches ...applyProgress) error {
	if s == nil || s.metadata == nil {
		return errors.New("view builder record processor requires metadata client")
	}
	var deletes []*pb.RecordRow
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
		return s.applyCheckpointOnly(ctx, progress, "bleve")
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
	groupedDeletes := make(map[projectionDatasetKey][]*pb.RecordRow)
	for _, row := range deletes {
		if row != nil && row.GetKey() != nil {
			key := row.GetKey()
			groupedDeletes[projectionDatasetKey{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}] = append(groupedDeletes[projectionDatasetKey{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}], row)
		}
	}
	for key := range groupedDeletes {
		if _, ok := grouped[key]; !ok {
			grouped[key] = nil
		}
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
				projected, ok, err := viewsvc.RecordRowsForView(ctx, item, activeColumns, datasetRows, nil)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("view " + item.GetViewId() + " active schema is not projectable")
				}
				projected = recordFragments(item, activeColumns, datasetRows)
				partialDeletes, fullDeletes := splitRecordDeletes(item, activeColumns, datasetDeletes)
				projected = append(projected, partialDeletes...)
				if len(projected) > 0 || len(datasetDeletes) > 0 || progress.sequence != 0 {
					if err := s.applyRecordIndexWithRecovery(ctx, engine, item, item.GetActiveIndexId(), activeColumns, projected, fullDeletes, false, progress); err != nil {
						return fmt.Errorf("derive engine=bleve view_id=%s rows=%d active write: %w", item.GetViewId(), len(projected), err)
					}
				}
			}
			if writable[item.GetIndexBuild().GetIndexId()] && item.GetIndexBuild().GetIndexId() != item.GetActiveIndexId() {
				projected, ok, err := viewsvc.RecordRowsForView(ctx, item, columns, datasetRows, nil)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("view " + item.GetViewId() + " build schema is not projectable")
				}
				projected = recordFragments(item, columns, datasetRows)
				partialDeletes, fullDeletes := splitRecordDeletes(item, columns, datasetDeletes)
				projected = append(projected, partialDeletes...)
				if len(projected) > 0 || len(datasetDeletes) > 0 || progress.sequence != 0 {
					if err := s.applyRecordIndexWithRecovery(ctx, engine, item, item.GetIndexBuild().GetIndexId(), columns, projected, fullDeletes, true, progress); err != nil {
						return fmt.Errorf("derive engine=bleve view_id=%s rows=%d build write: %w", item.GetViewId(), len(projected), err)
					}
				}
			}
		}
	}
	return nil
}

func (s *Service) currentRecordRows(ctx context.Context, keys []*pb.RecordKey) ([]*pb.RecordRow, error) {
	queryKeys := make([]*pb.RecordKey, 0, len(keys))
	for _, key := range keys {
		if key == nil {
			continue
		}
		queryKeys = append(queryKeys, proto.Clone(key).(*pb.RecordKey))
	}
	if len(queryKeys) == 0 {
		return nil, nil
	}
	rsp, err := s.reader.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{Keys: queryKeys})
	if err != nil {
		return nil, err
	}
	if rsp == nil {
		return nil, errors.New("read record rows returned nil response")
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp.GetRows(), nil
}

func (s *Service) readRecordProjectionRows(ctx context.Context, keys []*pb.RecordKey) ([]*pb.RecordRow, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rsp, err := s.reader.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{Keys: keys})
	if err != nil {
		return nil, err
	}
	if rsp == nil {
		return nil, errors.New("read record projection row returned nil response")
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp.GetRows(), nil
}
