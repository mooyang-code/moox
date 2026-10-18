package service

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
)

// sanitizeFilePrefix 把数据集名变成文件名前缀：去掉所有空白，路径不安全或控制字符换成下划线，去掉开头的点；
// 结果为空时返回空串（调用方改用数据集 ID）。
func sanitizeFilePrefix(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsSpace(r):
		case unicode.IsControl(r), strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), ".")
}

// chooseFilePrefix 为数据集选择文件名前缀：用数据集名去空格；为空或与空间内其他任务的前缀重名时，
// 退回到「名称_数据集 ID」或数据集 ID，保证不同数据集的文件不会互相覆盖。
func (s *Service) chooseFilePrefix(ctx context.Context, spaceID, datasetID, name string) (string, error) {
	tasks, _, err := s.store.ListTasks(ctx, spaceID, 100000, 0)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, t := range tasks {
		if t.DatasetID != datasetID && t.FilePrefix != "" {
			taken[strings.ToLower(t.FilePrefix)] = true
		}
	}
	base := sanitizeFilePrefix(name)
	for _, candidate := range []string{base, base + "_" + datasetID, datasetID} {
		if candidate != "" && candidate != "_"+datasetID && !taken[strings.ToLower(candidate)] {
			return candidate, nil
		}
	}
	return datasetID, nil
}

// datasetMeta 是写进归档根目录元数据文件的数据集信息（由 mgr 从 Storage 读取，随运行下发给 worker）。
type datasetMeta struct {
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	DataKind        string            `json:"data_kind"`
	Freq            string            `json:"freq"`
	Status          string            `json:"status"`
	DataSourceID    string            `json:"data_source_id,omitempty"`
	DataNodeID      string            `json:"data_node_id,omitempty"`
	Retention       string            `json:"online_retention"`
	RetentionSource string            `json:"online_retention_source,omitempty"`
	SubjectTags     []string          `json:"subject_tags,omitempty"`
	Attributes      map[string]string `json:"attributes,omitempty"`
	CreatedAt       string            `json:"created_at,omitempty"`
	UpdatedAt       string            `json:"updated_at,omitempty"`
	Revision        uint64            `json:"revision,omitempty"`
}

// metaJSON 把数据集信息序列化成 JSON（键顺序固定，内容不变时输出不变）。
func metaJSON(ds Dataset) string {
	raw, err := json.Marshal(datasetMeta{
		Name: ds.Name, Description: ds.Description, DataKind: ds.DataKind, Freq: ds.Freq, Status: ds.Status, DataSourceID: ds.DataSourceID,
		DataNodeID: ds.DataNodeID, Retention: ds.Retention, RetentionSource: ds.RetentionSource, SubjectTags: ds.SubjectTags,
		Attributes: ds.Attributes, CreatedAt: ds.CreatedAt, UpdatedAt: ds.UpdatedAt, Revision: ds.Revision,
	})
	if err != nil {
		return ""
	}
	return string(raw)
}
