//go:build cgo

package duckdb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
)

// Options 保存 DuckDB 视图存储打开配置。

func (s *ViewStore) Prepare(ctx context.Context, indexID string, schema viewindex.ViewIndexSchema) error {
	if err := s.DropResultTable(ctx, indexID); err != nil {
		return err
	}
	if err := s.CreateResultTable(ctx, indexID, schema.Columns); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE moox_view_index_meta SET view_version = ?, schema_hash = ?, indexed_from = '', indexed_to = '', checkpoints_json = '{}', updated_at = ? WHERE table_name = ?
	`, schema.ViewVersion, schema.SchemaHash, time.Now().UTC().Format(time.RFC3339Nano), indexID)
	return err
}

func (s *ViewStore) CreateResultTable(ctx context.Context, tableName string, columns []*pb.ViewColumn) error {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return err
	}
	columnDefs, err := resultColumnDefs(columns)
	if err != nil {
		return err
	}
	unlock := s.lockResultTable(tableName)
	defer unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	defs := []string{
		"row_key VARCHAR NOT NULL",
		"space_id VARCHAR NOT NULL",
		"dataset_id VARCHAR NOT NULL",
		"subject_id VARCHAR NOT NULL",
		"freq VARCHAR NOT NULL",
		"dimensions_json VARCHAR NOT NULL",
		"data_time VARCHAR NOT NULL",
		"attributes_json VARCHAR NOT NULL",
		"row_json VARCHAR NOT NULL",
	}
	for _, def := range columnDefs {
		quotedName, err := quoteColumnName(def.name)
		if err != nil {
			return err
		}
		defs = append(defs, quotedName+" "+duckDBType(def.valueType))
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			%s
		)
	`, quoted, strings.Join(defs, ",\n\t\t\t"))); err != nil {
		return err
	}
	if err := s.createResultIndexes(ctx, tableName, columnDefs); err != nil {
		return err
	}
	s.indexedTables.Store(tableName, struct{}{})
	encoded, err := encodeColumns(columns)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO moox_view_columns (table_name, columns_json)
		VALUES (?, ?)
		ON CONFLICT(table_name) DO UPDATE SET columns_json = excluded.columns_json
	`, tableName, encoded)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO moox_view_index_meta (table_name, updated_at)
		VALUES (?, ?)
		ON CONFLICT(table_name) DO NOTHING
	`, tableName, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func resultColumnDefs(columns []*pb.ViewColumn) ([]resultColumnDef, error) {
	seen := make(map[string]bool, len(columns))
	out := make([]resultColumnDef, 0, len(columns))
	for _, column := range columns {
		name := strings.TrimSpace(column.GetColumnName())
		if name == "" {
			return nil, errors.New("view column_name is required")
		}
		if _, err := quoteColumnName(name); err != nil {
			return nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, resultColumnDef{name: name, valueType: column.GetValueType()})
	}
	return out, nil
}

func resultColumnDefsFromResultColumns(columns []*pb.ResultColumn) ([]resultColumnDef, error) {
	seen := make(map[string]bool, len(columns))
	out := make([]resultColumnDef, 0, len(columns))
	for _, column := range columns {
		name := strings.TrimSpace(column.GetColumnName())
		if name == "" {
			return nil, errors.New("view column_name is required")
		}
		if _, err := quoteColumnName(name); err != nil {
			return nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, resultColumnDef{name: name, valueType: column.GetValueType()})
	}
	return out, nil
}

func (s *ViewStore) createResultIndexes(ctx context.Context, tableName string, columns []resultColumnDef) error {
	statements, err := createResultIndexStatements(tableName, columns)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *ViewStore) ensureResultIndexes(ctx context.Context, tableName string, columns []*pb.ResultColumn) error {
	if _, ok := s.indexedTables.Load(tableName); ok {
		return nil
	}
	columnDefs, err := resultColumnDefsFromResultColumns(columns)
	if err != nil {
		return err
	}
	unlock := s.lockResultTable(tableName)
	defer unlock()
	if _, ok := s.indexedTables.Load(tableName); ok {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.createResultIndexes(ctx, tableName, columnDefs); err != nil {
		return err
	}
	s.indexedTables.Store(tableName, struct{}{})
	return nil
}

func createResultIndexStatements(tableName string, columns []resultColumnDef) ([]string, error) {
	quotedTable, err := quoteTableName(tableName)
	if err != nil {
		return nil, err
	}
	keyTimeIndex, err := quoteIndexName(tableName, "key_time")
	if err != nil {
		return nil, err
	}
	subjectFreqIndex, err := quoteIndexName(tableName, "subject_freq_time")
	if err != nil {
		return nil, err
	}
	dataTimeIndex, err := quoteIndexName(tableName, "data_time")
	if err != nil {
		return nil, err
	}
	statements := []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (row_key, data_time)`, keyTimeIndex, quotedTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (subject_id, freq, data_time)`, subjectFreqIndex, quotedTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (data_time)`, dataTimeIndex, quotedTable),
	}
	for _, column := range columns {
		indexName, err := quoteIndexName(tableName, column.name)
		if err != nil {
			return nil, err
		}
		columnName, err := quoteColumnName(column.name)
		if err != nil {
			return nil, err
		}
		statements = append(statements, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (%s)`,
			indexName,
			quotedTable,
			columnName,
		))
	}
	return statements, nil
}

