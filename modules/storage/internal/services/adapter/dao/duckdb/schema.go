package duckdb

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/template"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

//go:embed schema.tpl
var SchemaTpl string

// FieldCounts 字段数量统计结构
type FieldCounts struct {
	BigintCount int // c_bigint 字段数量
	StringCount int // c_string 字段数量
	FloatCount  int // c_float 字段数量
	JSONCount   int // c_json 字段数量
	TimeCount   int // c_time 字段数量
}

var createTableLocks = struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}{
	locks: make(map[string]*sync.Mutex),
}

var (
	schemaFieldCountsOnce sync.Once
	schemaFieldCounts     *FieldCounts
	schemaFieldCountsErr  error
)

func lockCreateTable(tableName string) func() {
	createTableLocks.mu.Lock()
	lock, ok := createTableLocks.locks[tableName]
	if !ok {
		lock = &sync.Mutex{}
		createTableLocks.locks[tableName] = lock
	}
	createTableLocks.mu.Unlock()

	lock.Lock()
	return func() {
		lock.Unlock()
	}
}

// ============================================================================
// 接口层函数 - 对外提供的主要功能
// ============================================================================

// CreateTable 创建表
func (d *DuckDB) CreateTable(c context.Context, params *dao.CreateTableParams) error {
	if params == nil {
		return fmt.Errorf("创建表参数不能为空")
	}
	if params.TableID == "" {
		return fmt.Errorf("表名不能为空")
	}
	tableName := params.TableID
	unlock := lockCreateTable(tableName)
	defer unlock()
	log.DebugContextf(c, "开始创建DuckDB表: %s", tableName)
	ctx := trpc.CloneContext(c)

	// 检查表是否已存在
	exists, err := d.CheckTable(ctx, tableName)
	if err != nil {
		log.ErrorContextf(ctx, "检查表[%s]是否存在失败: %v", tableName, err)
		return err
	}

	// 如果表已存在且不强制创建，返回
	if exists && !params.ForceCreate {
		log.InfoContextf(ctx, "表[%s]已存在，如需覆盖请设置ForceCreate=true", tableName)
		return nil
	}

	// 如果表已存在且强制创建，先删除表
	if exists && params.ForceCreate {
		log.InfoContextf(ctx, "表[%s]已存在，强制创建模式，先删除原表", tableName)
		if err := d.DropTable(ctx, tableName); err != nil {
			log.ErrorContextf(ctx, "删除已存在表[%s]失败: %v", tableName, err)
			return err
		}
	}

	// 使用模板生成建表语句
	createSQL, err := d.generateCreateTableSQL(tableName, params.DataType)
	if err != nil {
		log.ErrorContextf(ctx, "生成建表语句失败: %v", err)
		return err
	}
	log.DebugContextf(ctx, "执行建表SQL: %s", createSQL)

	// 执行建表语句
	_, err = d.db.ExecContext(ctx, createSQL)
	if err != nil {
		log.ErrorContextf(ctx, "建表失败: %v, SQL: %s", err, createSQL)
		return errs.New(int(pb.EnumErrorCode_INNER_ERR), fmt.Sprintf("创建表失败: %v", err))
	}
	log.InfoContextf(ctx, "表[%s]创建成功", tableName)
	return nil
}

// CheckTable 检查表是否存在
func (d *DuckDB) CheckTable(c context.Context, tableName string) (bool, error) {
	ctx := trpc.CloneContext(c)
	if tableName == "" {
		return false, fmt.Errorf("表名不能为空")
	}

	// 使用DuckDB的information_schema查询表是否存在
	// 注意：information_schema中的table_name不包含引号，所以直接使用原始表名
	query := `SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?`
	var count int
	err := d.db.QueryRowContext(ctx, query, tableName).Scan(&count)
	if err != nil {
		log.ErrorContextf(ctx, "查询表[%s]是否存在失败: %v", tableName, err)
		return false, err
	}

	exists := count > 0
	log.DebugContextf(ctx, "表[%s]存在性检查结果: %v", tableName, exists)
	return exists, nil
}

// DropTable 删除表
func (d *DuckDB) DropTable(c context.Context, tableName string) error {
	ctx := trpc.CloneContext(c)
	if tableName == "" {
		return fmt.Errorf("表名不能为空")
	}
	dropSQL := fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName)
	log.DebugContextf(ctx, "执行删表SQL: %s", dropSQL)

	// 执行SQL语句
	_, err := d.db.ExecContext(ctx, dropSQL)
	if err != nil {
		log.ErrorContextf(ctx, "删表失败: %v, SQL: %s", err, dropSQL)
		return err
	}
	log.InfoContextf(ctx, "表[%s]删除成功", tableName)
	return nil
}

