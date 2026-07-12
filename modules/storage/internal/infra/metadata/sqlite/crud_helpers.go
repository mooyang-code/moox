package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。
type rowScanner interface {
	Scan(dest ...any) error
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type execQueryRower interface {
	queryRower
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

var (
	marshalOptions   = protojson.MarshalOptions{UseProtoNames: true}
	unmarshalOptions = protojson.UnmarshalOptions{DiscardUnknown: true}
)

var ErrViewIndexBuildConflict = errors.New("view index build conflict")

const sqliteBuildTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

const viewIndexBuildColumns = `
	c_space_id, c_view_id, c_build_id, c_index_id, c_engine,
	c_target_view_version, c_state, c_owner_id, c_lease_expires_at,
	c_cursor_json, c_snapshot_end, c_coverage_start, c_coverage_end,
	c_entries_written, c_schema_hash, c_columns_json, c_started_at,
	c_updated_at, c_finished_at, c_error`

func buildLeaseTTL(seconds uint32) time.Duration {
	if seconds == 0 {
		seconds = 90
	}
	return time.Duration(seconds) * time.Second
}

func requireChangedRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrViewIndexBuildConflict
	}
	return nil
}

func getMessage[T proto.Message](ctx context.Context, db queryRower, query string, args []any, newMessage func() T) (T, error) {
	row := db.QueryRowContext(ctx, query, args...)
	return scanMessage(row, newMessage)
}

func queryMessages[T proto.Message](ctx context.Context, db *sql.DB, query string, args []any, newMessage func() T) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []T
	for rows.Next() {
		item, err := scanMessage(rows, newMessage)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func queryPagedMessages[T proto.Message](ctx context.Context, db *sql.DB, query string, countQuery string, args []any, page *pb.Page, newMessage func() T) ([]T, *pb.PageResult, error) {
	pageNo, size, offset := normalizePage(page)
	limit := int(size)
	pagedArgs := append([]any{}, args...)
	pagedArgs = append(pagedArgs, limit, offset)
	items, err := queryMessages(ctx, db, query+` LIMIT ? OFFSET ?`, pagedArgs, newMessage)
	if err != nil {
		return nil, nil, err
	}
	total, err := countRows(ctx, db, countQuery, args)
	if err != nil {
		return nil, nil, err
	}
	hasMore := uint64(offset)+uint64(len(items)) < uint64(total)
	return items, &pb.PageResult{Page: pageNo, Size: size, Total: total, HasMore: hasMore}, nil
}

func countRows(ctx context.Context, db *sql.DB, query string, args []any) (uint32, error) {
	var total int64
	if err := db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, err
	}
	if total < 0 {
		return 0, nil
	}
	if total > int64(^uint32(0)) {
		return ^uint32(0), nil
	}
	return uint32(total), nil
}

func normalizePage(page *pb.Page) (pageNo uint32, size uint32, offset int) {
	pageNo = 1
	size = 1000
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	offset64 := uint64(pageNo-1) * uint64(size)
	maxInt := int(^uint(0) >> 1)
	if offset64 > uint64(maxInt) {
		return pageNo, size, maxInt
	}
	return pageNo, size, int(offset64)
}

func scanMessage[T proto.Message](row rowScanner, newMessage func() T) (T, error) {
	var raw string
	if err := row.Scan(&raw); err != nil {
		var zero T
		if errors.Is(err, sql.ErrNoRows) {
			return zero, fmt.Errorf("metadata row not found: %w", err)
		}
		return zero, err
	}
	msg := newMessage()
	if err := unmarshalOptions.Unmarshal([]byte(raw), msg); err != nil {
		var zero T
		return zero, err
	}
	return msg, nil
}

func marshal(msg proto.Message) (string, error) {
	data, err := marshalOptions.Marshal(msg)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func marshalJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func pageItems[T any](items []T, page *pb.Page) ([]T, *pb.PageResult, error) {
	pageNo := uint32(1)
	size := uint32(1000)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	start := int((pageNo - 1) * size)
	if start > len(items) {
		start = len(items)
	}
	end := start + int(size)
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(items)), HasMore: end < len(items)}, nil
}

func defaultStatus(status string) string {
	if status == "" {
		return "active"
	}
	return status
}

func defaultJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func dataKindFilter(kind pb.DataKind) string {
	if kind == pb.DataKind_DATA_KIND_UNSPECIFIED {
		return ""
	}
	return dataKindSQL(kind)
}

func dataKindSQL(kind pb.DataKind) string {
	switch kind {
	case pb.DataKind_DATA_KIND_TIME_SERIES:
		return "time_series"
	case pb.DataKind_DATA_KIND_SNAPSHOT:
		return "snapshot"
	case pb.DataKind_DATA_KIND_EVENT:
		return "event"
	case pb.DataKind_DATA_KIND_DOCUMENT:
		return "document"
	case pb.DataKind_DATA_KIND_TABLE:
		return "table"
	default:
		return "record"
	}
}

func valueTypeFilter(valueType pb.FieldValueType) string {
	if valueType == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
		return ""
	}
	return valueTypeSQL(valueType)
}

func valueTypeSQL(valueType pb.FieldValueType) string {
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return "int"
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return "double"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return "bool"
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		return "time"
	case pb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return "json"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return "bytes"
	default:
		return "string"
	}
}

func datasetOriginSQL(origin pb.DatasetColumnOriginType) string {
	switch origin {
	case pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR:
		return "factor"
	case pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_SYSTEM:
		return "system"
	default:
		return "field"
	}
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func mapsEqual(left map[string]string, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if right[key] != leftValue {
			return false
		}
	}
	return true
}
