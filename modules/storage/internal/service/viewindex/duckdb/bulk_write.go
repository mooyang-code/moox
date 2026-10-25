//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// maxBulkWriteRows 是一条批量语句最多带的行数，只为限制一次绑定的内存；批量写入的开销主要在每条语句的固定成本，
// 行数几乎不影响单行成本，所以这个上限取得很大。
const maxBulkWriteRows = 20000

// keyColumnTypes 是 view_rows 前四个物理键列的类型。
var keyColumnTypes = []string{"VARCHAR", "VARCHAR", "TIMESTAMP_NS", "VARCHAR"}

// writeRowGroups 把一批已按键合并的行写进 view_rows 并维护 view_series_counts。
// 每组（携带同一组字段的行）只执行两条语句：先对不存在的键按序列累加计数，再整批 UPSERT。
// 每个列绑定成一个 LIST 参数并用 unnest 展开，不再按 32 行拆成多条 VALUES 语句——后者在线上历史导入时
// 每行要经过两轮 SQL，View 约 1100 行/秒（见 docs/计划/初始数据导入执行计划.md 第 6 节）。
// 返回计数阶段与写行阶段各自的耗时，供慢日志使用。
func writeRowGroups(ctx context.Context, tx *sql.Tx, columns map[string]pb.FieldValueType, writes []viewindex.RowWrite, mode viewindex.WriteMode) (seriesTook, upsertTook time.Duration, err error) {
	for _, group := range groupRowWrites(writes) {
		names := append([]string{"subject_id", "freq", "data_time", "series_tag"}, group.columns...)
		names = append(names, internalAttributesColumn)
		listTypes := make([]string, len(names))
		bytesColumns := make([]bool, len(names))
		for i, name := range names {
			switch {
			case i < len(keyColumnTypes):
				listTypes[i] = keyColumnTypes[i]
			case name == internalAttributesColumn:
				listTypes[i] = "JSON"
			case columns[name] == pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
				// 驱动不支持绑定 BLOB 的 LIST：以十六进制文本传入，SQL 里用 from_hex 还原。
				listTypes[i], bytesColumns[i] = "VARCHAR", true
			default:
				listTypes[i] = duckType(columns[name])
			}
			listTypes[i] += "[]"
		}
		for start := 0; start < len(group.writes); start += maxBulkWriteRows {
			chunk := group.writes[start:min(start+maxBulkWriteRows, len(group.writes))]
			lists := make([][]any, len(names))
			for i := range lists {
				lists[i] = make([]any, 0, len(chunk))
			}
			for _, write := range chunk {
				values, err := rowArgs(columns, names, write, mode)
				if err != nil {
					return seriesTook, upsertTook, err
				}
				for i, value := range values {
					if raw, ok := value.([]byte); ok {
						value = hex.EncodeToString(raw)
					}
					lists[i] = append(lists[i], value)
				}
			}
			// 驱动不接受 LIST 里的 NULL 元素（按元素类型断言会 panic）：含 NULL 的列把 NULL 换成该类型的占位值，
			// 另绑一个布尔 LIST 作为空值掩码，由 SQL 在展开后还原成 NULL。
			args := make([]any, 0, len(names)+2)
			masked := make([]bool, len(names))
			for i := range names {
				var mask []any
				for row, value := range lists[i] {
					if value != nil {
						continue
					}
					if mask == nil {
						mask = make([]any, len(lists[i]))
						for k := range mask {
							mask[k] = false
						}
					}
					lists[i][row], mask[row] = nullPlaceholder(listTypes[i]), true
				}
				args = append(args, lists[i])
				if mask != nil {
					masked[i] = true
					args = append(args, mask)
				}
			}
			began := time.Now()
			if err := countNewSeriesRows(ctx, tx, args[:len(keyColumnTypes)]); err != nil {
				return seriesTook, upsertTook, fmt.Errorf("count new view rows: %w", err)
			}
			seriesTook += time.Since(began)
			began = time.Now()
			if _, err := tx.ExecContext(ctx, bulkUpsertSQL(names, listTypes, masked, bytesColumns, mode), args...); err != nil {
				return seriesTook, upsertTook, err
			}
			upsertTook += time.Since(began)
		}
	}
	return seriesTook, upsertTook, nil
}

