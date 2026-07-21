//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

type IndexManagerOptions struct{ Root string }
type IndexManager struct{ root string }

func OpenIndexManager(opts IndexManagerOptions) (*IndexManager, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, errors.New("view index root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &IndexManager{root: root}, nil
}

func (m *IndexManager) Engine() string { return "duckdb" }

func (m *IndexManager) Prepare(ctx context.Context, id string, schema viewindex.ViewIndexSchema) error {
	path, err := m.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	db, err := open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE view_meta (
			singleton INTEGER PRIMARY KEY,
			view_version UBIGINT NOT NULL,
			schema_hash VARCHAR NOT NULL,
			updated_at VARCHAR NOT NULL
		);
		CREATE TABLE view_rows (
			row_id VARCHAR PRIMARY KEY,
			row_proto BLOB NOT NULL
		);
	`); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO view_meta VALUES (1, ?, ?, ?)`,
		schema.ViewVersion, schema.SchemaHash, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (m *IndexManager) Apply(ctx context.Context, id string, batch viewindex.ViewIndexApplyBatch) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	db, err := m.openExisting(id)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var revision uint64
	var schemaHash string
	if err := tx.QueryRowContext(ctx, `SELECT view_version, schema_hash FROM view_meta WHERE singleton = 1`).Scan(&revision, &schemaHash); err != nil {
		return err
	}
	if revision != batch.ViewRevision {
		return fmt.Errorf("view revision conflict: current=%d requested=%d", revision, batch.ViewRevision)
	}
	if schemaHash != batch.ViewSchemaHash {
		return errors.New("view schema hash conflict")
	}
	for _, write := range batch.RowWrites {
		id := viewindex.RowKeyID(write.Key.Key)
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT row_proto FROM view_rows WHERE row_id = ?`, id).Scan(&raw)
		var row *pb.RowFieldValues
		if err == nil {
			row = &pb.RowFieldValues{}
			if err := proto.Unmarshal(raw, row); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		row = viewindex.ApplyRowWrite(row, write, batch.WriteMode == viewindex.Backfill)
		raw, err = proto.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO view_rows (row_id, row_proto) VALUES (?, ?)
			ON CONFLICT(row_id) DO UPDATE SET row_proto = excluded.row_proto
		`, id, raw); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE view_meta SET updated_at = ? WHERE singleton = 1`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *IndexManager) Query(ctx context.Context, id string, keys []*pb.RowKey, fields []string) ([]*pb.RowFieldValues, error) {
	db, err := m.openExisting(id)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := make([]*pb.RowFieldValues, 0, len(keys))
	for _, key := range keys {
		var raw []byte
		err := db.QueryRowContext(ctx, `SELECT row_proto FROM view_rows WHERE row_id = ?`, viewindex.RowKeyID(key)).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		row := &pb.RowFieldValues{}
		if err := proto.Unmarshal(raw, row); err != nil {
			return nil, err
		}
		out = append(out, project(row, fields))
	}
	return out, nil
}

func (m *IndexManager) Scan(ctx context.Context, id string, offset, limit int) ([]*pb.RowFieldValues, error) {
	db, err := m.openExisting(id)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `SELECT row_proto FROM view_rows ORDER BY row_id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*pb.RowFieldValues
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		row := &pb.RowFieldValues{}
		if err := proto.Unmarshal(raw, row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (m *IndexManager) Write(ctx context.Context, id string, batch viewindex.BatchWrite) error {
	writes := make([]viewindex.RowWrite, 0, len(batch.TimeSeriesRows)+len(batch.RecordRows))
	for _, row := range batch.TimeSeriesRows {
		if row != nil {
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: timeSeriesKey(row.GetKey())}, Fields: row.GetFields()})
		}
	}
	for _, row := range batch.RecordRows {
		if row != nil {
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: recordKey(row.GetKey())}, Fields: row.GetFields()})
		}
	}
	return m.Apply(ctx, id, viewindex.ViewIndexApplyBatch{RowWrites: writes, ViewRevision: batch.ViewVersion, ViewSchemaHash: batch.SchemaHash, WriteMode: viewindex.LiveWrite})
}