// GetSchemaFieldLimit 获取指定字段类型的最大数量限制
// 实现 TableOperator 接口，从模板动态获取字段数量限制
func (d *DuckDB) GetSchemaFieldLimit(fieldType string) (int, error) {
	counts, err := d.GetSchemaFieldCounts()
	if err != nil {
		return 0, fmt.Errorf("获取字段数量统计失败: %v", err)
	}

	switch fieldType {
	case "c_bigint":
		return counts.BigintCount, nil
	case "c_string":
		return counts.StringCount, nil
	case "c_float":
		return counts.FloatCount, nil
	case "c_json":
		return counts.JSONCount, nil
	case "c_time":
		return counts.TimeCount, nil
	default:
		return 0, fmt.Errorf("不支持的字段类型: %s", fieldType)
	}
}

// GetMaxAllowedSuffix 获取指定列名前缀的最大允许序号
// 保留原方法名以兼容现有代码，内部调用新的接口方法
func (d *DuckDB) GetMaxAllowedSuffix(columnPrefix string) (int, error) {
	return d.GetSchemaFieldLimit(columnPrefix)
}

// ============================================================================
// 辅助工具函数 - 底层工具和转换函数
// ============================================================================

// generateCreateTableSQL 使用模板生成建表语句
func (d *DuckDB) generateCreateTableSQL(tableName string, dataType pb.EnumDataTypeCategory) (string, error) {
	// 解析模板
	tmpl, err := template.New("schema").Parse(SchemaTpl)
	if err != nil {
		return "", fmt.Errorf("解析建表模板失败: %v", err)
	}

	// 将枚举类型转换为字符串，以便在模板中进行比较
	var dataTypeStr string
	if dataType == pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE {
		dataTypeStr = "time_series"
	} else {
		dataTypeStr = "static"
	}

	data := map[string]interface{}{
		"data_type":       dataTypeStr,
		"table_name":      tableName,
		"safe_table_name": d.generateSafeIdentifier(tableName), // 生成安全的索引前缀，移除特殊字符
		"is_object_table": strings.HasPrefix(tableName, "t_object"),
	}

	// 执行模板
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, data)
	if err != nil {
		return "", fmt.Errorf("执行建表模板失败: %v", err)
	}
	return buf.String(), nil
}

// generateSafeIdentifier 生成安全的标识符，用于索引名等
func (d *DuckDB) generateSafeIdentifier(identifier string) string {
	// 将特殊字符替换为下划线，确保索引名的唯一性和合法性
	safe := strings.ReplaceAll(identifier, "-", "_")
	safe = strings.ReplaceAll(safe, ".", "_")
	safe = strings.ReplaceAll(safe, " ", "_")
	safe = strings.ReplaceAll(safe, ":", "_")
	return safe
}

// updateFieldCount 更新指定字段类型的最大数量
func updateFieldCount(counts *FieldCounts, fieldType string, num int) {
	switch fieldType {
	case "bigint":
		if num > counts.BigintCount {
			counts.BigintCount = num
		}
	case "string":
		if num > counts.StringCount {
			counts.StringCount = num
		}
	case "float":
		if num > counts.FloatCount {
			counts.FloatCount = num
		}
	case "json":
		if num > counts.JSONCount {
			counts.JSONCount = num
		}
	case "time":
		if num > counts.TimeCount {
			counts.TimeCount = num
		}
	}
}

// GetSchemaFieldCounts 从SchemaTpl模板中动态获取各种字段类型的数量
// 返回各种字段（除了以下划线开头的系统字段）的数量统计
func (d *DuckDB) GetSchemaFieldCounts() (*FieldCounts, error) {
	schemaFieldCountsOnce.Do(func() {
		schemaFieldCounts, schemaFieldCountsErr = parseSchemaFieldCounts()
	})
	if schemaFieldCounts == nil {
		return nil, schemaFieldCountsErr
	}
	return schemaFieldCounts, schemaFieldCountsErr
}

func parseSchemaFieldCounts() (*FieldCounts, error) {
	counts := &FieldCounts{}

	// 定义正则表达式匹配各种字段类型
	patterns := map[string]*regexp.Regexp{
		"bigint": regexp.MustCompile(`"c_bigint_(\d+)"`),
		"string": regexp.MustCompile(`"c_string_(\d+)"`),
		"float":  regexp.MustCompile(`"c_float_(\d+)"`),
		"json":   regexp.MustCompile(`"c_json_(\d+)"`),
		"time":   regexp.MustCompile(`"c_time_(\d+)"`),
	}

	lines := strings.Split(SchemaTpl, "\n")
	for _, line := range lines {
		// 跳过以下划线开头的系统字段
		if strings.Contains(line, `"_`) {
			continue
		}

		// 处理每种字段类型的匹配
		for fieldType, pattern := range patterns {
			matches := pattern.FindAllStringSubmatch(line, -1)
			processFieldMatches(counts, fieldType, matches)
		}
	}
	return counts, nil
}

// processFieldMatches 处理字段匹配结果
func processFieldMatches(counts *FieldCounts, fieldType string, matches [][]string) {
	for _, match := range matches {
		if len(match) <= 1 {
			continue
		}

		num, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		updateFieldCount(counts, fieldType, num)
	}
}
