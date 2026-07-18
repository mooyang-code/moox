package builder

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func applyViewIndex(ctx context.Context, engine viewindex.ViewIndexEngine, indexID string, batch viewindex.ViewIndexBatch) error {
	return applyViewIndexWithDeletes(ctx, engine, indexID, batch, nil, nil)
}

func applyViewIndexWithDeletes(ctx context.Context, engine viewindex.ViewIndexEngine, indexID string, batch viewindex.ViewIndexBatch, timeSeriesDeletes []*pb.TimeSeriesRow, recordDeletes []*pb.RecordRow) error {
	if applier, ok := engine.(viewindex.ViewIndexApplier); ok {
		operations := operationBatch(batch, timeSeriesDeletes, recordDeletes)
		return applier.Apply(ctx, indexID, operations)
	}
	if len(timeSeriesDeletes) > 0 || len(recordDeletes) > 0 {
		return fmt.Errorf("view index engine does not support atomic delete operations")
	}
	return engine.Write(ctx, indexID, batch)
}

func operationBatch(batch viewindex.ViewIndexBatch, timeSeriesDeletes []*pb.TimeSeriesRow, recordDeletes []*pb.RecordRow) viewindex.ViewIndexApplyBatch {
	result := viewindex.ViewIndexApplyBatch{ViewVersion: batch.ViewVersion, ViewSchemaHash: batch.SchemaHash}
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
		result.RowWrites = append(result.RowWrites, viewindex.RowWrite{Operation: viewindex.RowWriteOperationMerge, Key: viewindex.RowKey{TimeSeriesKey: row.GetKey()}, Columns: row.GetColumns(), Attributes: row.GetAttributes()})
	}
	for _, row := range batch.RecordRows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		result.RowWrites = append(result.RowWrites, viewindex.RowWrite{Operation: viewindex.RowWriteOperationMerge, Key: viewindex.RowKey{RecordKey: row.GetKey()}, Columns: row.GetColumns(), Attributes: row.GetAttributes()})
	}
	return result
}

func validateOperationBatch(batch viewindex.ViewIndexApplyBatch) error {
	if err := batch.Validate(); err != nil {
		return fmt.Errorf("validate view index apply: %w", err)
	}
	return nil
}
