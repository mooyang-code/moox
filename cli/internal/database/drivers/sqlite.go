package drivers

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // SQLite 驱动 (纯Go实现，无需CGO)
)

// SQLiteDriver 是 SQLite 的具体实现
type SQLiteDriver struct {
	db               *sql.DB
	connectionString string
	connected        bool
}

// Connect 连接 SQLite 数据库
func (driver *SQLiteDriver) Connect(connectionString string) error {
	driver.connectionString = connectionString
	// 记录连接字符串但不立即连接
	// 实际连接会在需要时进行
	driver.connected = false
	return nil
}

// ensureConnected 确保已连接到数据库
func (driver *SQLiteDriver) ensureConnected() error {
	if driver.connected && driver.db != nil {
		return nil
	}

	// 确保数据库目录存在
	dbDir := filepath.Dir(driver.connectionString)
	if dbDir != "." && dbDir != "/" {
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			return fmt.Errorf("创建数据库目录失败: %v", err)
		}
	}

	// 连接数据库
	var err error
	driver.db, err = sql.Open("sqlite3", driver.connectionString)
	if err != nil {
		return err
	}

	// 测试连接
	if err = driver.db.Ping(); err != nil {
		return err
	}
	driver.connected = true
	return nil
}

// Close 关闭 SQLite 数据库连接
func (driver *SQLiteDriver) Close() error {
	if driver.db != nil {
		driver.connected = false
		return driver.db.Close()
	}
	return nil
}

// TableExists 检查 SQLite 表是否存在
func (driver *SQLiteDriver) TableExists(tableName string) (bool, error) {
	// 确保已连接
	if err := driver.ensureConnected(); err != nil {
		// 如果连接失败，假设是因为数据库不存在，返回false但不报错
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
			return false, nil
		}
		return false, err
	}

	// 使用LIKE进行不区分大小写的比较，并在name前后不添加引号
	query := `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND LOWER(name)=LOWER(?);`
	var count int
	err := driver.db.QueryRow(query, tableName).Scan(&count)
	return count > 0, err
}

// ExecuteSQL 执行 SQL 语句
func (driver *SQLiteDriver) ExecuteSQL(sqlStmt string) error {
	// 确保已连接
	if err := driver.ensureConnected(); err != nil {
		return err
	}

	// 限制长SQL的日志输出
	displaySQL := sqlStmt
	if len(displaySQL) > 100 {
		displaySQL = displaySQL[:100] + "..."
	}
	fmt.Printf("执行SQL: %s\n", displaySQL)
	_, err := driver.db.Exec(sqlStmt)
	return err
}

// DropTable 删除表
func (driver *SQLiteDriver) DropTable(tableName string) error {
	// 确保已连接
	if err := driver.ensureConnected(); err != nil {
		return err
	}

	_, err := driver.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s;", tableName))
	return err
}

// CreateTable 创建表
func (driver *SQLiteDriver) CreateTable(tableName string, schema string) error {
	// 确保已连接
	if err := driver.ensureConnected(); err != nil {
		return err
	}

	// 检查表是否存在
	exists, err := driver.TableExists(tableName)
	if err != nil {
		return fmt.Errorf("检查表是否存在失败: %v", err)
	}

	if exists {
		// 提示用户是否覆盖
		fmt.Printf("表 %s 已存在，是否覆盖？(Y/N): ", tableName)
		var choice string
		fmt.Scanln(&choice)
		if strings.ToUpper(choice) == "Y" {
			// 删除表
			err := driver.DropTable(tableName)
			if err != nil {
				return fmt.Errorf("删除表 %s 失败: %v", tableName, err)
			}
			fmt.Printf("表 %s 已删除。\n", tableName)
		} else {
			fmt.Printf("跳过表 %s 的创建。\n", tableName)
			return nil
		}
	}

	// 执行建表语句，添加IF NOT EXISTS以确保SQL语句不会在表已存在时报错
	createSQL := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s);", tableName, schema)
	fmt.Printf("执行建表SQL: %s\n", createSQL)
	_, err = driver.db.Exec(createSQL)
	if err != nil {
		return fmt.Errorf("创建表 %s 失败: %v", tableName, err)
	}

	fmt.Printf("表 %s 已成功创建！\n", tableName)
	return nil
}

// InsertData 向指定表中插入一条记录
func (driver *SQLiteDriver) InsertData(tableName string, record map[string]any) error {
	// 确保已连接
	if err := driver.ensureConnected(); err != nil {
		return err
	}

	if len(record) == 0 {
		return fmt.Errorf("记录不能为空")
	}

	var columns []string
	var params []string
	var args []any

	for col, val := range record {
		columns = append(columns, col)
		params = append(params, "?")
		args = append(args, val)
	}

	// 使用INSERT OR REPLACE语法实现UPSERT操作
	sql := fmt.Sprintf("INSERT OR REPLACE INTO %s (%s) VALUES (%s)",
		tableName,
		strings.Join(columns, ", "),
		strings.Join(params, ", "))

	// 限制长SQL的日志输出
	displaySQL := sql
	if len(displaySQL) > 100 {
		displaySQL = displaySQL[:100] + "..."
	}
	fmt.Printf("执行插入/更新SQL: %s\n", displaySQL)
	_, err := driver.db.Exec(sql, args...)
	return err
}

