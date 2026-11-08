package staging

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// OpenReadOnly 用一条只读连接（mode=ro）打开已有的暂存库，供数据端口查询：每次请求单独打开、用完立刻关闭，
// 不占用 worker 的写连接，也不挡每天维护的 wal_checkpoint(TRUNCATE)。库文件不存在时报错，不会创建。
// 返回的 Store 只能调用读方法。
func OpenReadOnly(path string) (*Store, error) {
	query := url.Values{}
	query.Set("mode", "ro")
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + query.Encode() + "&_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("只读打开暂存库: %w", err)
	}
	return &Store{db: db, path: path}, nil
}

// RecentColumn 是最新数据里的一列：系统列 data_time、subject_id、series_tag 在前，值列按 dataset.json 的顺序在后。
type RecentColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// RecentRows 是暂存表里某个标的最近的行。单元格按归档 CSV 的规则格式化成字符串（时间为 UTC RFC3339、布尔 true/false、
// 浮点数最短往返表示、二进制 base64），缺失值为 nil，空字符串是 ""，二者可以区分。
type RecentRows struct {
	Columns []RecentColumn
	Rows    [][]*string
}

// Recent 按 data_time 倒序返回 subject 在暂存表里最近的 limit 行；before 非零时只取 data_time 早于它的行（翻页）。
// task.Columns 取自 dataset.json；暂存表里还没有的列返回缺失值，暂存表不存在时返回空行。
func (s *Store) Recent(ctx context.Context, task Task, subject string, before time.Time, limit int) (RecentRows, error) {
	out := RecentRows{Columns: []RecentColumn{{Name: "data_time", Type: TypeTime}, {Name: "subject_id", Type: TypeString}, {Name: "series_tag", Type: TypeString}}}
	for _, c := range task.Columns {
		out.Columns = append(out.Columns, RecentColumn{Name: c.Name, Type: c.Type})
	}
	out.Rows = [][]*string{}
	if err := task.Validate(); err != nil {
		return out, err
	}
	if limit <= 0 {
		return out, errors.New("limit 必须为正")
	}
	have, err := s.tableColumns(ctx, task.Table())
	if err != nil {
		return out, err
	}
	if len(have) == 0 {
		return out, nil
	}
	selects := []string{"data_time", "series_tag"}
	for _, c := range task.Columns {
		if have[c.Name] {
			selects = append(selects, quote(c.Name))
		} else {
			selects = append(selects, "NULL")
		}
	}
	where, args := "subject_id = ?", []any{subject}
	if !before.IsZero() {
		if err := CheckNanosRange(before); err != nil {
			return out, err
		}
		where += " AND data_time < ?"
		args = append(args, nanos(before))
	}
	args = append(args, limit)
	// 主键 (subject_id, data_time, series_tag) 的前缀：按标的定位后倒序扫描，不用全表排序。
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s ORDER BY data_time DESC, series_tag LIMIT ?`,
		strings.Join(selects, ", "), quote(task.Table()), where), args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var at int64
	var tag string
	values := make([]any, len(task.Columns))
	targets := append([]any{&at, &tag}, make([]any, len(task.Columns))...)
	for i := range values {
		targets[2+i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return out, err
		}
		row := make([]*string, 0, len(out.Columns))
		row = append(row, cell(formatTime(fromNanos(at))), cell(subject), cell(tag))
		for i, c := range task.Columns {
			field, err := formatValue(c.Type, values[i])
			if err != nil {
				return out, err
			}
			if field.null {
				row = append(row, nil)
			} else {
				row = append(row, cell(field.value))
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return out, rows.Err()
}

func cell(value string) *string { return &value }
