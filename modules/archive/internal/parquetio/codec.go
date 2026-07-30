package parquetio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

type WriteOptions struct {
	Generation     uint64
	MaterializedAt time.Time
	RowGroupRows   int64
	Columns        map[string]storagepb.FieldValueType
}

func Write(path string, rows []domain.ArchiveRow, opts WriteOptions) (domain.Manifest, error) {
	if len(rows) == 0 {
		return domain.Manifest{}, fmt.Errorf("cannot write empty parquet partition")
	}
	columns := make(map[string]storagepb.FieldValueType, len(opts.Columns))
	for name, kind := range opts.Columns {
		columns[name] = kind
	}
	partition := rows[0].Partition
	if err := partition.Validate(); err != nil {
		return domain.Manifest{}, err
	}
	for _, row := range rows {
		if row.Partition != partition || domain.MonthOf(row.DataTime) != partition.Month {
			return domain.Manifest{}, fmt.Errorf("row partition identity mismatch")
		}
		for name, value := range row.Columns {
			if old, ok := columns[name]; ok && old != value.Type {
				return domain.Manifest{}, fmt.Errorf("schema conflict for %s", name)
			}
			columns[name] = value.Type
		}
	}
	schema, err := BuildSchema(columns)
	if err != nil {
		return domain.Manifest{}, err
	}
	if err := os.MkdirAll(filepathDir(path), 0o755); err != nil {
		return domain.Manifest{}, err
	}
	file, err := os.Create(path)
	if err != nil {
		return domain.Manifest{}, err
	}
	writer := parquet.NewGenericWriter[map[string]any](file, schema, parquet.Compression(&zstd.Codec{}), parquet.MaxRowsPerRowGroup(opts.RowGroupRows))
	writer.SetKeyValueMetadata("moox.archive.schema_version", "2")
	writer.SetKeyValueMetadata("moox.archive.generation", fmt.Sprint(opts.Generation))
	writer.SetKeyValueMetadata("moox.archive.materialized_at", opts.MaterializedAt.UTC().Format(time.RFC3339Nano))
	writer.SetKeyValueMetadata("moox.archive.space_id", partition.SpaceID)
	writer.SetKeyValueMetadata("moox.archive.dataset_id", partition.DatasetID)
	writer.SetKeyValueMetadata("moox.archive.subject_id", partition.SubjectID)
	writer.SetKeyValueMetadata("moox.archive.freq", partition.Freq)
	writer.SetKeyValueMetadata("moox.archive.series_tag", partition.SeriesTag)
	writer.SetKeyValueMetadata("moox.archive.month", partition.Month)
	typesRaw, _ := json.Marshal(columns)
	writer.SetKeyValueMetadata("moox.archive.column_types", string(typesRaw))
	maps := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		maps = append(maps, rowToMap(row))
	}
	if _, err := writer.Write(maps); err != nil {
		_ = writer.Close()
		_ = file.Close()
		return domain.Manifest{}, err
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		return domain.Manifest{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return domain.Manifest{}, err
	}
	if err := file.Close(); err != nil {
		return domain.Manifest{}, err
	}
	return manifestFor(path, rows, columns, opts), nil
}

func Read(path string) ([]domain.ArchiveRow, map[string]storagepb.FieldValueType, map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, nil, err
	}
	parquetFile, err := parquet.OpenFile(file, info.Size())
	if err != nil {
		return nil, nil, nil, err
	}
	metadata := map[string]string{}
	for _, key := range []string{"moox.archive.schema_version", "moox.archive.generation", "moox.archive.materialized_at", "moox.archive.column_types", "moox.archive.space_id", "moox.archive.dataset_id", "moox.archive.subject_id", "moox.archive.freq", "moox.archive.series_tag", "moox.archive.month"} {
		if value, ok := parquetFile.Lookup(key); ok {
			metadata[key] = value
		}
	}
	if metadata["moox.archive.schema_version"] != "2" {
		return nil, nil, nil, fmt.Errorf("unsupported archive parquet schema version %q; use a new v2 archive directory", metadata["moox.archive.schema_version"])
	}
	if !hasRequiredSchemaField(parquetFile.Schema(), colSeriesTag) || hasSchemaField(parquetFile.Schema(), "dimensions_json") {
		return nil, nil, nil, fmt.Errorf("invalid archive parquet v2 system columns")
	}
	values, err := parquet.ReadFile[any](path)
	if err != nil && err != io.EOF {
		return nil, nil, nil, err
	}
	columns := schemaColumns(parquetFile.Schema())
	if rawTypes := metadata["moox.archive.column_types"]; rawTypes != "" {
		var encoded map[string]storagepb.FieldValueType
		if json.Unmarshal([]byte(rawTypes), &encoded) == nil {
			for name, kind := range encoded {
				columns[name] = kind
			}
		}
	}
	rows := make([]domain.ArchiveRow, 0, len(values))
	for _, rawRow := range values {
		row, ok := rawRow.(map[string]any)
		if !ok {
			return nil, nil, nil, fmt.Errorf("unexpected parquet row type %T", rawRow)
		}
		decoded, err := mapToRow(row, columns)
		if err != nil {
			return nil, nil, nil, err
		}
		rows = append(rows, decoded)
	}
	return rows, columns, metadata, nil
}

