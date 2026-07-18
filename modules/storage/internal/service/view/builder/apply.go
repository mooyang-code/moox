package builder

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func applyViewIndex(ctx context.Context, engine viewindex.ViewIndexEngine, indexID string, batch viewindex.ViewIndexBatch) error {
	return applyViewIndexWithDeletes(ctx, engine, indexID, batch, nil, nil, applyProgress{})
}

func applyViewIndexWithDeletes(ctx context.Context, engine viewindex.ViewIndexEngine, indexID string, batch viewindex.ViewIndexBatch, timeSeriesDeletes []*pb.TimeSeriesRow, recordDeletes []*pb.RecordRow, progress applyProgress) error {
	return applyViewIndexWithMode(ctx, engine, indexID, batch, timeSeriesDeletes, recordDeletes, progress, false)
}

func applyViewIndexWithMode(ctx context.Context, engine viewindex.ViewIndexEngine, indexID string, batch viewindex.ViewIndexBatch, timeSeriesDeletes []*pb.TimeSeriesRow, recordDeletes []*pb.RecordRow, progress applyProgress, replace bool) error {
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
		}
		operations := operationBatch(batch, timeSeriesDeletes, recordDeletes, progress, replace)
		return applier.Apply(ctx, indexID, operations)
	}
	return fmt.Errorf("view index engine does not support atomic apply")
}

func operationBatch(batch viewindex.ViewIndexBatch, timeSeriesDeletes []*pb.TimeSeriesRow, recordDeletes []*pb.RecordRow, progress applyProgress, replace bool) viewindex.ViewIndexApplyBatch {
	result := viewindex.ViewIndexApplyBatch{ViewVersion: batch.ViewVersion, ViewSchemaHash: batch.SchemaHash, RequiredColumnNames: batch.RequiredColumnNames}
	if progress.shardID != "" && progress.sequence != 0 {
		result.CheckpointUpdates = append(result.CheckpointUpdates, viewindex.ShardCheckpointUpdate{ShardID: progress.shardID, ExpectedLastAppliedSequence: progress.expected, LastAppliedSequence: progress.sequence})
	}
	for _, row := range timeSeriesDeletes {
		if row != nil && row.GetKey() != nil {
			result.RowWrites = append(result.RowWrites, viewindex.RowWrite{Operation: viewindex.RowWriteOperationDelete, Key: viewindex.RowKey{TimeSeriesKey: row.GetKey()}})
		}
	}
	for _, row := range recordDeletes {
		if row != nil && row.GetKey() != nil {
			result.RowWrites = append(result.RowWrites, viewindex.RowWrite{Operation: viewindex.RowWriteOperationDelete, Key: viewindex.RowKey{RecordKey: row.GetKey()}})
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
		result.RowWrites = append(result.RowWrites, viewindex.RowWrite{Operation: op, Key: viewindex.RowKey{TimeSeriesKey: row.GetKey()}, Columns: row.GetColumns(), Attributes: row.GetAttributes(), AttributesToDelete: row.GetAttributesToDelete(), RemovedColumnNames: row.GetRemovedColumnNames()})
	}
	for _, row := range batch.RecordRows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		op := viewindex.RowWriteOperationMerge
		if replace {
			op = viewindex.RowWriteOperationReplace
		}
		result.RowWrites = append(result.RowWrites, viewindex.RowWrite{Operation: op, Key: viewindex.RowKey{RecordKey: row.GetKey()}, Columns: row.GetColumns(), Attributes: row.GetAttributes(), AttributesToDelete: row.GetAttributesToDelete(), RemovedColumnNames: row.GetRemovedColumnNames()})
	}
	return result
}

func validateOperationBatch(batch viewindex.ViewIndexApplyBatch) error {
	if err := batch.Validate(); err != nil {
		return fmt.Errorf("validate view index apply: %w", err)
	}
	return nil
}
