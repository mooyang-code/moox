//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
)

// Apply commits row operations and progress metadata in one DuckDB transaction.
func (s *ViewStore) Apply(ctx context.Context, tableName string, batch viewindex.ViewIndexApplyBatch) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	if len(batch.RowWrites) == 0 && len(batch.CheckpointUpdates) == 0 && batch.IndexRangeUpdate == nil {
		return errors.New("view index apply batch is empty")
	}
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return err
	}
	unlock := s.lockResultTable(tableName)
	defer unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	columns, err := s.loadColumns(ctx, tableName)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(err error) error { _ = tx.Rollback(); return err }
	meta, err := readProgressTx(ctx, tx, tableName)
	if err != nil {
		return rollback(err)
	}
	if meta.ViewVersion != 0 && batch.ViewVersion == 0 {
		return rollback(errors.New("view schema version is required"))
	}
	if batch.ViewVersion != 0 && meta.ViewVersion != 0 && batch.ViewVersion != meta.ViewVersion {
		return rollback(fmt.Errorf("view schema version conflict: current=%d requested=%d", meta.ViewVersion, batch.ViewVersion))
	}
	if meta.SchemaHash != "" && batch.ViewSchemaHash == "" {
		return rollback(errors.New("view schema hash is required"))
	}
	if batch.ViewSchemaHash != "" && meta.SchemaHash != "" && batch.ViewSchemaHash != meta.SchemaHash {
		return rollback(fmt.Errorf("view schema hash conflict: current=%s requested=%s", meta.SchemaHash, batch.ViewSchemaHash))
	}
	if err := viewindex.ValidateIndexRangeProgress(meta.IndexedFrom, meta.IndexedTo, batch.IndexRangeUpdate); err != nil {
		return rollback(err)
	}
	covered, err := validateProgress(meta, batch.CheckpointUpdates)
	if err != nil {
		return rollback(err)
	}
	if covered {
		batch.RowWrites = nil
		batch.CheckpointUpdates = nil
	}

	upsertRows := make([]*pb.TimeSeriesRow, 0, len(batch.RowWrites))
	deleteRows := make([]*pb.TimeSeriesRow, 0, len(batch.RowWrites))
	for _, write := range batch.RowWrites {
		if write.Key.TimeSeriesKey == nil {
			return rollback(errors.New("duckdb apply requires time-series row keys"))
		}
		row := &pb.TimeSeriesRow{Key: write.Key.TimeSeriesKey, Columns: write.Columns, Attributes: write.Attributes, AttributesToDelete: write.AttributesToDelete, RemovedColumnNames: write.RemovedColumnNames, RemovedColumns: write.RemovedColumns, SourceShardId: write.SourceShardID, SourceSequence: write.SourceSequence}
		switch write.Operation {
		case viewindex.RowWriteOperationDelete:
			deleteRows = append(deleteRows, row)
		case viewindex.RowWriteOperationMerge, viewindex.RowWriteOperationReplace:
			upsertRows = append(upsertRows, row)
		default:
			return rollback(fmt.Errorf("unsupported row operation %d", write.Operation))
		}
	}

	existing, err := loadExistingRows(ctx, tx, quoted, append(append([]*pb.TimeSeriesRow{}, upsertRows...), deleteRows...))
	if err != nil {
		return rollback(err)
	}
	filteredDeletes := make([]*pb.TimeSeriesRow, 0, len(deleteRows))
	for _, write := range batch.RowWrites {
		if write.Operation != viewindex.RowWriteOperationDelete {
			continue
		}
		row := &pb.TimeSeriesRow{Key: write.Key.TimeSeriesKey, SourceShardId: write.SourceShardID, SourceSequence: write.SourceSequence}
		if base := existing[rowPrimaryKey(row)]; base != "" {
			decoded := &pb.TimeSeriesRow{}
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(base), decoded); err != nil {
				return rollback(err)
			}
			if viewindex.IsStaleSource(decoded.GetSourceShardId(), decoded.GetSourceSequence(), write.SourceShardID, write.SourceSequence) {
				continue
			}
		}
		filteredDeletes = append(filteredDeletes, row)
	}
	deleteRows = filteredDeletes
	missing := make([]*pb.TimeSeriesKey, 0)
	merged := make([]*pb.TimeSeriesRow, 0, len(upsertRows))
	for _, write := range batch.RowWrites {
		if write.Operation != viewindex.RowWriteOperationMerge && write.Operation != viewindex.RowWriteOperationReplace {
			continue
		}
		row := &pb.TimeSeriesRow{Key: write.Key.TimeSeriesKey, Columns: write.Columns, Attributes: write.Attributes, AttributesToDelete: write.AttributesToDelete, RemovedColumnNames: write.RemovedColumnNames, RemovedColumns: write.RemovedColumns, SourceShardId: write.SourceShardID, SourceSequence: write.SourceSequence}
		if base := existing[rowPrimaryKey(row)]; base != "" {
			decoded := &pb.TimeSeriesRow{}
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(base), decoded); err != nil {
				return rollback(err)
			}
			if viewindex.IsStaleSource(decoded.GetSourceShardId(), decoded.GetSourceSequence(), write.SourceShardID, write.SourceSequence) {
				continue
			}
		}
		if write.Operation == viewindex.RowWriteOperationReplace {
			if err := validateCompleteReplace(row, columns); err != nil {
				return rollback(err)
			}
		}
		base := existing[rowPrimaryKey(row)]
		if write.Operation == viewindex.RowWriteOperationMerge {
			if base == "" {
				missing = append(missing, row.GetKey())
				continue
			}
			decoded := &pb.TimeSeriesRow{}
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(base), decoded); err != nil {
				return rollback(err)
			}
			row = mergeTimeSeriesRow(decoded, row)
		} else {
			row = normalizeTimeSeriesRow(row)
		}
		merged = append(merged, row)
	}
	if len(missing) > 0 {
		return rollback(&viewindex.MissingRowsError{TimeSeriesKeys: missing})
	}

	deleteTargets := append(append([]*pb.TimeSeriesRow{}, deleteRows...), merged...)
	if err := deleteRowsByPrimaryKey(ctx, tx, quoted, deleteTargets); err != nil {
		return rollback(err)
	}
	if len(merged) > 0 {
		insertSQL, err := buildInsertSQL(quoted, columns)
		if err != nil {
			return rollback(err)
		}
		stmt, err := tx.PrepareContext(ctx, insertSQL)
		if err != nil {
			return rollback(err)
		}
		for _, row := range merged {
			args, err := resultRowArgs(row, columns)
			if err != nil {
				_ = stmt.Close()
				return rollback(err)
			}
			if _, err := stmt.ExecContext(ctx, args...); err != nil {
				_ = stmt.Close()
				return rollback(err)
			}
		}
		if err := stmt.Close(); err != nil {
			return rollback(err)
		}
	}
	newRows := int64(0)
	for _, row := range merged {
		if existing[rowPrimaryKey(row)] == "" {
			newRows++
		}
	}
	deletedRows := int64(0)
	for _, row := range deleteRows {
		if existing[rowPrimaryKey(row)] != "" {
			deletedRows++
		}
	}
	minVersion, maxVersion := timeSeriesRowsVersionBounds(merged)
	if err := updateIndexMetaTx(ctx, tx, tableName, newRows-deletedRows, minVersion, maxVersion); err != nil {
		return rollback(err)
	}
	if err := writeProgressTx(ctx, tx, tableName, meta, batch); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func validateCompleteReplace(row *pb.TimeSeriesRow, columns []*pb.ResultColumn) error {
	present := make(map[string]struct{}, len(row.GetColumns()))
	for _, column := range row.GetColumns() {
		if column != nil {
			present[column.GetColumnName()] = struct{}{}
		}
	}
	for _, column := range columns {
		if column == nil || column.GetColumnName() == "" {
			continue
		}
		if _, ok := present[column.GetColumnName()]; !ok {
			return fmt.Errorf("REPLACE row is missing view column %q", column.GetColumnName())
		}
	}
	return nil
}