func Validate(path string, expected domain.PartitionKey, generation uint64) (domain.Manifest, error) {
	rows, columns, metadata, err := Read(path)
	if err != nil {
		return domain.Manifest{}, err
	}
	if metadata["moox.archive.generation"] != fmt.Sprint(generation) {
		return domain.Manifest{}, fmt.Errorf("generation metadata mismatch")
	}
	if metadata["moox.archive.schema_version"] != "2" {
		return domain.Manifest{}, fmt.Errorf("archive parquet schema is not v2")
	}
	if len(rows) == 0 {
		return domain.Manifest{}, fmt.Errorf("archive parquet partition is empty")
	}
	identity := map[string]string{
		"moox.archive.space_id":   expected.SpaceID,
		"moox.archive.dataset_id": expected.DatasetID,
		"moox.archive.subject_id": expected.SubjectID,
		"moox.archive.freq":       expected.Freq,
		"moox.archive.series_tag": expected.SeriesTag,
		"moox.archive.month":      expected.Month,
	}
	for name, want := range identity {
		if metadata[name] != want {
			return domain.Manifest{}, fmt.Errorf("%s metadata mismatch", name)
		}
	}
	seen := make(map[string]struct{}, len(rows))
	for i, row := range rows {
		if row.Partition != expected {
			return domain.Manifest{}, fmt.Errorf("partition identity mismatch")
		}
		if row.Partition.SeriesTag != expected.SeriesTag {
			return domain.Manifest{}, fmt.Errorf("series_tag is not constant")
		}
		id := domain.LogicalRowID(row.DataTime)
		if _, exists := seen[id]; exists {
			return domain.Manifest{}, fmt.Errorf("duplicate logical row %s", id)
		}
		seen[id] = struct{}{}
		if i > 0 {
			prev := rows[i-1]
			if !row.DataTime.After(prev.DataTime) {
				return domain.Manifest{}, fmt.Errorf("rows are not sorted")
			}
		}
	}
	materializedAt, err := time.Parse(time.RFC3339Nano, metadata["moox.archive.materialized_at"])
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("invalid materialized_at metadata: %w", err)
	}
	manifest := manifestFor(path, rows, columns, WriteOptions{Generation: generation, MaterializedAt: materializedAt})
	return manifest, nil
}

func rowToMap(row domain.ArchiveRow) map[string]any {
	attributes, _ := domain.CanonicalStringMap(row.Attributes)
	out := map[string]any{colCandleTime: row.DataTime.UTC(), colSpace: row.Partition.SpaceID, colDataset: row.Partition.DatasetID, colSubject: row.Partition.SubjectID, colFreq: row.Partition.Freq, colSeriesTag: row.Partition.SeriesTag, colAttributes: attributes, colWrittenAt: row.WrittenAt.UTC()}
	for name, value := range row.Columns {
		switch value.Type {
		case storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING:
			if value.String != nil {
				out[name] = *value.String
			}
		case storagepb.FieldValueType_FIELD_VALUE_TYPE_INT:
			if value.Int != nil {
				out[name] = *value.Int
			}
		case storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
			if value.Double != nil {
				out[name] = *value.Double
			}
		case storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
			if value.Bool != nil {
				out[name] = *value.Bool
			}
		case storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME:
			if value.Time != nil {
				t, _ := time.Parse(time.RFC3339Nano, *value.Time)
				out[name] = t
			}
		case storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON:
			if value.JSON != nil {
				out[name] = *value.JSON
			}
		case storagepb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
			if value.Bytes != nil {
				out[name] = *value.Bytes
			}
		}
	}
	return out
}

func mapToRow(value map[string]any, columns map[string]storagepb.FieldValueType) (domain.ArchiveRow, error) {
	dataTime, ok := parquetTime(value[colCandleTime])
	if !ok {
		return domain.ArchiveRow{}, fmt.Errorf("missing candle_begin_time")
	}
	writtenAt, ok := parquetTime(value[colWrittenAt])
	if !ok {
		return domain.ArchiveRow{}, fmt.Errorf("missing written_at")
	}
	attributes := map[string]string{}
	if raw, ok := value[colAttributes].(string); ok {
		if err := json.Unmarshal([]byte(raw), &attributes); err != nil {
			return domain.ArchiveRow{}, err
		}
	}
	seriesTag, exists := value[colSeriesTag]
	if !exists || seriesTag == nil {
		return domain.ArchiveRow{}, fmt.Errorf("series_tag is required")
	}
	seriesTagString, ok := seriesTag.(string)
	if !ok {
		return domain.ArchiveRow{}, fmt.Errorf("series_tag must be a string")
	}
	row := domain.ArchiveRow{Partition: domain.PartitionKey{SpaceID: asString(value[colSpace]), DatasetID: asString(value[colDataset]), SubjectID: asString(value[colSubject]), Freq: asString(value[colFreq]), SeriesTag: seriesTagString, Month: domain.MonthOf(dataTime)}, DataTime: dataTime.UTC(), Attributes: attributes, WrittenAt: writtenAt.UTC(), Columns: map[string]domain.Scalar{}}
	for name, kind := range columns {
		raw, exists := value[name]
		if !exists || raw == nil {
			continue
		}
		scalar, err := anyToScalar(kind, raw)
		if err != nil {
			return domain.ArchiveRow{}, fmt.Errorf("column %s: %w", name, err)
		}
		row.Columns[name] = scalar
	}
	return row, nil
}