func dropResultIndexStatements(tableName string, columns []resultColumnDef) ([]string, error) {
	keyTimeIndex, err := quoteIndexName(tableName, "key_time")
	if err != nil {
		return nil, err
	}
	subjectFreqIndex, err := quoteIndexName(tableName, "subject_freq_time")
	if err != nil {
		return nil, err
	}
	dataTimeIndex, err := quoteIndexName(tableName, "data_time")
	if err != nil {
		return nil, err
	}
	indexNames := []string{keyTimeIndex, subjectFreqIndex, dataTimeIndex}
	for _, column := range columns {
		indexName, err := quoteIndexName(tableName, column.name)
		if err != nil {
			return nil, err
		}
		indexNames = append(indexNames, indexName)
	}
	statements := make([]string, 0, len(indexNames))
	for _, indexName := range indexNames {
		statements = append(statements, fmt.Sprintf(`DROP INDEX IF EXISTS %s`, indexName))
	}
	return statements, nil
}

func (s *ViewStore) loadColumns(ctx context.Context, tableName string) ([]*pb.ResultColumn, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT columns_json FROM moox_view_columns WHERE table_name = ?`, tableName).Scan(&raw); err != nil {
		return nil, err
	}
	rsp := &pb.QueryTimeSeriesRowsRsp{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(raw), rsp); err != nil {
		return nil, err
	}
	return rsp.GetColumns(), nil
}

func encodeColumns(columns []*pb.ViewColumn) (string, error) {
	out := make([]*pb.ResultColumn, 0, len(columns))
	for _, column := range columns {
		out = append(out, &pb.ResultColumn{
			ColumnName: column.GetColumnName(),
			OriginType: column.GetOriginType(),
			OriginId:   column.GetOriginId(),
			ValueType:  column.GetValueType(),
		})
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(&pb.QueryTimeSeriesRowsRsp{Columns: out})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func quoteTableName(tableName string) (string, error) {
	if !tableNamePattern.MatchString(tableName) {
		return "", fmt.Errorf("invalid duckdb table name %s", tableName)
	}
	return `"` + tableName + `"`, nil
}

func unquoteTableName(quotedTableName string) (string, error) {
	tableName := strings.Trim(quotedTableName, `"`)
	if !tableNamePattern.MatchString(tableName) {
		return "", fmt.Errorf("invalid duckdb table name %s", quotedTableName)
	}
	return tableName, nil
}

func quoteColumnName(columnName string) (string, error) {
	columnName = strings.TrimSpace(columnName)
	if columnName == "" || strings.Contains(columnName, `"`) || strings.ContainsAny(columnName, "\x00\r\n\t") {
		return "", fmt.Errorf("invalid duckdb column name %s", columnName)
	}
	return `"` + columnName + `"`, nil
}

func quoteIndexName(tableName string, suffix string) (string, error) {
	name := "idx_" + tableName + "_" + safeIndexNamePart(suffix)
	if !tableNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid duckdb index name %s", name)
	}
	return `"` + name + `"`, nil
}

func safeIndexNamePart(value string) string {
	value = unsafeIndexNameChar.ReplaceAllString(strings.TrimSpace(value), "_")
	value = strings.Trim(value, "_")
	if value == "" {
		return "column"
	}
	if first := value[0]; (first < 'A' || first > 'Z') && (first < 'a' || first > 'z') && first != '_' {
		value = "_" + value
	}
	return value
}

func duckDBType(valueType pb.FieldValueType) string {
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return "BIGINT"
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return "DOUBLE"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return "BOOLEAN"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return "BLOB"
	case pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		pb.FieldValueType_FIELD_VALUE_TYPE_TIME,
		pb.FieldValueType_FIELD_VALUE_TYPE_JSON,
		pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED:
		return "VARCHAR"
	default:
		return "VARCHAR"
	}
}
