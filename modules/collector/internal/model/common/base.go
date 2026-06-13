package common

import (
	"encoding/json"
	"time"
)

// DataPoint 数据点接口
type DataPoint interface {
	// 数据源信息
	Source() string      // 数据来源
	SourceType() string  // 来源类型
	
	// 时间信息
	Timestamp() time.Time
	
	// 数据验证
	Validate() error
	
	// 序列化
	Marshal() ([]byte, error)
	Unmarshal([]byte) error
}

// BaseDataPoint 基础数据点实现
type BaseDataPoint struct {
	ID         string    `json:"id"`
	DataSource string    `json:"source"`
	DataType   string    `json:"type"`
	CreatedAt  time.Time `json:"created_at"`
}

func NewBaseDataPoint(source, dataType string) BaseDataPoint {
	return BaseDataPoint{
		ID:         generateID(),
		DataSource: source,
		DataType:   dataType,
		CreatedAt:  time.Now(),
	}
}

func (b *BaseDataPoint) Source() string {
	return b.DataSource
}

func (b *BaseDataPoint) SourceType() string {
	return b.DataType
}

func (b *BaseDataPoint) Timestamp() time.Time {
	return b.CreatedAt
}

func (b *BaseDataPoint) Validate() error {
	// 基础验证逻辑
	return nil
}

func (b *BaseDataPoint) Marshal() ([]byte, error) {
	return json.Marshal(b)
}

func (b *BaseDataPoint) Unmarshal(data []byte) error {
	return json.Unmarshal(data, b)
}

// 生成唯一ID的辅助函数
func generateID() string {
	// 简化实现，实际应使用 UUID
	return time.Now().Format("20060102150405") + "-" + randomString(8)
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, n)
	for i := range result {
		result[i] = letters[time.Now().UnixNano()%int64(len(letters))]
	}
	return string(result)
}