// countNewSeriesRows 对 view_rows 里还不存在的键按 (标的, 频率, 序列标签) 累加 view_series_counts。
// 必须在写行之前执行：写行之后这些键已经存在，就数不出哪些是新键。
func countNewSeriesRows(ctx context.Context, tx *sql.Tx, keyLists []any) error {
	selects := make([]string, len(keyColumnTypes))
	for i, name := range []string{"subject_id", "freq", "data_time", "series_tag"} {
		selects[i] = fmt.Sprintf("unnest(?::%s[]) AS %s", keyColumnTypes[i], quote(name))
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO view_series_counts (subject_id, freq, series_tag, row_count)
		SELECT s.subject_id, s.freq, s.series_tag, count(*) FROM (SELECT `+strings.Join(selects, ", ")+`) s
		WHERE NOT EXISTS (SELECT 1 FROM view_rows v WHERE v.subject_id = s.subject_id AND v.freq = s.freq AND v.data_time = s.data_time AND v.series_tag = s.series_tag)
		GROUP BY s.subject_id, s.freq, s.series_tag
		ON CONFLICT (subject_id, freq, series_tag) DO UPDATE SET row_count = view_series_counts.row_count + excluded.row_count`, keyLists...)
	return err
}

// bulkUpsertSQL 生成整批 UPSERT：每个参数是一个列的 LIST，unnest 后按位置对齐成行；含 NULL 的列多带一个布尔掩码 LIST。
func bulkUpsertSQL(names, listTypes []string, masked, bytesColumns []bool, mode viewindex.WriteMode) string {
	inner := make([]string, 0, len(names)+2)
	outer := make([]string, len(names))
	for i, name := range names {
		unnested := fmt.Sprintf("unnest(?::%s)", listTypes[i])
		if bytesColumns[i] {
			unnested = "from_hex(" + unnested + ")"
		}
		inner = append(inner, fmt.Sprintf("%s AS %s", unnested, quote("__v_"+name)))
		if masked[i] {
			inner = append(inner, fmt.Sprintf("unnest(?::BOOLEAN[]) AS %s", quote("__n_"+name)))
			outer[i] = fmt.Sprintf("CASE WHEN %s THEN NULL ELSE %s END AS %s", quote("__n_"+name), quote("__v_"+name), quote(name))
		} else {
			outer[i] = fmt.Sprintf("%s AS %s", quote("__v_"+name), quote(name))
		}
	}
	return fmt.Sprintf("INSERT INTO view_rows (%s) SELECT %s FROM (SELECT %s) %s", joinQuoted(names), strings.Join(outer, ", "), strings.Join(inner, ", "), upsertConflictSQL(names, mode))
}

// nullPlaceholder 是各 LIST 类型里代替 NULL 的占位值；JSON 要能通过 JSON 转换，时间要在 TIMESTAMP_NS 的范围内。
func nullPlaceholder(listType string) any {
	switch listType {
	case "BIGINT[]":
		return int64(0)
	case "DOUBLE[]":
		return float64(0)
	case "BOOLEAN[]":
		return false
	case "TIMESTAMP_NS[]":
		return time.Unix(0, 0).UTC()
	case "JSON[]":
		return "null"
	default:
		return ""
	}
}

// upsertConflictSQL 是 UPSERT 的冲突处理：实时写入整行覆盖，回填只补空值；内部属性 JSON 按键合并。
func upsertConflictSQL(names []string, mode viewindex.WriteMode) string {
	sets := make([]string, 0, len(names)-4)
	for _, name := range names[4:] {
		// Live writes overwrite complete rows. Backfill only fills missing values.
		set := fmt.Sprintf("%s = excluded.%s", quote(name), quote(name))
		if name == internalAttributesColumn {
			// Dataset events often carry only a subset of provenance attributes.
			// Merge JSON objects by key so a later factor patch cannot erase hashes
			// written by earlier factors on the same projected row.
			set = fmt.Sprintf("%s = CASE WHEN excluded.%s IS NULL THEN view_rows.%s ELSE json_merge_patch(COALESCE(view_rows.%s, '{}'), excluded.%s) END", quote(name), quote(name), quote(name), quote(name), quote(name))
		}
		if mode == viewindex.Backfill {
			if name == internalAttributesColumn {
				// Backfill must never overwrite live provenance. Put the
				// backfill object first so existing/live keys win on conflicts.
				set = fmt.Sprintf("%s = CASE WHEN view_rows.%s IS NULL THEN excluded.%s ELSE json_merge_patch(COALESCE(excluded.%s, '{}'), view_rows.%s) END", quote(name), quote(name), quote(name), quote(name), quote(name))
			} else {
				set = fmt.Sprintf("%s = COALESCE(view_rows.%s, excluded.%s)", quote(name), quote(name), quote(name))
			}
		}
		sets = append(sets, set)
	}
	conflict := "ON CONFLICT (subject_id, freq, data_time, series_tag) DO NOTHING"
	if len(sets) > 0 {
		conflict = "ON CONFLICT (subject_id, freq, data_time, series_tag) DO UPDATE SET " + strings.Join(sets, ", ")
	}
	return conflict
}
