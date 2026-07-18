package viewindex

import (
	"errors"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// RowWriteOperation describes how a ViewIndex row is applied.
type RowWriteOperation uint8

const (
	RowWriteOperationUnspecified RowWriteOperation = iota
	RowWriteOperationMerge
	RowWriteOperationReplace
	RowWriteOperationDelete
)

// RowKey is the identity of one materialized ViewIndex row. Exactly one key
// variant must be set because time-series and record rows have different key
// semantics.
type RowKey struct {
	TimeSeriesKey *pb.TimeSeriesKey
	RecordKey     *pb.RecordKey
}

// RowWrite is one atomic row operation within a ViewIndex apply.
type RowWrite struct {
	Operation          RowWriteOperation
	Key                RowKey
	Columns            []*pb.ColumnValue
	Attributes         map[string]string
	AttributesToDelete []string
	RemovedColumnNames []string
}

// MissingRowsError reports MERGE targets that are absent from an index. The
// builder can use the keys to rebuild complete rows from their source datasets
// and retry the same atomic batch as REPLACE operations.
type MissingRowsError struct {
	TimeSeriesKeys []*pb.TimeSeriesKey
	RecordKeys     []*pb.RecordKey
}

func (e *MissingRowsError) Error() string {
	return fmt.Sprintf("view index merge targets are missing: %d", len(e.TimeSeriesKeys)+len(e.RecordKeys))
}

// ShardCheckpointUpdate advances one source shard checkpoint with a compare
// and swap guard supplied by the caller.
type ShardCheckpointUpdate struct {
	ShardID                     string
	ExpectedLastAppliedSequence uint64
	LastAppliedSequence         uint64
}

// Validate checks the compare-and-swap checkpoint bound.
func (u ShardCheckpointUpdate) Validate() error {
	if u.ShardID == "" {
		return errors.New("ShardID is required")
	}
	if u.LastAppliedSequence <= u.ExpectedLastAppliedSequence {
		return fmt.Errorf("LastAppliedSequence %d must advance ExpectedLastAppliedSequence %d", u.LastAppliedSequence, u.ExpectedLastAppliedSequence)
	}
	return nil
}

// IndexRangeUpdate changes one or both indexed range boundaries. A nil field
// means that boundary is left unchanged.
type IndexRangeUpdate struct {
	IndexedFrom *string
	IndexedTo   *string
}

// ViewIndexApplyBatch is the complete atomic command applied to one index.
// At least one of RowWrites, CheckpointUpdates, or IndexRangeUpdate must be
// present.
type ViewIndexApplyBatch struct {
	RowWrites           []RowWrite
	CheckpointUpdates   []ShardCheckpointUpdate
	ViewVersion         uint64
	ViewSchemaHash      string
	IndexRangeUpdate    *IndexRangeUpdate
	RequiredColumnNames []string
}

// Validate checks that the key is represented by exactly one row-key variant.
func (k RowKey) Validate() error {
	set := 0
	if k.TimeSeriesKey != nil {
		set++
	}
	if k.RecordKey != nil {
		set++
	}
	if set != 1 {
		return errors.New("row key must set exactly one of time series or record key")
	}
	return nil
}

// Validate checks the operation, key, and operation-specific payload rules.
func (w RowWrite) Validate() error {
	switch w.Operation {
	case RowWriteOperationMerge, RowWriteOperationReplace:
	case RowWriteOperationDelete:
		if len(w.Columns) != 0 || len(w.Attributes) != 0 || len(w.AttributesToDelete) != 0 || len(w.RemovedColumnNames) != 0 {
			return errors.New("DELETE row write must not include columns or attributes")
		}
	default:
		return fmt.Errorf("row write operation %d is invalid", w.Operation)
	}
	if err := w.Key.Validate(); err != nil {
		return err
	}
	return nil
}

// Validate checks all row writes and progress updates before an engine can
// persist any part of the apply.
func (b ViewIndexApplyBatch) Validate() error {
	if len(b.RowWrites) == 0 && len(b.CheckpointUpdates) == 0 && b.IndexRangeUpdate == nil {
		return errors.New("view index apply batch is empty")
	}

	seen := make(map[string]struct{}, len(b.RowWrites))
	for i, write := range b.RowWrites {
		if err := write.Validate(); err != nil {
			return fmt.Errorf("row write %d: %w", i, err)
		}
		identity, err := write.Key.identity()
		if err != nil {
			return fmt.Errorf("row write %d: %w", i, err)
		}
		if _, exists := seen[identity]; exists {
			return fmt.Errorf("row write %d duplicates a row key", i)
		}
		seen[identity] = struct{}{}
		if write.Operation == RowWriteOperationReplace && len(b.RequiredColumnNames) > 0 {
			present := make(map[string]struct{}, len(write.Columns))
			for _, column := range write.Columns {
				if column != nil && column.GetColumnName() != "" {
					present[column.GetColumnName()] = struct{}{}
				}
			}
			for _, name := range b.RequiredColumnNames {
				if name == "" {
					continue
				}
				if _, ok := present[name]; !ok {
					return fmt.Errorf("row write %d REPLACE is missing view column %q", i, name)
				}
			}
		}
	}

	for i, update := range b.CheckpointUpdates {
		if err := update.Validate(); err != nil {
			return fmt.Errorf("checkpoint update %d: %w", i, err)
		}
	}
	return nil
}

func (k RowKey) identity() (string, error) {
	if err := k.Validate(); err != nil {
		return "", err
	}
	marshal := proto.MarshalOptions{Deterministic: true}
	if k.TimeSeriesKey != nil {
		raw, err := marshal.Marshal(k.TimeSeriesKey)
		if err != nil {
			return "", fmt.Errorf("marshal time series row key: %w", err)
		}
		return "time-series:" + string(raw), nil
	}
	raw, err := marshal.Marshal(k.RecordKey)
	if err != nil {
		return "", fmt.Errorf("marshal record row key: %w", err)
	}
	return "record:" + string(raw), nil
}