func (m *IndexManager) Stat(ctx context.Context, id string) (viewindex.ViewIndexStats, error) {
	path, err := m.path(id)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return viewindex.ViewIndexStats{}, nil
	}
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	db, err := open(path)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	defer db.Close()
	var stats viewindex.ViewIndexStats
	if err := db.QueryRowContext(ctx, `SELECT view_version, schema_hash, updated_at FROM view_meta WHERE singleton = 1`).Scan(&stats.ViewVersion, &stats.SchemaHash, &stats.UpdatedAt); err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM view_rows`).Scan(&stats.EntryCount); err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	rows, err := db.QueryContext(ctx, `SELECT row_proto FROM view_rows`)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return viewindex.ViewIndexStats{}, err
		}
		row := &pb.RowFieldValues{}
		if err := proto.Unmarshal(raw, row); err != nil {
			_ = rows.Close()
			return viewindex.ViewIndexStats{}, err
		}
		updateRange(&stats, row.GetKey())
	}
	if err := rows.Close(); err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	stats.Exists = true
	stats.PhysicalBytes = uint64(info.Size())
	return stats, nil
}

func (m *IndexManager) Remove(_ context.Context, id string) error {
	path, err := m.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *IndexManager) List(context.Context) ([]string, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".duckdb") {
			ids = append(ids, strings.TrimSuffix(entry.Name(), ".duckdb"))
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (m *IndexManager) Close() error { return nil }

func (m *IndexManager) openExisting(id string) (*sql.DB, error) {
	path, err := m.path(id)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return open(path)
}

func (m *IndexManager) path(id string) (string, error) {
	if id == "" || filepath.Base(id) != id {
		return "", errors.New("invalid view index id")
	}
	return filepath.Join(m.root, id+".duckdb"), nil
}

func open(path string) (*sql.DB, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func project(row *pb.RowFieldValues, fields []string) *pb.RowFieldValues {
	if len(fields) == 0 {
		return proto.Clone(row).(*pb.RowFieldValues)
	}
	want := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		want[field] = struct{}{}
	}
	out := &pb.RowFieldValues{Key: proto.Clone(row.GetKey()).(*pb.RowKey)}
	for _, field := range row.GetFields() {
		if _, ok := want[field.GetFieldId()]; ok {
			out.Fields = append(out.Fields, proto.Clone(field).(*pb.FieldValue))
		}
	}
	for key, value := range row.GetAttributes() {
		if _, ok := want[key]; ok {
			if out.Attributes == nil {
				out.Attributes = make(map[string]*pb.TypedValue)
			}
			out.Attributes[key] = proto.Clone(value).(*pb.TypedValue)
		}
	}
	return out
}

func timeSeriesKey(key *pb.TimeSeriesKey) *pb.RowKey {
	if key == nil {
		return nil
	}
	return &pb.RowKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime()}}}
}

func recordKey(key *pb.RecordKey) *pb.RowKey {
	if key == nil {
		return nil
	}
	return &pb.RowKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: key.GetRecordId(), Version: key.GetVersion()}}}
}

func updateRange(stats *viewindex.ViewIndexStats, key *pb.RowKey) {
	if stats == nil || key == nil {
		return
	}
	value := ""
	if row := key.GetTimeSeries(); row != nil {
		value = row.GetDataTime()
	} else if row := key.GetRecord(); row != nil {
		value = row.GetVersion()
	}
	if value == "" {
		return
	}
	if stats.IndexedFrom == "" || value < stats.IndexedFrom {
		stats.IndexedFrom = value
	}
	if stats.IndexedTo == "" || value > stats.IndexedTo {
		stats.IndexedTo = value
	}
}

var _ viewindex.QueryEngine = (*IndexManager)(nil)
