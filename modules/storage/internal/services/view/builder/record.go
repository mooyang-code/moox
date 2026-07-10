package builder

import (
	"context"
	"errors"
	"strings"

	viewsvc "github.com/mooyang-code/moox/modules/storage/internal/services/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
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
	if len(rows) == 0 {
		return nil
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
			engine, err := s.engine(item.GetEngine())
			if err != nil {
				return err
			}
			columns, _, err := s.metadata.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), &pb.Page{Size: 10000})
			if err != nil {
				return err
			}
			writable := writableIndexSet(item)
			if writable[item.GetActiveIndexId()] {
				activeColumns := item.GetActiveColumns()
				if len(activeColumns) == 0 {
					return errors.New("view " + item.GetViewId() + " has an active index without an active schema")
				}
				projected, ok, err := viewsvc.RecordRowsForView(ctx, item, activeColumns, datasetRows, s.readRecordProjectionRows)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("view " + item.GetViewId() + " active schema is not projectable")
				}
				if len(projected) > 0 {
					if err := engine.Write(ctx, item.GetActiveIndexId(), viewIndexBatch(item, activeColumns, nil, projected, false)); err != nil {
						return err
					}
				}
			}
			if writable[item.GetIndexBuild().GetIndexId()] && item.GetIndexBuild().GetIndexId() != item.GetActiveIndexId() {
				projected, ok, err := viewsvc.RecordRowsForView(ctx, item, columns, datasetRows, s.readRecordProjectionRows)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("view " + item.GetViewId() + " build schema is not projectable")
				}
				if len(projected) > 0 {
					if err := engine.Write(ctx, item.GetIndexBuild().GetIndexId(), viewIndexBatch(item, columns, nil, projected, true)); err != nil {
						return err
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
