// Package sqlitedb 统一 MooX 各 SQLite 模块的连接参数、schema 校验与数据格式文件写入。
//
// 仅做两件事：
//  1. 按统一参数打开 SQLite（*sql.DB 或 *gorm.DB），默认启用 WAL、外键约束、busy_timeout 与 _txlock=immediate。
//  2. 把 schema 载入内存库，规范化 sqlite_master 中的表与索引语句并与目标库比对；结构不符时拒绝启动并提示全新部署，
//     校验通过后把格式记录写入组件数据目录的 data-format 文件。
package sqlitedb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	stdlog "log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	sqlitedriver "github.com/glebarez/go-sqlite"
	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/packages/dataformat"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	// DriverName 是纯 Go SQLite 驱动注册到 database/sql 的名字。
	DriverName = "sqlite"
	// DefaultBusyTimeoutMS 是默认的锁等待毫秒数。
	DefaultBusyTimeoutMS = 5000
	// DefaultReadOnlyConns 是只读连接池的默认连接数。
	DefaultReadOnlyConns = 4
)

// ErrIncompatibleSchema 表示已有数据库里的表或索引结构与当前版本 schema 不符。
var ErrIncompatibleSchema = errors.New("数据库表结构与当前版本不符")

// Options 控制 SQLite 连接参数与连接池。
type Options struct {
	Path string
	// Synchronous 默认 NORMAL；需要强落盘的模块（如 Admin）可传 FULL。
	Synchronous string
	// BusyTimeoutMS 默认 5000。
	BusyTimeoutMS int
	// AutoVacuum 默认空；需增量回收空闲页的模块（如 Archive 管理端）传 INCREMENTAL，排在 WAL 之前使新库生效。
	AutoVacuum string
	// ReadOnly 为真时打开只读连接（query_only=1，不带 _txlock=immediate，不建父目录）。
	ReadOnly bool
	// MaxOpenConns / MaxIdleConns：读写默认 1/1，只读默认 4/4。
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	// Logger 仅用于 OpenGORM；为空时使用默认静默未找到错误的 warn 日志。
	Logger gormlogger.Interface
	// SkipCreateDir 为真时不创建父目录（调用方已预建文件或只读打开时）。
	SkipCreateDir bool
}

// IsMemoryPath 报告路径是否为 SQLite 内存库。
func IsMemoryPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	return trimmed == ":memory:" || strings.Contains(trimmed, "mode=memory")
}

// DSN 按统一参数构造连接串。
func DSN(opts Options) string {
	busyMS := opts.BusyTimeoutMS
	if busyMS <= 0 {
		busyMS = DefaultBusyTimeoutMS
	}
	var params []string
	if opts.ReadOnly {
		params = []string{
			"_pragma=query_only(1)",
			fmt.Sprintf("_pragma=busy_timeout(%d)", busyMS),
		}
	} else {
		syncMode := strings.ToUpper(strings.TrimSpace(opts.Synchronous))
		if syncMode == "" {
			syncMode = "NORMAL"
		}
		if autoVacuum := strings.TrimSpace(opts.AutoVacuum); autoVacuum != "" {
			// auto_vacuum 只对尚未建表的新库生效，必须排在 journal_mode(WAL) 之前。
			params = append(params, fmt.Sprintf("_pragma=auto_vacuum(%s)", autoVacuum))
		}
		params = append(params,
			// 事务一开始就拿写锁（BEGIN IMMEDIATE），拿不到按 busy_timeout 等待；
			// 避免离线命令与运行中进程共用同一库时，延迟事务在对方提交后立刻报 database is locked。
			"_txlock=immediate",
			"_pragma=foreign_keys(ON)",
			"_pragma=journal_mode(WAL)",
			fmt.Sprintf("_pragma=synchronous(%s)", syncMode),
			fmt.Sprintf("_pragma=busy_timeout(%d)", busyMS),
			"_pragma=temp_store(MEMORY)",
			"_pragma=cache_size(-64000)",
			"_pragma=wal_autocheckpoint(1000)",
		)
	}
	sep := "?"
	if strings.Contains(opts.Path, "?") {
		sep = "&"
	}
	return opts.Path + sep + strings.Join(params, "&")
}

