package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DisplayHidden 是实例外显设置里隐藏的内容；没有记录时全部展示。
type DisplayHidden struct {
	Categories []string `json:"categories,omitempty"`
	Members    []string `json:"members,omitempty"`
	Columns    []string `json:"columns,omitempty"`
}

// 外显设置的数量上限：每类最多这么多项，每项最长这么多字符。
const (
	maxDisplayItems  = 500
	maxDisplayLength = 128
)

func normalizeDisplay(hidden DisplayHidden) (DisplayHidden, error) {
	clean := func(name string, values []string) ([]string, error) {
		if len(values) > maxDisplayItems {
			return nil, fmt.Errorf("外显设置的 %s 最多 %d 项", name, maxDisplayItems)
		}
		seen := make(map[string]struct{}, len(values))
		out := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if len(value) > maxDisplayLength {
				return nil, fmt.Errorf("外显设置的 %s 含过长的项", name)
			}
			if _, dup := seen[value]; dup {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
		sort.Strings(out)
		return out, nil
	}
	var err error
	if hidden.Categories, err = clean("categories", hidden.Categories); err != nil {
		return DisplayHidden{}, err
	}
	if hidden.Members, err = clean("members", hidden.Members); err != nil {
		return DisplayHidden{}, err
	}
	if hidden.Columns, err = clean("columns", hidden.Columns); err != nil {
		return DisplayHidden{}, err
	}
	return hidden, nil
}

// GetDisplayHidden 读取实例的外显设置；没有记录时返回空设置（全部展示）。
func (s *Store) GetDisplayHidden(ctx context.Context, instanceID string) (DisplayHidden, error) {
	var raw string
	var count int64
	if err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM t_strategy_display_settings WHERE c_instance_id = ?`, instanceID).Scan(&count).Error; err != nil {
		return DisplayHidden{}, err
	}
	if count == 0 {
		return DisplayHidden{}, nil
	}
	if err := s.db.WithContext(ctx).Raw(`SELECT c_hidden_json FROM t_strategy_display_settings WHERE c_instance_id = ?`, instanceID).Scan(&raw).Error; err != nil {
		return DisplayHidden{}, err
	}
	var hidden DisplayHidden
	if err := json.Unmarshal([]byte(raw), &hidden); err != nil {
		return DisplayHidden{}, fmt.Errorf("外显设置已损坏：%w", err)
	}
	return hidden, nil
}

// SetDisplayHidden 保存实例的外显设置；全部展示时删除记录。实例不存在或已删除返回 ErrNotFound。
func (s *Store) SetDisplayHidden(ctx context.Context, instanceID string, hidden DisplayHidden, at time.Time) (DisplayHidden, error) {
	hidden, err := normalizeDisplay(hidden)
	if err != nil {
		return DisplayHidden{}, err
	}
	if _, err := s.GetInstance(ctx, instanceID); err != nil {
		return DisplayHidden{}, err
	}
	if len(hidden.Categories)+len(hidden.Members)+len(hidden.Columns) == 0 {
		return hidden, s.db.WithContext(ctx).Exec(`DELETE FROM t_strategy_display_settings WHERE c_instance_id = ?`, instanceID).Error
	}
	raw, err := json.Marshal(hidden)
	if err != nil {
		return DisplayHidden{}, err
	}
	now := at.UTC()
	return hidden, s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategy_display_settings (c_instance_id, c_hidden_json, c_ctime, c_mtime) VALUES (?, ?, ?, ?)
		ON CONFLICT (c_instance_id) DO UPDATE SET c_hidden_json = excluded.c_hidden_json, c_mtime = excluded.c_mtime
	`, instanceID, string(raw), now, now).Error
}
