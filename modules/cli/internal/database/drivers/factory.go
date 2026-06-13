package drivers

import (
	"fmt"
	"strings"
)

// CreateDatabaseDriver 根据存储设备类型创建对应的数据库驱动
func CreateDatabaseDriver(storageInfo string) (DatabaseDriver, error) {
	// 解析存储设备信息，格式为 "类型:连接信息"
	parts := strings.SplitN(storageInfo, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("存储设备信息格式不正确，应为 '类型:连接信息'")
	}

	deviceType := strings.ToLower(parts[0])
	connectionString := parts[1]

	// 根据设备类型创建对应的驱动
	switch deviceType {
	case "sqlite", "sqllite": // 兼容拼写错误
		driver := &SQLiteDriver{}
		err := driver.Connect(connectionString)
		if err != nil {
			return nil, fmt.Errorf("连接 SQLite 数据库失败: %v", err)
		}
		return driver, nil
	// 可以在此添加其他数据库类型的支持
	default:
		return nil, fmt.Errorf("不支持的存储设备类型: %s", deviceType)
	}
}
