package drivers

// DatabaseDriver 定义通用的数据库操作接口
type DatabaseDriver interface {
	Connect(connectionString string) error                    // 连接数据库
	Close() error                                             // 关闭数据库连接
	TableExists(tableName string) (bool, error)               // 检查表是否存在
	ExecuteSQL(sqlStmt string) error                          // 执行 SQL 语句
	DropTable(tableName string) error                         // 删除表
	CreateTable(tableName string, schema string) error        // 创建表
	ShowTableSchema(tableName string) error                   // 显示表结构
	InsertData(tableName string, record map[string]any) error // 插入数据记录(主键冲突时更新)
	ShowData(tableName string, limit int) error               // 显示表中的最近数据
}
