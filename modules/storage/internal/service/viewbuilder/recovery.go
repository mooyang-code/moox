package builder

import (
	"context"
	"errors"

	viewsvc "github.com/mooyang-code/moox/modules/storage/internal/service/dataview"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// applyTimeSeriesIndexWithRecovery retries a failed MERGE as a full REPLACE
// after rebuilding the affected logical rows from every source dataset.
func (s *Service) applyTimeSeriesIndexWithRecovery(ctx context.Context, engine viewindex.ViewIndexEngine, item *pb.View, indexID string, columns []*pb.ViewColumn, rows, deletes []*pb.TimeSeriesRow, warming bool, progress applyProgress) error {
	batch := viewIndexBatch(item, columns, rows, nil, warming)
	err := applyViewIndexWithDeletes(ctx, engine, indexID, batch, deletes, nil, progress)
	if err == nil {
		return nil
	}
	var missing *viewindex.MissingRowsError
	if !errors.As(err, &missing) || len(missing.TimeSeriesKeys) == 0 {
		return err
	}
	recovered, err := s.recoverTimeSeriesRows(ctx, item, columns, missing.TimeSeriesKeys)
	if err != nil {
		return err
	}
	rows = mergeTimeSeriesRecoveryRows(rows, recovered)
	deletes = appendMissingTimeSeriesDeletes(deletes, missing.TimeSeriesKeys, append(rows, recovered...))
	return applyViewIndexWithMode(ctx, engine, indexID, viewIndexBatch(item, columns, rows, nil, warming), deletes, nil, progress, true)
}

func (s *Service) recoverTimeSeriesRows(ctx context.Context, item *pb.View, columns []*pb.ViewColumn, keys []*pb.TimeSeriesKey) ([]*pb.TimeSeriesRow, error) {
	readKeys := make([]*pb.TimeSeriesKey, 0, len(keys)*len(columns)+len(keys))
	for _, key := range keys {
		if key == nil {
			continue
		}
		for _, datasetID := range viewsvc.ViewRowMapperDatasets(item.GetPrimaryDatasetId(), columns) {
			copyKey := proto.Clone(key).(*pb.TimeSeriesKey)
			copyKey.DatasetId = datasetID
			readKeys = append(readKeys, copyKey)
		}
	}
	sourceRows, err := s.readTimeSeriesRowMapperRows(ctx, readKeys)
	if err != nil {
		return nil, err
	}
	recovered, ok, err := viewsvc.FilteredTimeSeriesRowsForView(ctx, item, columns, sourceRows, nil)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("view schema is not projectable during time-series recovery")
	}
	return recovered, nil
}

func mergeTimeSeriesRecoveryRows(rows, recovered []*pb.TimeSeriesRow) []*pb.TimeSeriesRow {
	seen := make(map[string]struct{}, len(rows)+len(recovered))
	out := make([]*pb.TimeSeriesRow, 0, len(rows)+len(recovered))
	for _, row := range append(append([]*pb.TimeSeriesRow{}, recovered...), rows...) {
		if row == nil || row.GetKey() == nil {
			continue
		}
		key := viewsvc.TimeSeriesRowMapperGrainKey(row.GetKey())
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, row)
	}
	return out
}

func appendMissingTimeSeriesDeletes(deletes []*pb.TimeSeriesRow, keys []*pb.TimeSeriesKey, available []*pb.TimeSeriesRow) []*pb.TimeSeriesRow {
	seen := make(map[string]struct{}, len(deletes)+len(available))
	for _, row := range deletes {
		if row != nil && row.GetKey() != nil {
			seen[viewsvc.TimeSeriesRowMapperGrainKey(row.GetKey())] = struct{}{}
		}
	}
	for _, row := range available {
		if row != nil && row.GetKey() != nil {
			seen[viewsvc.TimeSeriesRowMapperGrainKey(row.GetKey())] = struct{}{}
		}
	}
	for _, key := range keys {
		if key == nil {
			continue
		}
		identity := viewsvc.TimeSeriesRowMapperGrainKey(key)
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		deletes = append(deletes, &pb.TimeSeriesRow{Key: proto.Clone(key).(*pb.TimeSeriesKey)})
	}
	return deletes
}

