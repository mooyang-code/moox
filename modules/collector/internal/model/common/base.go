package common

import "time"

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