// OpenSQL 按统一参数打开 *sql.DB 并完成连接检查。
func OpenSQL(opts Options) (*sql.DB, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, errors.New("SQLite 数据库路径不能为空")
	}
	if !opts.ReadOnly && !opts.SkipCreateDir && !IsMemoryPath(opts.Path) && !strings.HasPrefix(opts.Path, "file:") {
		if err := os.MkdirAll(filepath.Dir(opts.Path), 0o755); err != nil {
			return nil, fmt.Errorf("创建数据库目录失败：%w", err)
		}
	}
	db, err := sql.Open(DriverName, DSN(opts))
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 数据库失败：%w", err)
	}
	applyPoolConfig(db, opts)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接 SQLite 数据库失败：%w", err)
	}
	return db, nil
}

// OpenGORM 按统一参数打开 *gorm.DB。
func OpenGORM(opts Options) (*gorm.DB, error) {
	sqlDB, err := OpenSQL(opts)
	if err != nil {
		return nil, err
	}
	logCfg := opts.Logger
	if logCfg == nil {
		logCfg = defaultGORMLogger
	}
	db, err := gorm.Open(sqlite.Dialector{DriverName: DriverName, Conn: sqlDB}, &gorm.Config{Logger: logCfg})
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("初始化 GORM 连接失败：%w", err)
	}
	return db, nil
}

var defaultGORMLogger = gormlogger.New(stdlog.New(os.Stderr, "\r\n", stdlog.LstdFlags), gormlogger.Config{
	SlowThreshold:             200 * time.Millisecond,
	LogLevel:                  gormlogger.Warn,
	IgnoreRecordNotFoundError: true,
})

func applyPoolConfig(db *sql.DB, opts Options) {
	maxOpen := 1
	maxIdle := 1
	if opts.ReadOnly {
		maxOpen = DefaultReadOnlyConns
		maxIdle = DefaultReadOnlyConns
	}
	if opts.MaxOpenConns > 0 {
		maxOpen = opts.MaxOpenConns
	}
	if opts.MaxIdleConns > 0 {
		maxIdle = opts.MaxIdleConns
	}
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	if opts.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(opts.ConnMaxLifetime)
	}
	if opts.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(opts.ConnMaxIdleTime)
	}
}

// ErrorCode 提取底层 SQLite 驱动的扩展错误码（例如 2067 表示 UNIQUE 约束冲突）。
func ErrorCode(err error) (int, bool) {
	var sqliteErr *sqlitedriver.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code(), true
	}
	return 0, false
}

// SchemaObject 是 sqlite_master 中的表、显式索引、触发器或视图（SQL 已规范化）。
type SchemaObject struct {
	Type  string
	Name  string
	Table string
	SQL   string
}

const sqlSelectSchemaObjects = `
SELECT type, name, tbl_name, sql
FROM sqlite_master
WHERE type IN ('table', 'index', 'trigger', 'view')
  AND sql IS NOT NULL
  AND name NOT LIKE 'sqlite_%'`

