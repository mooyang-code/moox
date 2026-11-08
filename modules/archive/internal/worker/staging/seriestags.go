package staging

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// DistinctSeriesTags 返回任务暂存表里 series_tag 的不同取值，取到 limit 个就停；暂存表不存在时返回空。
func (s *Store) DistinctSeriesTags(ctx context.Context, task Task, limit int) ([]string, error) {
	have, err := s.tableColumns(ctx, task.Table())
	if err != nil || len(have) == 0 {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT DISTINCT series_tag FROM %s LIMIT ?`, quote(task.Table())), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

// FileSeriesTags 读归档文件（.csv 或 .csv.gz）里 series_tag 的不同取值，取到 limit 个就停。
func FileSeriesTags(path string, limit int) ([]string, error) {
	reader, err := OpenCSV(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("读取 %s 的表头: %w", path, err)
	}
	column := -1
	for i, cell := range header {
		if cell.Value == "series_tag" {
			column = i
		}
	}
	if column < 0 {
		return nil, fmt.Errorf("归档文件 %s 没有 series_tag 列", path)
	}
	seen := map[string]bool{}
	var out []string
	for len(out) < limit {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取 %s 第 %d 行: %w", path, reader.Line(), err)
		}
		if column < len(record) && !seen[record[column].Value] {
			seen[record[column].Value] = true
			out = append(out, record[column].Value)
		}
	}
	return out, nil
}