type persistedProgress struct {
	ViewVersion uint64
	SchemaHash  string
	IndexedFrom string
	IndexedTo   string
	Checkpoints map[string]uint64
}

func readProgressTx(ctx context.Context, tx *sql.Tx, tableName string) (persistedProgress, error) {
	var indexedFrom, indexedTo, raw, schemaHash string
	var viewVersion uint64
	if err := tx.QueryRowContext(ctx, `SELECT view_version, schema_hash, indexed_from, indexed_to, checkpoints_json FROM moox_view_index_meta WHERE table_name = ?`, tableName).Scan(&viewVersion, &schemaHash, &indexedFrom, &indexedTo, &raw); err != nil {
		return persistedProgress{}, err
	}
	progress := persistedProgress{ViewVersion: viewVersion, SchemaHash: schemaHash, IndexedFrom: indexedFrom, IndexedTo: indexedTo, Checkpoints: make(map[string]uint64)}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &progress.Checkpoints); err != nil {
			return persistedProgress{}, err
		}
	}
	return progress, nil
}

func validateProgress(progress persistedProgress, updates []viewindex.ShardCheckpointUpdate) (bool, error) {
	if len(updates) == 0 {
		return false, nil
	}
	seen := make(map[string]struct{}, len(updates))
	covered := true
	sawCovered := false
	sawPending := false
	for _, update := range updates {
		if _, ok := seen[update.ShardID]; ok {
			return false, fmt.Errorf("duplicate checkpoint shard %q", update.ShardID)
		}
		seen[update.ShardID] = struct{}{}
		current := progress.Checkpoints[update.ShardID]
		if current == update.ExpectedLastAppliedSequence {
			covered = false
			sawPending = true
			continue
		}
		if current >= update.LastAppliedSequence {
			sawCovered = true
			continue
		}
		return false, fmt.Errorf("checkpoint conflict for shard %q: current=%d expected=%d last=%d", update.ShardID, current, update.ExpectedLastAppliedSequence, update.LastAppliedSequence)
	}
	if sawCovered && sawPending {
		return false, errors.New("checkpoint apply mixes covered and pending shards")
	}
	return covered, nil
}

func writeProgressTx(ctx context.Context, tx *sql.Tx, tableName string, progress persistedProgress, batch viewindex.ViewIndexApplyBatch) error {
	for _, update := range batch.CheckpointUpdates {
		progress.Checkpoints[update.ShardID] = update.LastAppliedSequence
	}
	if batch.IndexRangeUpdate != nil {
		if batch.IndexRangeUpdate.IndexedFrom != nil {
			progress.IndexedFrom = *batch.IndexRangeUpdate.IndexedFrom
		}
		if batch.IndexRangeUpdate.IndexedTo != nil {
			progress.IndexedTo = *batch.IndexRangeUpdate.IndexedTo
		}
	}
	raw, err := json.Marshal(progress.Checkpoints)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE moox_view_index_meta SET indexed_from = ?, indexed_to = ?, checkpoints_json = ? WHERE table_name = ?`, progress.IndexedFrom, progress.IndexedTo, string(raw), tableName)
	return err
}
