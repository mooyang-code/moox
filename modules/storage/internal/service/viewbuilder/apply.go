package builder

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func applyViewIndex(ctx context.Context, engine viewindex.ViewIndexEngine, indexID string, batch viewindex.BatchWrite) error {
	return applyViewIndexWithDeletes(ctx, engine, indexID, batch, nil, nil, applyProgress{})
}

func applyViewIndexWithDeletes(ctx context.Context, engine viewindex.ViewIndexEngine, indexID string, batch viewindex.BatchWrite, timeSeriesDeletes []*pb.TimeSeriesRow, recordDeletes []*pb.RecordRow, progress applyProgress) error {
	return applyViewIndexWithMode(ctx, engine, indexID, batch, timeSeriesDeletes, recordDeletes, progress, false)
}

func applyViewIndexWithMode(ctx context.Context, engine viewindex.ViewIndexEngine, indexID string, batch viewindex.BatchWrite, timeSeriesDeletes []*pb.TimeSeriesRow, recordDeletes []*pb.RecordRow, progress applyProgress, replace bool) error {
	if applier, ok := engine.(viewindex.ViewIndexApplier); ok {
		if progress.shardID != "" && progress.sequence != 0 {
			stats, err := engine.Stat(ctx, indexID)
			if err != nil {
				return err
			}
			progress.expected = stats.ShardCheckpoints[progress.shardID]
			if progress.expected >= progress.sequence {
				return nil
			}
			if progress.sequence != progress.expected+1 {
				return fmt.Errorf("view index shard %q sequence gap: got %d after durable checkpoint %d", progress.shardID, progress.sequence, progress.expected)
			}
		}
		operations := operationBatch(batch, timeSeriesDeletes, recordDeletes, progress, replace)
		return applier.Apply(ctx, indexID, operations)
	}
	return fmt.Errorf("view index engine does not support atomic apply")
}

func operationBatch(batch viewindex.BatchWrite, timeSeriesDeletes []*pb.TimeSeriesRow, recordDeletes []*pb.RecordRow, progress applyProgress, replace bool) viewindex.ViewIndexApplyBatch {
	result := viewindex.ViewIndexApplyBatch{ViewVersion: batch.ViewVersion, ViewSchemaHash: batch.SchemaHash, RequiredColumnNames: batch.RequiredColumnNames}
	if progress.shardID != "" && progress.sequence != 0 {
		result.CheckpointUpdates = append(result.CheckpointUpdates, viewindex.ShardCheckpointUpdate{ShardID: progress.shardID, ExpectedLastAppliedSequence: progress.expected, LastAppliedSequence: progress.sequence})
	}
	for _, row := range timeSeriesDeletes {
		if row != nil && row.GetKey() != nil {
			result.RowWrites = append(result.RowWrites, viewindex.RowWrite{Operation: viewindex.RowWriteOperationDelete, Key: viewindex.RowKey{TimeSeriesKey: row.GetKey()}, SourceShardID: row.GetSourceShardId(), SourceSequence: row.GetSourceSequence()})
		}
	}
	for _, row := range recordDeletes {
		if row != nil && row.GetKey() != nil {
			result.RowWrites = append(result.RowWrites, viewindex.RowWrite{Operation: viewindex.RowWriteOperationDelete, Key: viewindex.RowKey{RecordKey: row.GetKey()}, SourceShardID: row.GetSourceShardId(), SourceSequence: row.GetSourceSequence()})
		}
	}
	for _, row := range batch.TimeSeriesRows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		op := viewindex.RowWriteOperationMerge
		if replace {
			op = viewindex.RowWriteOperationReplace
		}
		result.RowWrites = append(result.RowWrites, viewindex.RowWrite{Operation: op, Key: viewindex.RowKey{TimeSeriesKey: row.GetKey()}, Columns: row.GetColumns(), Attributes: row.GetAttributes(), AttributesToDelete: row.GetAttributesToDelete(), RemovedColumnNames: row.GetRemovedColumnNames(), RemovedColumns: row.GetRemovedColumns(), SourceShardID: row.GetSourceShardId(), SourceSequence: row.GetSourceSequence()})
	}
	for _, row := range batch.RecordRows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		op := viewindex.RowWriteOperationMerge
		if replace {
			op = viewindex.RowWriteOperationReplace
		}
		result.RowWrites = append(result.RowWrites, viewindex.RowWrite{Operation: op, Key: viewindex.RowKey{RecordKey: row.GetKey()}, Columns: row.GetColumns(), Attributes: row.GetAttributes(), AttributesToDelete: row.GetAttributesToDelete(), RemovedColumnNames: row.GetRemovedColumnNames(), RemovedColumns: row.GetRemovedColumns(), SourceShardID: row.GetSourceShardId(), SourceSequence: row.GetSourceSequence()})
	}
	result.RowWrites = deduplicateRowWrites(result.RowWrites)
	return result
}