// NormalizeSchemaSQL 规范化建表与建索引语句：去掉 -- 注释，引号外的空白全部去掉、ASCII 字母统一小写。
// 仅调整注释、缩进、换行、括号与逗号周围空格或关键字大小写不影响结果。
func NormalizeSchemaSQL(text string) string {
	var out strings.Builder
	quote := byte(0)
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			out.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '\'' || c == '"':
			quote = c
			out.WriteByte(c)
		case c == '-' && i+1 < len(text) && text[i+1] == '-':
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		case c >= 'A' && c <= 'Z':
			out.WriteByte(c + ('a' - 'A'))
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

type cachedSchema struct {
	objects map[string]SchemaObject
	hash    string
}

var expectedSchemaCache sync.Map // string(schemaSQL) -> cachedSchema

// SchemaHash 对 schemaSQL 载入内存库后规范化的表与索引语句计算确定性 SHA-256 哈希。
// 与启动校验采用完全相同的口径：纯格式调整不改变哈希，任何语义变更都会改变哈希。
func SchemaHash(schemaSQL string) (string, error) {
	parsed, err := parseExpectedSchema(schemaSQL)
	if err != nil {
		return "", err
	}
	return parsed.hash, nil
}

func parseExpectedSchema(schemaSQL string) (cachedSchema, error) {
	if strings.TrimSpace(schemaSQL) == "" {
		return cachedSchema{}, errors.New("schema SQL 不能为空")
	}
	if cached, ok := expectedSchemaCache.Load(schemaSQL); ok {
		return cached.(cachedSchema), nil
	}
	db, err := sql.Open(DriverName, ":memory:")
	if err != nil {
		return cachedSchema{}, fmt.Errorf("打开 schema 内存参考库失败：%w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(schemaSQL); err != nil {
		return cachedSchema{}, fmt.Errorf("载入 schema 到内存参考库失败：%w", err)
	}
	objects, err := readSchemaObjects(context.Background(), db)
	if err != nil {
		return cachedSchema{}, err
	}
	if len(objects) == 0 {
		return cachedSchema{}, errors.New("schema 未定义任何表或索引")
	}
	entry := cachedSchema{
		objects: objects,
		hash:    hashSchemaObjects(objects),
	}
	expectedSchemaCache.Store(schemaSQL, entry)
	return entry, nil
}

func hashSchemaObjects(objects map[string]SchemaObject) string {
	keys := make([]string, 0, len(objects))
	for k := range objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		obj := objects[k]
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", obj.Type, obj.Name, obj.Table, obj.SQL)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func readSchemaObjects(ctx context.Context, db *sql.DB) (map[string]SchemaObject, error) {
	rows, err := db.QueryContext(ctx, sqlSelectSchemaObjects)
	if err != nil {
		return nil, fmt.Errorf("读取 sqlite_master 失败：%w", err)
	}
	defer rows.Close()
	byKey := make(map[string]SchemaObject)
	for rows.Next() {
		var obj SchemaObject
		if err := rows.Scan(&obj.Type, &obj.Name, &obj.Table, &obj.SQL); err != nil {
			return nil, fmt.Errorf("扫描 sqlite_master 失败：%w", err)
		}
		obj.SQL = NormalizeSchemaSQL(obj.SQL)
		byKey[obj.Type+"\x00"+obj.Name] = obj
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 sqlite_master 失败：%w", err)
	}
	return byKey, nil
}

// ValidateSQL 校验目标数据库中的全部表与索引是否与 schemaSQL 一致。
// allowMissing 为真时允许空库或缺少当前版本新增的对象（用于应用 schema 之前的前置校验），但已有对象必须逐字等价且不允许存在多余对象。
// allowMissing 为假时要求目标库对象与参考 schema 完全一致。
func ValidateSQL(ctx context.Context, db *sql.DB, component, schemaSQL string, allowMissing bool) error {
	if db == nil {
		return fmt.Errorf("%s 数据库未打开", component)
	}
	expected, err := parseExpectedSchema(schemaSQL)
	if err != nil {
		return err
	}
	actual, err := readSchemaObjects(ctx, db)
	if err != nil {
		return err
	}
	return compareSchemaObjects(component, expected.objects, actual, allowMissing, false)
}

// ValidateTablesSQL 仅校验 schemaSQL 中声明的那组对象（忽略库里其他动态表与索引），供归档 worker 暂存库校验固定表使用。
func ValidateTablesSQL(ctx context.Context, db *sql.DB, component, schemaSQL string, allowMissing bool) error {
	if db == nil {
		return fmt.Errorf("%s 数据库未打开", component)
	}
	expected, err := parseExpectedSchema(schemaSQL)
	if err != nil {
		return err
	}
	actual, err := readSchemaObjects(ctx, db)
	if err != nil {
		return err
	}
	return compareSchemaObjects(component, expected.objects, actual, allowMissing, true)
}

func compareSchemaObjects(component string, expected, actual map[string]SchemaObject, allowMissing, ignoreExtra bool) error {
	if !ignoreExtra {
		extraKeys := make([]string, 0)
		for key := range actual {
			if _, ok := expected[key]; !ok {
				extraKeys = append(extraKeys, key)
			}
		}
		sort.Strings(extraKeys)
		if len(extraKeys) > 0 {
			obj := actual[extraKeys[0]]
			return fmt.Errorf("%w：%s 数据库存在当前版本不包含的%s %s；请备份后执行全新部署重建数据库",
				ErrIncompatibleSchema, component, objectKindLabel(obj.Type), obj.Name)
		}
	}
	if allowMissing && len(actual) == 0 {
		return nil
	}
	keys := make([]string, 0, len(expected))
	for key := range expected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		want := expected[key]
		got, ok := actual[key]
		if !ok {
			if allowMissing {
				continue
			}
			return fmt.Errorf("%w：%s 数据库缺少%s %s；请备份后执行全新部署重建数据库",
				ErrIncompatibleSchema, component, objectKindLabel(want.Type), want.Name)
		}
		if got.SQL != want.SQL {
			return fmt.Errorf("%w：%s 数据库%s %s 的定义与当前版本不一致；请备份后执行全新部署重建数据库",
				ErrIncompatibleSchema, component, objectKindLabel(want.Type), want.Name)
		}
	}
	return nil
}

func objectKindLabel(kind string) string {
	switch kind {
	case "table":
		return "表"
	case "index":
		return "索引"
	case "trigger":
		return "触发器"
	case "view":
		return "视图"
	default:
		return kind
	}
}

// ApplySchemaSQL 先校验已有对象兼容性，再在单个事务中执行 schemaSQL，最后严格比对完整表与索引结构。
func ApplySchemaSQL(ctx context.Context, db *sql.DB, component, schemaSQL string) error {
	if db == nil {
		return fmt.Errorf("%s 数据库未打开", component)
	}
	if strings.TrimSpace(schemaSQL) == "" {
		return fmt.Errorf("%s schema SQL 不能为空", component)
	}
	if err := ValidateSQL(ctx, db, component, schemaSQL, true); err != nil {
		return err
	}
	return applySchemaSQLValidated(ctx, db, component, schemaSQL)
}

func applySchemaSQLValidated(ctx context.Context, db *sql.DB, component, schemaSQL string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启 %s schema 事务失败：%w", component, err)
	}
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("执行 %s schema 失败：%w", component, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 %s schema 失败：%w", component, err)
	}
	return ValidateSQL(ctx, db, component, schemaSQL, false)
}