// applyRecordIndexWithRecovery is the record-index counterpart of the
// time-series recovery path above.
func (s *Service) applyRecordIndexWithRecovery(ctx context.Context, engine viewindex.ViewIndexEngine, item *pb.View, indexID string, columns []*pb.ViewColumn, rows, deletes []*pb.RecordRow, warming bool, progress applyProgress) error {
	batch := viewIndexBatch(item, columns, nil, rows, warming)
	err := applyViewIndexWithDeletes(ctx, engine, indexID, batch, nil, deletes, progress)
	if err == nil {
		return nil
	}
	var missing *viewindex.MissingRowsError
	if !errors.As(err, &missing) || len(missing.RecordKeys) == 0 {
		return err
	}
	recovered, err := s.recoverRecordRows(ctx, item, columns, missing.RecordKeys)
	if err != nil {
		return err
	}
	rows = mergeRecordRecoveryRows(rows, recovered)
	deletes = appendMissingRecordDeletes(deletes, missing.RecordKeys, append(rows, recovered...))
	return applyViewIndexWithMode(ctx, engine, indexID, viewIndexBatch(item, columns, nil, rows, warming), nil, deletes, progress, true)
}

func (s *Service) recoverRecordRows(ctx context.Context, item *pb.View, columns []*pb.ViewColumn, keys []*pb.RecordKey) ([]*pb.RecordRow, error) {
	readKeys := make([]*pb.RecordKey, 0, len(keys)*len(columns)+len(keys))
	for _, key := range keys {
		if key == nil {
			continue
		}
		for _, datasetID := range viewsvc.ViewRowMapperDatasets(item.GetPrimaryDatasetId(), columns) {
			copyKey := proto.Clone(key).(*pb.RecordKey)
			copyKey.DatasetId = datasetID
			readKeys = append(readKeys, copyKey)
		}
	}
	sourceRows, err := s.readRecordRowMapperRows(ctx, readKeys)
	if err != nil {
		return nil, err
	}
	recovered, ok, err := viewsvc.RecordRowsForView(ctx, item, columns, sourceRows, nil)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("view schema is not projectable during record recovery")
	}
	return recovered, nil
}

func mergeRecordRecoveryRows(rows, recovered []*pb.RecordRow) []*pb.RecordRow {
	seen := make(map[string]struct{}, len(rows)+len(recovered))
	out := make([]*pb.RecordRow, 0, len(rows)+len(recovered))
	for _, row := range append(append([]*pb.RecordRow{}, recovered...), rows...) {
		if row == nil || row.GetKey() == nil {
			continue
		}
		key := viewsvc.RecordRowMapperGrainKey(row.GetKey())
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, row)
	}
	return out
}

func appendMissingRecordDeletes(deletes []*pb.RecordRow, keys []*pb.RecordKey, available []*pb.RecordRow) []*pb.RecordRow {
	seen := make(map[string]struct{}, len(deletes)+len(available))
	for _, row := range deletes {
		if row != nil && row.GetKey() != nil {
			seen[viewsvc.RecordRowMapperGrainKey(row.GetKey())] = struct{}{}
		}
	}
	for _, row := range available {
		if row != nil && row.GetKey() != nil {
			seen[viewsvc.RecordRowMapperGrainKey(row.GetKey())] = struct{}{}
		}
	}
	for _, key := range keys {
		if key == nil {
			continue
		}
		identity := viewsvc.RecordRowMapperGrainKey(key)
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		deletes = append(deletes, &pb.RecordRow{Key: proto.Clone(key).(*pb.RecordKey)})
	}
	return deletes
}
