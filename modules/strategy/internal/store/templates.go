package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Template 是一份策略模板：解析与校验由 template 包负责，这里只存原文与展示字段。
type Template struct {
	TemplateID   string
	Name         string
	Description  string
	TemplateYAML string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type templateRow struct {
	TemplateID   string    `gorm:"column:c_template_id"`
	Name         string    `gorm:"column:c_name"`
	Description  string    `gorm:"column:c_description"`
	TemplateYAML string    `gorm:"column:c_template_yaml"`
	CreatedAt    time.Time `gorm:"column:c_ctime"`
	UpdatedAt    time.Time `gorm:"column:c_mtime"`
}

func (r templateRow) template() Template {
	return Template{TemplateID: r.TemplateID, Name: r.Name, Description: r.Description, TemplateYAML: r.TemplateYAML, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}

// 模板的 SQL：整条写成常量，不在调用处拼接；读模板的语句列清单与 templateRow 一一对应。
const (
	// sqlUpsertTemplate 按模板 ID 新建或整体替换模板（保留原创建时间）。
	sqlUpsertTemplate = `
		INSERT INTO t_strategy_templates (c_template_id, c_name, c_description, c_template_yaml, c_ctime, c_mtime)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (c_template_id) DO UPDATE SET c_name = excluded.c_name, c_description = excluded.c_description,
			c_template_yaml = excluded.c_template_yaml, c_mtime = excluded.c_mtime`
	// sqlSelectTemplateByID 按模板 ID 读取模板。
	sqlSelectTemplateByID = `SELECT c_template_id, c_name, c_description, c_template_yaml, c_ctime, c_mtime FROM t_strategy_templates WHERE c_template_id = ?`
	// sqlListTemplates 按模板 ID 列出全部模板。
	sqlListTemplates = `SELECT c_template_id, c_name, c_description, c_template_yaml, c_ctime, c_mtime FROM t_strategy_templates ORDER BY c_template_id`
	// sqlDeleteTemplate 删除一个模板。
	sqlDeleteTemplate = `DELETE FROM t_strategy_templates WHERE c_template_id = ?`
)

// SaveTemplate 新建或整体替换一个模板（按模板 ID）。
func (s *Store) SaveTemplate(ctx context.Context, value Template) (Template, error) {
	if strings.TrimSpace(value.TemplateID) == "" || strings.TrimSpace(value.Name) == "" || strings.TrimSpace(value.TemplateYAML) == "" {
		return Template{}, errors.New("模板缺少 template_id、name 或 template_yaml")
	}
	now, err := requireTime(value.UpdatedAt)
	if err != nil {
		return Template{}, err
	}
	if err := s.db.WithContext(ctx).Exec(sqlUpsertTemplate, value.TemplateID, value.Name, value.Description, value.TemplateYAML, now, now).Error; err != nil {
		return Template{}, err
	}
	saved, found, err := s.GetTemplate(ctx, value.TemplateID)
	if err != nil {
		return Template{}, err
	}
	if !found {
		return Template{}, errors.New("模板保存后读取不到")
	}
	return saved, nil
}

// GetTemplate 读取一个模板。
func (s *Store) GetTemplate(ctx context.Context, templateID string) (Template, bool, error) {
	var row templateRow
	if err := s.db.WithContext(ctx).Raw(sqlSelectTemplateByID, templateID).Scan(&row).Error; err != nil {
		return Template{}, false, err
	}
	if row.TemplateID == "" {
		return Template{}, false, nil
	}
	return row.template(), true, nil
}

// ListTemplates 按模板 ID 列出全部模板。
func (s *Store) ListTemplates(ctx context.Context) ([]Template, error) {
	var rows []templateRow
	if err := s.db.WithContext(ctx).Raw(sqlListTemplates).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Template, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.template())
	}
	return out, nil
}

// DeleteTemplate 删除一个模板；已生成的策略不受影响。
func (s *Store) DeleteTemplate(ctx context.Context, templateID string) (bool, error) {
	result := s.db.WithContext(ctx).Exec(sqlDeleteTemplate, templateID)
	return result.RowsAffected > 0, result.Error
}