// EnsureSchemaSQL 先按已有库对象与已有 data-format 文件做只读前置核对，再应用并严格校验 schema，最后写入数据库同目录的 data-format 文件。
// extraDataPaths 传入组件除 SQLite 库文件之外的其他持久化状态文件路径（如该组件实际的 lifecycle.json 路径），用于判断“是否已有本地数据”。
// 已有数据、已有格式文件且格式值（如新增表或 lifecycle 版本常量变化）不同时，会在建表事务执行前直接拒绝启动，不改动已有库。
// 内存库或 component 为空时不核对也不写格式文件。
func EnsureSchemaSQL(ctx context.Context, db *sql.DB, dbPath, component, schemaSQL string, extraParts map[string]string, extraDataPaths ...string) error {
	if db == nil {
		return fmt.Errorf("%s 数据库未打开", component)
	}
	if strings.TrimSpace(schemaSQL) == "" {
		return fmt.Errorf("%s schema SQL 不能为空", component)
	}
	hadData, err := hasExistingSQLiteData(ctx, db, extraDataPaths...)
	if err != nil {
		return err
	}
	if err := ValidateSQL(ctx, db, component, schemaSQL, true); err != nil {
		return err
	}
	if err := verifyExistingFormat(dbPath, component, schemaSQL, extraParts, hadData); err != nil {
		return err
	}
	if err := applySchemaSQLValidated(ctx, db, component, schemaSQL); err != nil {
		return err
	}
	return recordFormat(dbPath, component, schemaSQL, extraParts, hadData)
}