func anyToScalar(kind storagepb.FieldValueType, raw any) (domain.Scalar, error) {
	s := domain.Scalar{Type: kind}
	switch kind {
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		if v, ok := raw.(*string); ok {
			s.String = v
			if kind == storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON {
				s.JSON = v
			}
			return s, nil
		}
		if v, ok := raw.(string); ok {
			s.String = &v
			if kind == storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON {
				s.JSON = &v
			}
			return s, nil
		}
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_INT:
		if v, ok := raw.(*int64); ok {
			s.Int = v
			return s, nil
		}
		if v, ok := raw.(int64); ok {
			s.Int = &v
			return s, nil
		}
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		if v, ok := raw.(*float64); ok {
			s.Double = v
			return s, nil
		}
		if v, ok := raw.(float64); ok {
			s.Double = &v
			return s, nil
		}
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		if v, ok := raw.(*bool); ok {
			s.Bool = v
			return s, nil
		}
		if v, ok := raw.(bool); ok {
			s.Bool = &v
			return s, nil
		}
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		if v, ok := parquetTime(raw); ok {
			x := v.UTC().Format(time.RFC3339Nano)
			s.Time = &x
			return s, nil
		}
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		if v, ok := raw.([]byte); ok {
			s.Bytes = &v
			return s, nil
		}
		if v, ok := raw.(*[]byte); ok {
			s.Bytes = v
			return s, nil
		}
	}
	return domain.Scalar{}, fmt.Errorf("cannot decode parquet value %T", raw)
}

func schemaColumns(schema *parquet.Schema) map[string]storagepb.FieldValueType {
	out := map[string]storagepb.FieldValueType{}
	for _, field := range schema.Fields() {
		switch field.Name() {
		case colCandleTime, colSpace, colDataset, colSubject, colFreq, colSeriesTag, colAttributes, colWrittenAt:
			continue
		}
		kind := field.Type().Kind()
		logical := field.Type().LogicalType()
		switch {
		case logical != nil && logical.Timestamp != nil:
			out[field.Name()] = storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
		case kind == parquet.Boolean:
			out[field.Name()] = storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL
		case kind == parquet.Int64:
			out[field.Name()] = storagepb.FieldValueType_FIELD_VALUE_TYPE_INT
		case kind == parquet.Double:
			out[field.Name()] = storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
		case kind == parquet.ByteArray:
			out[field.Name()] = storagepb.FieldValueType_FIELD_VALUE_TYPE_BYTES
		default:
			out[field.Name()] = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
		}
	}
	return out
}

func hasSchemaField(schema *parquet.Schema, name string) bool {
	for _, field := range schema.Fields() {
		if field.Name() == name {
			return true
		}
	}
	return false
}

func hasRequiredSchemaField(schema *parquet.Schema, name string) bool {
	for _, field := range schema.Fields() {
		if field.Name() == name {
			return !field.Optional() && !field.Repeated()
		}
	}
	return false
}

func manifestFor(path string, rows []domain.ArchiveRow, columns map[string]storagepb.FieldValueType, opts WriteOptions) domain.Manifest {
	data, _ := os.ReadFile(path)
	sum := sha256.Sum256(data)
	if columns == nil {
		columns = map[string]storagepb.FieldValueType{}
		for _, row := range rows {
			for name, value := range row.Columns {
				columns[name] = value.Type
			}
		}
	}
	names := domain.SortedColumnNames(columns)
	min, max := rows[0].DataTime, rows[0].DataTime
	for _, row := range rows[1:] {
		if row.DataTime.Before(min) {
			min = row.DataTime
		}
		if row.DataTime.After(max) {
			max = row.DataTime
		}
	}
	return domain.Manifest{Path: path, Generation: opts.Generation, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), RowCount: uint64(len(rows)), MinTime: min, MaxTime: max, Columns: names, MaterializedAt: opts.MaterializedAt}
}
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if p, ok := v.(*string); ok && p != nil {
		return *p
	}
	return ""
}

func parquetTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case time.Time:
		return v, true
	case *time.Time:
		if v != nil {
			return *v, true
		}
	case int64:
		return time.Unix(0, v).UTC(), true
	case *int64:
		if v != nil {
			return time.Unix(0, *v).UTC(), true
		}
	}
	return time.Time{}, false
}
func filepathDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			if i == 0 {
				return path[:1]
			}
			return path[:i]
		}
	}
	return "."
}
