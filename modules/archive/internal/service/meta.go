package service

import "encoding/json"

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