func hasExistingSQLiteData(ctx context.Context, db *sql.DB, extraDataPaths ...string) (bool, error) {
	if db == nil {
		return false, nil
	}
	objects, err := readSchemaObjects(ctx, db)
	if err != nil {
		return false, err
	}
	if len(objects) > 0 {
		return true, nil
	}
	for _, path := range extraDataPaths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		if info, err := os.Stat(trimmed); err == nil && !info.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

func verifyExistingFormat(dbPath, component, schemaSQL string, extraParts map[string]string, hasExistingData bool) error {
	if !hasExistingData || strings.TrimSpace(component) == "" || isLocalPathSkipped(dbPath) {
		return nil
	}
	rec, err := FormatRecord(component, schemaSQL, extraParts)
	if err != nil {
		return err
	}
	return dataformat.VerifyExistingFile(filepath.Join(filepath.Dir(dbPath), dataformat.FileName), rec)
}

func isLocalPathSkipped(dbPath string) bool {
	trimmed := strings.TrimSpace(dbPath)
	return trimmed == "" || IsMemoryPath(trimmed) || strings.HasPrefix(trimmed, "file:")
}

// EnsureSchemaGORM 是 EnsureSchemaSQL 的 GORM 封装。
func EnsureSchemaGORM(db *gorm.DB, dbPath, component, schemaSQL string, extraParts map[string]string, extraDataPaths ...string) error {
	if db == nil {
		return fmt.Errorf("%s 数据库未打开", component)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("获取 %s 底层数据库连接失败：%w", component, err)
	}
	return EnsureSchemaSQL(context.Background(), sqlDB, dbPath, component, schemaSQL, extraParts, extraDataPaths...)
}

// ValidateGORM 是 ValidateSQL 的 GORM 封装。
func ValidateGORM(db *gorm.DB, component, schemaSQL string, allowMissing bool) error {
	if db == nil {
		return fmt.Errorf("%s 数据库未打开", component)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("获取 %s 底层数据库连接失败：%w", component, err)
	}
	return ValidateSQL(context.Background(), sqlDB, component, schemaSQL, allowMissing)
}

// FormatRecord 计算基于 SQLite schema（加可选附加部分）的组件格式记录。
func FormatRecord(component, schemaSQL string, extraParts map[string]string) (dataformat.Record, error) {
	hash, err := SchemaHash(schemaSQL)
	if err != nil {
		return dataformat.Record{}, err
	}
	parts := make(map[string]string, len(extraParts)+1)
	parts[dataformat.PartSQLiteSchema] = hash
	for k, v := range extraParts {
		parts[k] = v
	}
	return dataformat.Build(component, parts)
}

func recordFormat(dbPath, component, schemaSQL string, extraParts map[string]string, hasExistingData bool) error {
	if strings.TrimSpace(component) == "" || isLocalPathSkipped(dbPath) {
		return nil
	}
	return RecordFormatAt(filepath.Join(filepath.Dir(dbPath), dataformat.FileName), component, schemaSQL, extraParts, hasExistingData)
}

// RecordFormatAt 核对并把格式记录写入指定的格式文件路径（用于库文件位于数据目录子目录的组件，如 storage-primary）。
// hasExistingData 为真且格式文件已存在但不一致时拒绝启动并提示全新部署。
func RecordFormatAt(formatPath, component, schemaSQL string, extraParts map[string]string, hasExistingData bool) error {
	if strings.TrimSpace(component) == "" || strings.TrimSpace(formatPath) == "" {
		return nil
	}
	rec, err := FormatRecord(component, schemaSQL, extraParts)
	if err != nil {
		return err
	}
	return dataformat.VerifyOrWriteFile(formatPath, rec, hasExistingData)
}
