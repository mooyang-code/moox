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
	"google.golang.org/protobuf/reflect/protoreflect"
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。
type rowScanner interface {
	Scan(dest ...any) error
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type execQueryRower interface {
	queryRower
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

var (
	marshalOptions   = protojson.MarshalOptions{UseProtoNames: true}
	unmarshalOptions = protojson.UnmarshalOptions{DiscardUnknown: true}
)

func getMessage[T proto.Message](ctx context.Context, db queryRower, query string, args []any, newMessage func() T) (T, error) {
	row := db.QueryRowContext(ctx, query, args...)
	return scanMessage(row, newMessage)
}

func queryMessages[T proto.Message](ctx context.Context, db queryer, query string, args []any, newMessage func() T) ([]T, error) {
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

type readDB interface {
	queryRower
	queryer
}

func queryPagedMessages[T proto.Message](ctx context.Context, db readDB, query string, countQuery string, args []any, page *pb.Page, newMessage func() T) ([]T, *pb.PageResult, error) {
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

func countRows(ctx context.Context, db queryRower, query string, args []any) (uint32, error) {
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

func scanMessageWithSQLTimestamps[T proto.Message](row rowScanner, newMessage func() T) (T, error) {
	var raw, createdAt, updatedAt string
	if err := row.Scan(&raw, &createdAt, &updatedAt); err != nil {
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
	if err := setTimestampField(msg, "created_at", createdAt); err != nil {
		var zero T
		return zero, err
	}
	if err := setTimestampField(msg, "updated_at", updatedAt); err != nil {
		var zero T
		return zero, err
	}
	return msg, nil
}

// setTimestampField 把库里的时间文本按字段类型回填到消息：int64 字段写 Unix 毫秒，string 字段写 RFC3339Nano（UTC）。
// 文本为空时不回填；字段不存在、类型既不是 int64 也不是 string、文本解析不了都报错，不静默跳过。
func setTimestampField(msg proto.Message, name, text string) error {
	field := msg.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(name))
	if field == nil {
		return fmt.Errorf("元数据消息 %s 没有时间字段 %s", msg.ProtoReflect().Descriptor().FullName(), name)
	}
	ms, err := sqlTimestampMillis(text)
	if err != nil {
		return fmt.Errorf("元数据字段 %s：%w", name, err)
	}
	if ms == 0 {
		return nil
	}
	switch field.Kind() {
	case protoreflect.Int64Kind:
		msg.ProtoReflect().Set(field, protoreflect.ValueOfInt64(ms))
	case protoreflect.StringKind:
		msg.ProtoReflect().Set(field, protoreflect.ValueOfString(time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)))
	default:
		return fmt.Errorf("元数据字段 %s 的类型 %s 不能回填时间", name, field.Kind())
	}
	return nil
}

// sqlTimestampMillis 把 SQLite 时间列的文本（RFC3339Nano，或 CURRENT_TIMESTAMP 的“年-月-日 时:分:秒”）换成 Unix 毫秒；空文本得 0。
func sqlTimestampMillis(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("无法解析时间文本 %q", value)
}

// sqlTimestampText 是写进 SQLite 时间列的文本；与 sqlTimestampMillis 配对，读回得到同一个毫秒数。
func sqlTimestampText(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
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

func defaultStatus(status string) string {
	if status == "" {
		return "active"
	}
	return status
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
	default:
		return "record"
	}
}