// ShowData 显示表中的最近数据
func (driver *SQLiteDriver) ShowData(tableName string, limit int) error {
	// 确保已连接
	if err := driver.ensureConnected(); err != nil {
		return err
	}

	// 获取表的所有列
	columnQuery := fmt.Sprintf("PRAGMA table_info(%s);", tableName)
	columnRows, err := driver.db.Query(columnQuery)
	if err != nil {
		return fmt.Errorf("获取表 %s 的列信息失败: %v", tableName, err)
	}

	var columns []string
	for columnRows.Next() {
		var cid int
		var name, ctype string
		var notnull, dfltValue, pk any
		err := columnRows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk)
		if err != nil {
			columnRows.Close()
			return fmt.Errorf("读取列信息失败: %v", err)
		}
		columns = append(columns, name)
	}
	columnRows.Close()

	if len(columns) == 0 {
		return fmt.Errorf("表 %s 没有列", tableName)
	}

	// 查询最近修改的记录
	// SQLite默认没有修改时间戳，我们查询所有记录并按rowid降序排序（假设最近添加/修改的记录rowid较大）
	query := fmt.Sprintf("SELECT * FROM %s ORDER BY rowid DESC LIMIT %d", tableName, limit)
	rows, err := driver.db.Query(query)
	if err != nil {
		return fmt.Errorf("查询表 %s 数据失败: %v", tableName, err)
	}
	defer rows.Close()
	fmt.Printf("表 %s 的最近 %d 条数据:\n", tableName, limit)

	// 确定最长的列名，用于对齐输出
	maxColLen := 0
	for _, col := range columns {
		if len(col) > maxColLen {
			maxColLen = len(col)
		}
	}

	// 准备存储每行数据的值
	values := make([]interface{}, len(columns))
	valuePtrs := make([]interface{}, len(columns))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	// 读取并打印数据
	count := 0
	rowSeparator := strings.Repeat("*", 27)
	for rows.Next() {
		err := rows.Scan(valuePtrs...)
		if err != nil {
			return fmt.Errorf("读取行数据失败: %v", err)
		}

		// 打印行分隔符和行号
		count++
		fmt.Printf("%s %d. row %s\n", rowSeparator, count, rowSeparator)

		// 将每个字段的名称和值按垂直格式打印
		for i, col := range columns {
			val := values[i]
			valStr := "NULL"
			if val != nil {
				switch v := val.(type) {
				case []byte:
					valStr = string(v)
				default:
					valStr = fmt.Sprintf("%v", v)
				}
			}
			// 右对齐列名，加上冒号和值
			fmt.Printf("%*s: %s\n", maxColLen, col, valStr)
		}
	}
	if count == 0 {
		fmt.Println("没有找到数据")
		return nil
	}

	// 查询表的总记录数
	var totalCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName)
	if err := driver.db.QueryRow(countQuery).Scan(&totalCount); err != nil {
		return fmt.Errorf("获取表 %s 总记录数失败: %v", tableName, err)
	}
	fmt.Printf("\n显示了 %d 条记录，表中共有 %d 条记录\n", count, totalCount)
	return nil
}

// ShowTableIndexes 显示表的索引信息
func (driver *SQLiteDriver) ShowTableIndexes(tableName string) error {
	// 确保已连接
	if err := driver.ensureConnected(); err != nil {
		return err
	}

	// 获取表的所有索引
	indexQuery := fmt.Sprintf("PRAGMA index_list(%s);", tableName)
	indexRows, err := driver.db.Query(indexQuery)
	if err != nil {
		return fmt.Errorf("获取表 %s 的索引信息失败: %v", tableName, err)
	}
	defer indexRows.Close()

	fmt.Printf("表 %s 的索引信息:\n", tableName)
	fmt.Println("序号\t索引名称\t\t唯一性\t创建方式\t部分索引")

	var hasIndexes bool
	for indexRows.Next() {
		hasIndexes = true
		var seq int
		var name string
		var unique, origin, partial interface{}

		err := indexRows.Scan(&seq, &name, &unique, &origin, &partial)
		if err != nil {
			return fmt.Errorf("读取索引信息失败: %v", err)
		}

		fmt.Printf("%d\t%-20s\t%v\t%v\t%v\n", seq, name, unique, origin, partial)

		// 获取每个索引的列信息
		indexInfoQuery := fmt.Sprintf("PRAGMA index_info(%s);", name)
		indexInfoRows, err := driver.db.Query(indexInfoQuery)
		if err != nil {
			fmt.Printf("  无法获取索引 %s 的列信息: %v\n", name, err)
			continue
		}

		fmt.Println("  索引列信息:")
		fmt.Println("  序号\t列名\t\t排序顺序")

		for indexInfoRows.Next() {
			var seqno, cid int
			var name string

			err := indexInfoRows.Scan(&seqno, &cid, &name)
			if err != nil {
				indexInfoRows.Close()
				fmt.Printf("  读取索引列信息失败: %v\n", err)
				break
			}

			// 获取排序方向 (尝试获取，SQLite可能不支持此操作)
			var sortOrder string = "ASC" // 默认为升序
			fmt.Printf("  %d\t%-20s\t%s\n", seqno, name, sortOrder)
		}
		indexInfoRows.Close()
		fmt.Println()
	}

	if !hasIndexes {
		fmt.Printf("表 %s 没有索引\n", tableName)
	}

	return nil
}

// ShowTableStructure 显示表的结构信息
func (driver *SQLiteDriver) ShowTableSchema(tableName string) error {
	// 确保已连接
	if err := driver.ensureConnected(); err != nil {
		return err
	}

	// 获取表的所有列信息
	query := fmt.Sprintf("PRAGMA table_info(%s);", tableName)
	rows, err := driver.db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	fmt.Printf("表 %s 的结构:\n", tableName)
	fmt.Println("序号\t字段名\t\t类型\t非空\t默认值\t主键")

	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, dfltValue, pk interface{}
		err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk)
		if err != nil {
			return err
		}
		fmt.Printf("%d\t%-20s\t%s\t%v\t%v\t%v\n", cid, name, ctype, notnull, dfltValue, pk)
	}

	// 显示表的索引信息
	return driver.ShowTableIndexes(tableName)
}