func deduplicateRowWrites(writes []viewindex.RowWrite) []viewindex.RowWrite {
	if len(writes) < 2 {
		return writes
	}
	positions := make(map[string]int, len(writes))
	merged := make([]viewindex.RowWrite, 0, len(writes))
	for _, write := range writes {
		identity, err := write.Key.Identity()
		if err != nil {
			continue
		}
		if index, exists := positions[identity]; exists {
			merged[index] = mergeDuplicateRowWrite(merged[index], write)
			continue
		}
		positions[identity] = len(merged)
		merged = append(merged, write)
	}
	return merged
}

func mergeDuplicateRowWrite(left, right viewindex.RowWrite) viewindex.RowWrite {
	if right.Operation == viewindex.RowWriteOperationDelete {
		return viewindex.RowWrite{Operation: right.Operation, Key: left.Key, SourceShardID: right.SourceShardID, SourceSequence: right.SourceSequence}
	}
	if left.Operation == viewindex.RowWriteOperationDelete {
		left.Operation = right.Operation
	}
	if right.Operation == viewindex.RowWriteOperationReplace {
		left.Operation = right.Operation
	}
	columnPositions := make(map[string]int, len(left.Columns))
	for index, column := range left.Columns {
		if column != nil {
			columnPositions[column.GetColumnName()] = index
		}
	}
	for _, column := range right.Columns {
		if column == nil {
			continue
		}
		name := column.GetColumnName()
		left.RemovedColumnNames = removeString(left.RemovedColumnNames, name)
		left.RemovedColumns = removeColumnRemoval(left.RemovedColumns, name)
		if index, ok := columnPositions[name]; ok {
			left.Columns[index] = proto.Clone(column).(*pb.ColumnValue)
		} else {
			columnPositions[name] = len(left.Columns)
			left.Columns = append(left.Columns, proto.Clone(column).(*pb.ColumnValue))
		}
	}
	for _, name := range right.RemovedColumnNames {
		left.Columns, columnPositions = removeColumn(left.Columns, columnPositions, name)
		left.RemovedColumns = removeColumnRemoval(left.RemovedColumns, name)
		if !containsString(left.RemovedColumnNames, name) {
			left.RemovedColumnNames = append(left.RemovedColumnNames, name)
		}
	}
	for _, removal := range right.RemovedColumns {
		if removal == nil {
			continue
		}
		name := removal.GetColumnName()
		left.Columns, columnPositions = removeColumn(left.Columns, columnPositions, name)
		left.RemovedColumnNames = removeString(left.RemovedColumnNames, name)
		left.RemovedColumns = appendColumnRemoval(left.RemovedColumns, removal)
	}
	if len(right.Attributes) > 0 {
		if left.Attributes == nil {
			left.Attributes = make(map[string]string, len(right.Attributes))
		}
		for key, value := range right.Attributes {
			left.Attributes[key] = value
			left.AttributesToDelete = removeString(left.AttributesToDelete, key)
		}
	}
	for _, key := range right.AttributesToDelete {
		delete(left.Attributes, key)
		if !containsString(left.AttributesToDelete, key) {
			left.AttributesToDelete = append(left.AttributesToDelete, key)
		}
	}
	left.SourceShardID, left.SourceSequence = right.SourceShardID, right.SourceSequence
	return left
}

func removeColumn(columns []*pb.ColumnValue, positions map[string]int, name string) ([]*pb.ColumnValue, map[string]int) {
	index, ok := positions[name]
	if !ok {
		return columns, positions
	}
	columns = append(columns[:index], columns[index+1:]...)
	positions = make(map[string]int, len(columns))
	for index, column := range columns {
		if column != nil {
			positions[column.GetColumnName()] = index
		}
	}
	return columns, positions
}

func removeColumnRemoval(removals []*pb.ColumnRemoval, name string) []*pb.ColumnRemoval {
	filtered := removals[:0]
	for _, removal := range removals {
		if removal != nil && removal.GetColumnName() != name {
			filtered = append(filtered, removal)
		}
	}
	return filtered
}

func appendColumnRemoval(removals []*pb.ColumnRemoval, removal *pb.ColumnRemoval) []*pb.ColumnRemoval {
	if removal == nil || removal.GetColumnName() == "" {
		return removals
	}
	return append(removeColumnRemoval(removals, removal.GetColumnName()), proto.Clone(removal).(*pb.ColumnRemoval))
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func removeString(values []string, target string) []string {
	filtered := values[:0]
	for _, value := range values {
		if value != target {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func validateOperationBatch(batch viewindex.ViewIndexApplyBatch) error {
	if err := batch.Validate(); err != nil {
		return fmt.Errorf("validate view index apply: %w", err)
	}
	return nil
}
