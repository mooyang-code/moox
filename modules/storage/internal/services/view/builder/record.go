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
	if s == nil || s.reader == nil || s.metadata == nil || s.search == nil {
		return errors.New("view builder record processor requires reader, metadata client and record indexer")
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
			columns, _, err := s.metadata.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), &pb.Page{Size: 10000})
			if err != nil {
				return err
			}
			projected, ok, err := viewsvc.RecordRowsForView(ctx, item, columns, datasetRows, s.readRecordProjectionRow)
			if err != nil {
				return err
			}
			if !ok {
				if err := markPending(ctx, s.metadata, item); err != nil {
					return err
				}
				continue
			}
			if item.GetActiveResult() != "" {
				if err := s.search.IndexRecordViewRows(ctx, item.GetActiveResult(), columns, projected); err != nil {
					return err
				}
			}
			if item.GetBuildingResult() != "" {
				if err := s.search.IndexRecordViewRows(ctx, item.GetBuildingResult(), columns, projected); err != nil {
					return err
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

func (s *Service) readRecordProjectionRow(ctx context.Context, base *pb.RecordKey, datasetID string) (*pb.RecordRow, error) {
	if base == nil {
		return nil, nil
	}
	key := proto.Clone(base).(*pb.RecordKey)
	key.DatasetId = datasetID
	rsp, err := s.reader.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{Keys: []*pb.RecordKey{key}})
	if err != nil {
		return nil, err
	}
	if rsp == nil {
		return nil, errors.New("read record projection row returned nil response")
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	if len(rsp.GetRows()) == 0 {
		return nil, nil
	}
	return rsp.GetRows()[0], nil
}
