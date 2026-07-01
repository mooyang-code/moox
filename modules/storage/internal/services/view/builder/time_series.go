package builder

import (
	"context"
	"errors"
	"strings"

	viewsvc "github.com/mooyang-code/moox/modules/storage/internal/services/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func (s *Service) processTimeSeriesBatch(ctx context.Context, keys []*pb.TimeSeriesKey) error {
	if len(keys) == 0 {
		return nil
	}
	if s == nil || s.reader == nil || s.metadata == nil || s.views == nil {
		return errors.New("view builder time-series processor requires reader, metadata client and view writer")
	}
	rows, err := s.currentTimeSeriesRows(ctx, keys)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
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
	for key, datasetRows := range grouped {
		views, err := s.metadata.ListViewsByDataset(ctx, key.spaceID, key.datasetID)
		if err != nil {
			return err
		}
		for _, item := range views {
			if !strings.EqualFold(item.GetEngine(), "duckdb") {
				continue
			}
			columns, _, err := s.metadata.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), &pb.Page{Size: 10000})
			if err != nil {
				return err
			}
			mapped, ok, err := viewsvc.TimeSeriesRowsForView(ctx, item, columns, datasetRows, s.readTimeSeriesProjectionRow)
			if err != nil {
				return err
			}
			if !ok {
				if err := markPending(ctx, s.metadata, item); err != nil {
					return err
				}
				continue
			}
			if len(mapped) == 0 {
				continue
			}
			if item.GetActiveResult() != "" {
				if err := s.views.InsertRows(ctx, item.GetActiveResult(), mapped); err != nil {
					return err
				}
			}
			if item.GetBuildingResult() != "" {
				if err := s.views.InsertRows(ctx, item.GetBuildingResult(), mapped); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Service) currentTimeSeriesRows(ctx context.Context, keys []*pb.TimeSeriesKey) ([]*pb.TimeSeriesRow, error) {
	var out []*pb.TimeSeriesRow
	for _, key := range keys {
		if key == nil {
			continue
		}
		rsp, err := s.reader.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{Keys: []*pb.TimeSeriesKey{proto.Clone(key).(*pb.TimeSeriesKey)}})
		if err != nil {
			return nil, err
		}
		if rsp == nil {
			return nil, errors.New("read time-series rows returned nil response")
		}
		if err := retInfoError(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		out = append(out, rsp.GetRows()...)
	}
	return out, nil
}

func (s *Service) readTimeSeriesProjectionRow(ctx context.Context, base *pb.TimeSeriesKey, datasetID string) (*pb.TimeSeriesRow, error) {
	if base == nil {
		return nil, nil
	}
	key := proto.Clone(base).(*pb.TimeSeriesKey)
	key.DatasetId = datasetID
	rsp, err := s.reader.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{Keys: []*pb.TimeSeriesKey{key}})
	if err != nil {
		return nil, err
	}
	if rsp == nil {
		return nil, errors.New("read time-series projection row returned nil response")
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	if len(rsp.GetRows()) == 0 {
		return nil, nil
	}
	return rsp.GetRows()[0], nil
}
