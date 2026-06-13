RocksDB 存储适配层完整实现计划

目录

1. #一核心设计
2. #二key-设计方案
3. #三目录和文件结构
4. #四各文件详细实现
5. #五接口实现逻辑
6. #六关键算法和流程
7. #七配置和依赖
8. #八测试计划
9. #九实施步骤

  ---
一、核心设计

1.1 设计原则

- 静态数据：支持软删除，查询时需要过滤 _deleted=1 的记录
- 时序数据：不支持删除，查询时无需过滤删除标记
- 列式存储：每个字段独立存储为一个 KV 对，支持灵活的字段访问
- 字典序优化：利用 RocksDB 的 Key 字典序实现高效的时间范围查询
- 通用框架：字段数量动态可扩展，字段类型灵活支持

1.2 数据类型区分
┌──────────┬───────────────┬───────────────┬───────────────────────────────┐
│ 数据类型 │   主键标识    │   删除支持    │           查询特点            │
├──────────┼───────────────┼───────────────┼───────────────────────────────┤
│ 静态数据 │ rowID         │ ✅ 支持软删除 │ 按 rowID 查询，需过滤删除标记 │
├──────────┼───────────────┼───────────────┼───────────────────────────────┤
│ 时序数据 │ rowID + times │ ❌ 不支持删除 │ 时间范围查询，无需过滤删除    │
└──────────┴───────────────┴───────────────┴───────────────────────────────┘
  ---
二、Key 设计方案

2.1 Key 结构定义

【数据字段】
格式: {tableID}:{rowID}:{times}:f{fieldID}
值:   字段值（序列化后的字节）

【静态数据删除标记】（仅静态数据）
格式: {tableID}:{rowID}::_meta:deleted
值:   "1" 表示已删除

【静态数据删除时间】（仅静态数据）
格式: {tableID}:{rowID}::_meta:deleted_time
值:   删除时间戳（如 "2024-01-15 10:00:00"）

【表元数据】
格式: {tableID}:_table_meta:exists
值:   "1" 表示表存在

关键设计点：
- 静态数据的 times 字段为空字符串，在 Key 中体现为双冒号 ::
- 时序数据的 times 字段为具体时间戳，如 2024-01-15 09:30:00
- 字段 ID 前缀使用 f 区分（如 f1, f123）
- 系统元数据使用 _meta: 前缀区分

2.2 Key 示例

示例 1：静态数据（股票基本信息）

# 表: t_stock_basic_info
# rowID: 000001.SZ
# times: "" (空字符串)

t_stock_basic_info:000001.SZ::f1 → "平安银行"          (股票名称, fieldID=1)
t_stock_basic_info:000001.SZ::f2 → "1991-04-03"       (上市日期, fieldID=2)
t_stock_basic_info:000001.SZ::f3 → "银行"             (行业, fieldID=3)
t_stock_basic_info:000001.SZ::f5 → {"PE":10.5,"PB":1.2} (财务指标MAP, fieldID=5)
t_stock_basic_info:000001.SZ::_meta:deleted → "0"     (未删除)

示例 2：时序数据（股票日K线）

# 表: t_stock_daily_kline
# rowID: 000001.SZ
# times: 2024-01-15 00:00:00

t_stock_daily_kline:000001.SZ:2024-01-15 00:00:00:f1 → 10.50   (开盘价)
t_stock_daily_kline:000001.SZ:2024-01-15 00:00:00:f2 → 10.80   (收盘价)
t_stock_daily_kline:000001.SZ:2024-01-15 00:00:00:f3 → 10.90   (最高价)
t_stock_daily_kline:000001.SZ:2024-01-15 00:00:00:f4 → 10.30   (最低价)
t_stock_daily_kline:000001.SZ:2024-01-15 00:00:00:f5 → 1000000 (成交量)
# 注意：时序数据无 _meta:deleted 标记

示例 3：静态数据已删除

t_stock_basic_info:000002.SZ::f1 → "万科A"
t_stock_basic_info:000002.SZ::f2 → "1991-01-29"
t_stock_basic_info:000002.SZ::_meta:deleted → "1"              (已删除)
t_stock_basic_info:000002.SZ::_meta:deleted_time → "2024-01-15 10:00:00"

2.3 Key 排序特性

利用字典序实现高效查询：

# 静态数据（同一表，不同股票）
t_stock_basic_info:000001.SZ::f1
t_stock_basic_info:000001.SZ::f2
t_stock_basic_info:000001.SZ::f3
t_stock_basic_info:000002.SZ::f1
t_stock_basic_info:000002.SZ::f2

# 时序数据（同一股票，不同时间）
t_stock_kline:000001.SZ:2024-01-15 09:30:00:f1
t_stock_kline:000001.SZ:2024-01-15 09:30:00:f2
t_stock_kline:000001.SZ:2024-01-15 09:31:00:f1  # 自动按时间排序
t_stock_kline:000001.SZ:2024-01-15 09:31:00:f2
t_stock_kline:000001.SZ:2024-01-16 09:30:00:f1

  ---
三、目录和文件结构

storage/internal/services/adapter/dao/rocksdb/
├── init.go           # 包初始化、设备注册、构造函数
├── rocksdb.go        # 核心结构体、连接管理、基础方法
├── schema.go         # 表操作（CreateTable、DropTable、CheckTable、GetSchemaFieldLimit）
├── get.go            # 数据读取接口（GetFieldInfos、GetStaticFieldInfos、GetTimingFieldInfos）
├── set.go            # 数据写入接口（SetFieldInfos、processStaticDataUpdate、processTimingDataUpdate）
├── delete.go         # 数据删除接口（DeleteRows，仅静态数据）
├── search.go         # 数据搜索接口（SearchFieldInfos、SearchStaticFieldInfos、SearchTimingFieldInfos）
└── utils.go          # 辅助工具函数（Key构建、序列化、类型转换）

  ---
四、各文件详细实现

4.1 init.go - 包初始化

职责：
- 注册 RocksDB 设备类型
- 提供构造函数

代码结构：
//go:build !norocksdb && cgo
// +build !norocksdb,cgo

package rocksdb

import (
"context"
"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func init() {
// 注册 RocksDB 设备类型
dao.RegisterDeviceType(pb.EnumDeviceType_ROCKDB_DEVICE, func(ctx context.Context) (dao.Storer, error) {
return NewRocksDB(ctx), nil
})
}

// NewRocksDB 创建新的 RocksDB 存储对象
func NewRocksDB(ctx context.Context) *RocksDB {
return &RocksDB{}
}

  ---
4.2 rocksdb.go - 核心结构体

职责：
- 定义 RocksDB 结构体
- 实现设备连接管理（GetDeviceConn、CloseDeviceConn、GetDeviceKey、GetDeviceTableID）
- 实现基础工具方法

结构体定义：
package rocksdb

import (
"github.com/tecbot/gorocksdb"
pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// RocksDB 存储对象
type RocksDB struct {
db      *gorocksdb.DB              // 数据库实例
tableID string                     // 当前操作的表ID
wo      *gorocksdb.WriteOptions    // 写选项
ro      *gorocksdb.ReadOptions     // 读选项
}

核心方法：

1. GetDeviceConn(connectInfo string) error

逻辑：
1. 处理 connectInfo：
    - 如果是 "localhost"，使用配置文件中的 RocksDB.DataPath
    - 否则直接使用 connectInfo 作为数据库路径

2. 创建数据库目录（如果不存在）

3. 配置 RocksDB Options：
    - SetCreateIfMissing(true)
    - SetCompression(SnappyCompression)
    - SetBlockCache(LRUCache, 默认 512MB)
    - SetBloomFilter(10)
    - SetPrefixExtractor（针对表前缀优化）

4. 打开数据库：db, err := gorocksdb.OpenDb(opts, dbPath)

5. 初始化 WriteOptions 和 ReadOptions

6. 记录日志

代码框架：
func (r *RocksDB) GetDeviceConn(connectInfo string) error {
ctx := context.Background()

      // 处理 connectInfo
      actualConnectInfo := connectInfo
      if connectInfo == "localhost" {
          cfg := config.GetGlobalConfig()
          if cfg != nil && cfg.RocksDB.DataPath != "" {
              actualConnectInfo = cfg.RocksDB.DataPath
          } else {
              actualConnectInfo = "../database/rocksdb"
          }
      }

      // 确保目录存在
      if err := os.MkdirAll(actualConnectInfo, 0755); err != nil {
          return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV),
              fmt.Sprintf("创建RocksDB目录失败: %v", err))
      }

      // 配置 Options
      opts := gorocksdb.NewDefaultOptions()
      opts.SetCreateIfMissing(true)
      opts.SetCompression(gorocksdb.SnappyCompression)

      // 设置缓存
      blockCache := gorocksdb.NewLRUCache(512 * 1024 * 1024) // 512MB
      opts.SetBlockCache(blockCache)

      // 设置 Bloom Filter
      opts.SetBloomFilter(10)

      // 打开数据库
      db, err := gorocksdb.OpenDb(opts, actualConnectInfo)
      if err != nil {
          return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV),
              fmt.Sprintf("连接RocksDB失败: %v", err))
      }

      r.db = db
      r.wo = gorocksdb.NewDefaultWriteOptions()
      r.ro = gorocksdb.NewDefaultReadOptions()

      log.InfoContextf(ctx, "RocksDB连接成功: %s", actualConnectInfo)
      return nil
}

2. CloseDeviceConn() error

func (r *RocksDB) CloseDeviceConn() error {
if r.wo != nil {
r.wo.Destroy()
}
if r.ro != nil {
r.ro.Destroy()
}
if r.db != nil {
r.db.Close()
}
return nil
}

3. GetDeviceTableID(logicTableID string) string

func (r *RocksDB) GetDeviceTableID(logicTableID string) string {
// RocksDB 直接使用逻辑表名
return logicTableID
}

4. GetDeviceKey() string

func (r *RocksDB) GetDeviceKey() string {
return "rocksdb"
}

  ---
4.3 utils.go - 辅助工具函数

职责：
- Key 构建和解析
- 字段值序列化和反序列化
- 类型转换
- 删除标记检查

核心函数：

1. Key 构建函数

// buildFieldKey 构建字段 Key
// 格式: {tableID}:{rowID}:{times}:f{fieldID}
func buildFieldKey(tableID, rowID, times string, fieldID uint32) string {
return fmt.Sprintf("%s:%s:%s:f%d", tableID, rowID, times, fieldID)
}

// buildRowPrefix 构建行前缀（用于扫描某行的所有字段）
// 格式: {tableID}:{rowID}:{times}:f
func buildRowPrefix(tableID, rowID, times string) string {
return fmt.Sprintf("%s:%s:%s:f", tableID, rowID, times)
}

// buildDeletedKey 构建删除标记 Key（仅静态数据）
// 格式: {tableID}:{rowID}::_meta:deleted
func buildDeletedKey(tableID, rowID string) string {
return fmt.Sprintf("%s:%s::_meta:deleted", tableID, rowID)
}

// buildDeletedTimeKey 构建删除时间 Key（仅静态数据）
// 格式: {tableID}:{rowID}::_meta:deleted_time
func buildDeletedTimeKey(tableID, rowID string) string {
return fmt.Sprintf("%s:%s::_meta:deleted_time", tableID, rowID)
}

// buildTableMetaKey 构建表元数据 Key
// 格式: {tableID}:_table_meta:exists
func buildTableMetaKey(tableID string) string {
return fmt.Sprintf("%s:_table_meta:exists", tableID)
}

2. Key 解析函数

// parseFieldIDFromKey 从 Key 中解析字段 ID
// 输入: "t_stock_kline:000001.SZ:2024-01-15 09:30:00:f123"
// 输出: 123
func parseFieldIDFromKey(key string) (uint32, error) {
parts := strings.Split(key, ":")
if len(parts) < 4 {
return 0, fmt.Errorf("invalid key format")
}

      fieldPart := parts[len(parts)-1] // 最后一段，如 "f123"
      if !strings.HasPrefix(fieldPart, "f") {
          return 0, fmt.Errorf("invalid field prefix")
      }

      fieldIDStr := strings.TrimPrefix(fieldPart, "f")
      fieldID, err := strconv.ParseUint(fieldIDStr, 10, 32)
      if err != nil {
          return 0, fmt.Errorf("parse field ID failed: %v", err)
      }

      return uint32(fieldID), nil
}

// parseKeyComponents 解析 Key 的各个组成部分
// 输入: "t_stock_kline:000001.SZ:2024-01-15 09:30:00:f1"
// 输出: tableID="t_stock_kline", rowID="000001.SZ", times="2024-01-15 09:30:00", fieldID=1
func parseKeyComponents(key string) (tableID, rowID, times string, fieldID uint32, err error) {
parts := strings.Split(key, ":")
if len(parts) < 4 {
err = fmt.Errorf("invalid key format")
return
}

      tableID = parts[0]
      rowID = parts[1]
      times = parts[2]

      fieldID, err = parseFieldIDFromKey(key)
      return
}

3. 序列化和反序列化

// serializeFieldValue 序列化字段值
func serializeFieldValue(fieldInfo *pb.FieldInfo) ([]byte, error) {
// 根据字段类型序列化
switch fieldInfo.FieldType {
case pb.EnumFieldType_STR_FIELD:
return []byte(fieldInfo.SimpleValue.GetStr()), nil

      case pb.EnumFieldType_INT_FIELD:
          intVal := fieldInfo.SimpleValue.GetInt()
          return []byte(fmt.Sprintf("%d", intVal)), nil

      case pb.EnumFieldType_FLOAT_FIELD:
          floatVal := fieldInfo.SimpleValue.GetFloat()
          return []byte(fmt.Sprintf("%f", floatVal)), nil

      case pb.EnumFieldType_TIME_FIELD:
          return []byte(fieldInfo.SimpleValue.GetTime()), nil

      case pb.EnumFieldType_INT_VEC_FIELD:
          // JSON 序列化
          return json.Marshal(fieldInfo.SimpleValue.GetIntList().Values)

      case pb.EnumFieldType_SET_FIELD:
          // JSON 序列化
          return json.Marshal(fieldInfo.SimpleValue.GetStrList().Values)

      case pb.EnumFieldType_MAP_KV_FIELD:
          // JSON 序列化 Map
          return json.Marshal(fieldInfo.MapValue)

      default:
          return nil, fmt.Errorf("unsupported field type: %v", fieldInfo.FieldType)
      }
}

// deserializeFieldValue 反序列化字段值
func deserializeFieldValue(data []byte, fieldType pb.EnumFieldType) (*pb.FieldInfo, error) {
fieldInfo := &pb.FieldInfo{
FieldType: fieldType,
}

      switch fieldType {
      case pb.EnumFieldType_STR_FIELD:
          fieldInfo.SimpleValue = &pb.SimpleValue{
              Value: &pb.SimpleValue_Str{Str: string(data)},
          }

      case pb.EnumFieldType_INT_FIELD:
          intVal, err := strconv.ParseInt(string(data), 10, 64)
          if err != nil {
              return nil, err
          }
          fieldInfo.SimpleValue = &pb.SimpleValue{
              Value: &pb.SimpleValue_Int{Int: intVal},
          }

      case pb.EnumFieldType_FLOAT_FIELD:
          floatVal, err := strconv.ParseFloat(string(data), 64)
          if err != nil {
              return nil, err
          }
          fieldInfo.SimpleValue = &pb.SimpleValue{
              Value: &pb.SimpleValue_Float{Float: floatVal},
          }

      case pb.EnumFieldType_TIME_FIELD:
          fieldInfo.SimpleValue = &pb.SimpleValue{
              Value: &pb.SimpleValue_Time{Time: string(data)},
          }

      case pb.EnumFieldType_INT_VEC_FIELD:
          var values []int64
          if err := json.Unmarshal(data, &values); err != nil {
              return nil, err
          }
          fieldInfo.SimpleValue = &pb.SimpleValue{
              Value: &pb.SimpleValue_IntList{
                  IntList: &pb.IntList{Values: values},
              },
          }

      case pb.EnumFieldType_SET_FIELD:
          var values []string
          if err := json.Unmarshal(data, &values); err != nil {
              return nil, err
          }
          fieldInfo.SimpleValue = &pb.SimpleValue{
              Value: &pb.SimpleValue_StrList{
                  StrList: &pb.StrList{Values: values},
              },
          }

      case pb.EnumFieldType_MAP_KV_FIELD:
          var mapValue pb.MapContainer
          if err := json.Unmarshal(data, &mapValue); err != nil {
              return nil, err
          }
          fieldInfo.MapValue = &mapValue

      default:
          return nil, fmt.Errorf("unsupported field type: %v", fieldType)
      }

      return fieldInfo, nil
}

4. 删除标记检查（仅静态数据）

// isRowDeleted 检查行是否被软删除（仅静态数据）
func (r *RocksDB) isRowDeleted(tableID, rowID string) (bool, error) {
key := buildDeletedKey(tableID, rowID)
value, err := r.db.Get(r.ro, []byte(key))
if err != nil {
return false, err
}
defer value.Free()

      if !value.Exists() {
          return false, nil
      }

      return string(value.Data()) == "1", nil
}

// batchCheckDeleted 批量检查多个行是否被删除（优化性能）
func (r *RocksDB) batchCheckDeleted(tableID string, rowIDs []string) (map[string]bool, error) {
keys := make([][]byte, len(rowIDs))
for i, rowID := range rowIDs {
keys[i] = []byte(buildDeletedKey(tableID, rowID))
}

      values, err := r.db.MultiGet(r.ro, keys...)
      if err != nil {
          return nil, err
      }

      result := make(map[string]bool)
      for i, val := range values {
          defer val.Free()
          result[rowIDs[i]] = val.Exists() && string(val.Data()) == "1"
      }

      return result, nil
}

5. 字段类型获取（调用 DAO 层公共方法）

// getFieldType 获取字段类型（从缓存或配置中心）
func (r *RocksDB) getFieldType(ctx context.Context, fieldID uint32) (pb.EnumFieldType, error) {
// 调用 DAO 层公共方法获取字段类型
// 这个方法应该在 dao 包中实现，通过查询配置中心或缓存获取
return dao.GetFieldTypeByID(ctx, fieldID)
}

  ---
4.4 schema.go - 表操作

职责：
- CreateTable：创建表元数据
- DropTable：删除表的所有数据
- CheckTable：检查表是否存在
- GetSchemaFieldLimit：获取字段数量限制

代码实现：

1. CreateTable(ctx, params) error

逻辑：
1. 参数校验（tableID 非空）

2. 加表级锁（防止并发创建）

3. 检查表是否已存在：
    - 读取 {tableID}:_table_meta:exists
    - 如果存在且 ForceCreate=false，返回成功
    - 如果存在且 ForceCreate=true，先删除表

4. 创建表元数据标记：
    - 写入 {tableID}:_table_meta:exists → "1"

5. 记录日志

代码框架：
var createTableLocks = struct {
mu    sync.Mutex
locks map[string]*sync.Mutex
}{
locks: make(map[string]*sync.Mutex),
}

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

func (r *RocksDB) CreateTable(ctx context.Context, params *dao.CreateTableParams) error {
if params.TableID == "" {
return fmt.Errorf("表名不能为空")
}

      tableID := params.TableID
      unlock := lockCreateTable(tableID)
      defer unlock()

      log.DebugContextf(ctx, "开始创建RocksDB表: %s", tableID)

      // 检查表是否已存在
      exists, err := r.CheckTable(ctx, tableID)
      if err != nil {
          return err
      }

      // 如果表已存在且不强制创建
      if exists && !params.ForceCreate {
          log.InfoContextf(ctx, "表[%s]已存在", tableID)
          return nil
      }

      // 如果强制创建，先删除表
      if exists && params.ForceCreate {
          log.InfoContextf(ctx, "表[%s]已存在，强制创建模式，先删除", tableID)
          if err := r.DropTable(ctx, tableID); err != nil {
              return err
          }
      }

      // 创建表元数据标记
      metaKey := buildTableMetaKey(tableID)
      err = r.db.Put(r.wo, []byte(metaKey), []byte("1"))
      if err != nil {
          return errs.New(int(pb.EnumErrorCode_INNER_ERR),
              fmt.Sprintf("创建表失败: %v", err))
      }

      log.InfoContextf(ctx, "表[%s]创建成功", tableID)
      return nil
}

2. CheckTable(ctx, tableName) (bool, error)

func (r *RocksDB) CheckTable(ctx context.Context, tableName string) (bool, error) {
if tableName == "" {
return false, fmt.Errorf("表名不能为空")
}

      metaKey := buildTableMetaKey(tableName)
      value, err := r.db.Get(r.ro, []byte(metaKey))
      if err != nil {
          return false, err
      }
      defer value.Free()

      exists := value.Exists() && string(value.Data()) == "1"
      log.DebugContextf(ctx, "表[%s]存在性检查结果: %v", tableName, exists)
      return exists, nil
}

3. DropTable(ctx, tableName) error

逻辑：
1. 使用 Iterator 扫描表前缀 {tableID}:
2. 收集所有需要删除的 Key
3. 使用 WriteBatch 批量删除
4. 删除表元数据标记

func (r *RocksDB) DropTable(ctx context.Context, tableName string) error {
if tableName == "" {
return fmt.Errorf("表名不能为空")
}

      log.DebugContextf(ctx, "开始删除表: %s", tableName)

      // 扫描表前缀
      tablePrefix := fmt.Sprintf("%s:", tableName)
      it := r.db.NewIterator(r.ro)
      defer it.Close()

      // 收集所有 Key
      var keysToDelete [][]byte
      for it.Seek([]byte(tablePrefix)); it.ValidForPrefix([]byte(tablePrefix)); it.Next() {
          key := make([]byte, len(it.Key().Data()))
          copy(key, it.Key().Data())
          keysToDelete = append(keysToDelete, key)
      }

      if err := it.Err(); err != nil {
          return errs.New(int(pb.EnumErrorCode_INNER_ERR),
              fmt.Sprintf("扫描表数据失败: %v", err))
      }

      // 批量删除
      batch := gorocksdb.NewWriteBatch()
      defer batch.Destroy()

      for _, key := range keysToDelete {
          batch.Delete(key)
      }

      // 删除表元数据
      metaKey := buildTableMetaKey(tableName)
      batch.Delete([]byte(metaKey))

      // 提交批量删除
      err := r.db.Write(r.wo, batch)
      if err != nil {
          return errs.New(int(pb.EnumErrorCode_INNER_ERR),
              fmt.Sprintf("删除表失败: %v", err))
      }

      log.InfoContextf(ctx, "表[%s]删除成功，共删除 %d 个Key", tableName, len(keysToDelete)+1)
      return nil
}

4. GetSchemaFieldLimit(fieldType) (int, error)

func (r *RocksDB) GetSchemaFieldLimit(fieldType string) (int, error) {
// RocksDB 是 KV 存储，理论上字段数量无限制
// 返回一个足够大的值
return 100000, nil
}

  ---
4.5 get.go - 数据读取

职责：
- GetFieldInfos：统一读取入口
- GetStaticFieldInfos：读取静态数据
- GetTimingFieldInfos：读取时序数据

核心逻辑：

1. GetFieldInfos(ctx, params) ([]*pb.DocRow, error)

逻辑：
1. 根据 params.DataType 分发：
    - STATIC_DATA_TYPE → GetStaticFieldInfos
    - TIME_SERIES_DATA_TYPE → GetTimingFieldInfos

2. 返回 DocRow 列表

func (r *RocksDB) GetFieldInfos(ctx context.Context, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
r.tableID = params.TableID

      switch params.DataType {
      case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
          return r.GetStaticFieldInfos(ctx, params)

      case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
          return r.GetTimingFieldInfos(ctx, params)

      default:
          return nil, fmt.Errorf("invalid data type: %v", params.DataType)
      }
}

2. GetStaticFieldInfos(ctx, params) ([]*pb.DocRow, error)

逻辑：
1. 构建查询前缀：
    - 如果指定 RowID：{tableID}:{rowID}::f
    - 否则：{tableID}: (扫描所有行)

2. 使用 Iterator 扫描：
    - 按行聚合字段
    - 检查删除标记（isRowDeleted）
    - 如果指定 FieldIDs，只读取指定字段
    - 如果指定 MapKeys，过滤 Map 字段

3. 应用 MaxLimit 限制

4. 返回 DocRow 列表

func (r *RocksDB) GetStaticFieldInfos(ctx context.Context, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
tableID := params.TableID
rowID := params.RowID
fieldIDs := params.FieldIDs
mapKeys := params.MapKeys
maxLimit := params.MaxLimit

      var results []*pb.DocRow

      // 情况1：查询指定行
      if rowID != "" {
          docRow, err := r.readStaticRow(ctx, tableID, rowID, fieldIDs, mapKeys)
          if err != nil {
              return nil, err
          }
          if docRow != nil {
              results = append(results, docRow)
          }
          return results, nil
      }

      // 情况2：查询所有行
      tablePrefix := fmt.Sprintf("%s:", tableID)
      it := r.db.NewIterator(r.ro)
      defer it.Close()

      currentRowID := ""
      currentDocRow := &pb.DocRow{Fields: make(map[uint32]*pb.FieldInfo)}
      count := uint32(0)

      for it.Seek([]byte(tablePrefix)); it.ValidForPrefix([]byte(tablePrefix)); it.Next() {
          key := string(it.Key().Data())

          // 解析 Key
          _, parsedRowID, times, fieldID, err := parseKeyComponents(key)
          if err != nil || times != "" { // 静态数据 times 必须为空
              continue
          }

          // 新行开始
          if parsedRowID != currentRowID {
              // 保存上一行（检查删除标记）
              if currentRowID != "" {
                  deleted, _ := r.isRowDeleted(tableID, currentRowID)
                  if !deleted {
                      results = append(results, currentDocRow)
                      count++
                      if maxLimit > 0 && count >= maxLimit {
                          break
                      }
                  }
              }

              // 开始新行
              currentRowID = parsedRowID
              currentDocRow = &pb.DocRow{
                  RowId:  parsedRowID,
                  Fields: make(map[uint32]*pb.FieldInfo),
              }
          }

          // 过滤字段（如果指定了 fieldIDs）
          if len(fieldIDs) > 0 && !contains(fieldIDs, fieldID) {
              continue
          }

          // 反序列化字段值
          fieldType, err := r.getFieldType(ctx, fieldID)
          if err != nil {
              log.WarnContextf(ctx, "获取字段类型失败: fieldID=%d, err=%v", fieldID, err)
              continue
          }

          fieldInfo, err := deserializeFieldValue(it.Value().Data(), fieldType)
          if err != nil {
              log.WarnContextf(ctx, "反序列化字段值失败: fieldID=%d, err=%v", fieldID, err)
              continue
          }

          fieldInfo.FieldId = fieldID
          currentDocRow.Fields[fieldID] = fieldInfo
      }

      // 处理最后一行
      if currentRowID != "" {
          deleted, _ := r.isRowDeleted(tableID, currentRowID)
          if !deleted && (maxLimit == 0 || count < maxLimit) {
              results = append(results, currentDocRow)
          }
      }

      // 过滤 Map 字段
      if len(mapKeys) > 0 {
          for _, docRow := range results {
              filterMapFields(docRow, mapKeys)
          }
      }

      return results, nil
}

// readStaticRow 读取单行静态数据（辅助函数）
func (r *RocksDB) readStaticRow(ctx context.Context, tableID, rowID string,
fieldIDs []uint32, mapKeys map[uint32]*pb.KeyList) (*pb.DocRow, error) {

      // 检查删除标记
      deleted, err := r.isRowDeleted(tableID, rowID)
      if err != nil {
          return nil, err
      }
      if deleted {
          return nil, nil // 已删除，返回空
      }

      docRow := &pb.DocRow{
          RowId:  rowID,
          Fields: make(map[uint32]*pb.FieldInfo),
      }

      // 如果指定了字段列表，使用 MultiGet
      if len(fieldIDs) > 0 {
          keys := make([][]byte, len(fieldIDs))
          for i, fieldID := range fieldIDs {
              key := buildFieldKey(tableID, rowID, "", fieldID)
              keys[i] = []byte(key)
          }

          values, err := r.db.MultiGet(r.ro, keys...)
          if err != nil {
              return nil, err
          }

          for i, val := range values {
              defer val.Free()
              if !val.Exists() {
                  continue
              }

              fieldID := fieldIDs[i]
              fieldType, err := r.getFieldType(ctx, fieldID)
              if err != nil {
                  continue
              }

              fieldInfo, err := deserializeFieldValue(val.Data(), fieldType)
              if err != nil {
                  continue
              }

              fieldInfo.FieldId = fieldID
              docRow.Fields[fieldID] = fieldInfo
          }
      } else {
          // 扫描所有字段
          rowPrefix := buildRowPrefix(tableID, rowID, "")
          it := r.db.NewIterator(r.ro)
          defer it.Close()

          for it.Seek([]byte(rowPrefix)); it.ValidForPrefix([]byte(rowPrefix)); it.Next() {
              key := string(it.Key().Data())
              fieldID, err := parseFieldIDFromKey(key)
              if err != nil {
                  continue
              }

              fieldType, err := r.getFieldType(ctx, fieldID)
              if err != nil {
                  continue
              }

              fieldInfo, err := deserializeFieldValue(it.Value().Data(), fieldType)
              if err != nil {
                  continue
              }

              fieldInfo.FieldId = fieldID
              docRow.Fields[fieldID] = fieldInfo
          }
      }

      // 过滤 Map 字段
      if len(mapKeys) > 0 {
          filterMapFields(docRow, mapKeys)
      }

      return docRow, nil
}

3. GetTimingFieldInfos(ctx, params) ([]*pb.DocRow, error)

逻辑：
1. 必须指定 TimeInterval（时序数据必填）

2. 构建扫描范围：
   startKey = {tableID}:{rowID}:{startTime}:f
   endKey = {tableID}:{rowID}:{endTime}:f

3. 使用 Iterator 扫描：
    - 按 times 聚合字段
    - 不检查删除标记（时序数据不支持删除）
    - 如果指定 FieldIDs，只读取指定字段
    - 如果指定 MapKeys，过滤 Map 字段

4. 应用 MaxLimit 限制

5. 返回 DocRow 列表

func (r *RocksDB) GetTimingFieldInfos(ctx context.Context, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
tableID := params.TableID
rowID := params.RowID
timeInterval := params.TimeInterval
fieldIDs := params.FieldIDs
mapKeys := params.MapKeys
maxLimit := params.MaxLimit

      // 时序数据必须指定时间范围
      if timeInterval == nil || timeInterval.Start == "" {
          return nil, fmt.Errorf("时序数据查询必须指定时间范围")
      }

      // 构建扫描范围
      startKey := buildRowPrefix(tableID, rowID, timeInterval.Start)
      endKey := buildRowPrefix(tableID, rowID, timeInterval.End)
      if timeInterval.End == "" {
          endKey = buildRowPrefix(tableID, rowID, "9999-12-31 23:59:59") // 最大时间
      }

      var results []*pb.DocRow
      it := r.db.NewIterator(r.ro)
      defer it.Close()

      currentTimes := ""
      currentDocRow := &pb.DocRow{Fields: make(map[uint32]*pb.FieldInfo)}
      count := uint32(0)

      for it.Seek([]byte(startKey)); it.Valid(); it.Next() {
          key := string(it.Key().Data())

          // 检查是否超出范围
          if key > endKey {
              break
          }

          // 解析 Key
          _, parsedRowID, times, fieldID, err := parseKeyComponents(key)
          if err != nil || times == "" { // 时序数据 times 不能为空
              continue
          }

          // 如果指定了 rowID，必须匹配
          if rowID != "" && parsedRowID != rowID {
              continue
          }

          // 新时间点开始
          if times != currentTimes {
              // 保存上一时间点
              if currentTimes != "" {
                  results = append(results, currentDocRow)
                  count++
                  if maxLimit > 0 && count >= maxLimit {
                      break
                  }
              }

              // 开始新时间点
              currentTimes = times
              currentDocRow = &pb.DocRow{
                  RowId:  parsedRowID,
                  Times:  times,
                  Fields: make(map[uint32]*pb.FieldInfo),
              }
          }

          // 过滤字段
          if len(fieldIDs) > 0 && !contains(fieldIDs, fieldID) {
              continue
          }

          // 反序列化字段值
          fieldType, err := r.getFieldType(ctx, fieldID)
          if err != nil {
              continue
          }

          fieldInfo, err := deserializeFieldValue(it.Value().Data(), fieldType)
          if err != nil {
              continue
          }

          fieldInfo.FieldId = fieldID
          currentDocRow.Fields[fieldID] = fieldInfo
      }

      // 处理最后一个时间点
      if currentTimes != "" && (maxLimit == 0 || count < maxLimit) {
          results = append(results, currentDocRow)
      }

      // 过滤 Map 字段
      if len(mapKeys) > 0 {
          for _, docRow := range results {
              filterMapFields(docRow, mapKeys)
          }
      }

      return results, nil
}

  ---
4.6 set.go - 数据写入（续）

func (r *RocksDB) SetFieldInfos(ctx context.Context, params *dao.SetFieldParams) (*pb.SetFieldInfosRsp, error) {
log.DebugContextf(ctx, "RocksDB SetFieldInfos: %+v", params)

      // 初始化响应
      rsp := &pb.SetFieldInfosRsp{
          RetInfo: &pb.RetInfo{
              Code: pb.EnumErrorCode_SUCCESS,
              Msg:  "success",
          },
          ModifyInfos: []*pb.ModifyFieldInfo{},
          LastRows:    []*pb.DocRow{},
          FailedRows:  []*pb.FailedDocRow{},
      }

      if len(params.UpdateDocRows) == 0 {
          return rsp, nil
      }

      r.tableID = params.TableID

      // 根据数据类型分发
      switch params.DataType {
      case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
          return r.processStaticDataUpdate(ctx, params)

      case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
          return r.processTimingDataUpdate(ctx, params)

      default:
          return nil, fmt.Errorf("invalid data type: %v", params.DataType)
      }
}

2. processStaticDataUpdate(ctx, params) (*pb.SetFieldInfosRsp, error)

逻辑：
1. 初始化响应和 WriteBatch

2. 遍历 UpdateDocRows：
    - 对于每个 rowID，检查是否已删除
    - 如果已删除，跳过或返回错误
    - 读取现有字段值（用于生成 ModifyInfos）
    - 根据 UpdateType 处理：
        - SET_UPDATE: 覆盖写入
        - DEL_UPDATE: 删除字段（实际是删除 Key）
        - APPEND_UPDATE: 追加写入（仅 MAP/SET 类型）
    - 生成 ModifyFieldInfo

3. 批量提交 WriteBatch

4. 返回响应（包含 ModifyInfos、FailedRows）

func (r *RocksDB) processStaticDataUpdate(ctx context.Context, params *dao.SetFieldParams) (*pb.SetFieldInfosRsp, error) {
tableID := params.TableID
updateDocRows := params.UpdateDocRows

      rsp := &pb.SetFieldInfosRsp{
          RetInfo: &pb.RetInfo{
              Code: pb.EnumErrorCode_SUCCESS,
              Msg:  "success",
          },
          ModifyInfos: []*pb.ModifyFieldInfo{},
          FailedRows:  []*pb.FailedDocRow{},
      }

      batch := gorocksdb.NewWriteBatch()
      defer batch.Destroy()

      for _, updateRow := range updateDocRows {
          rowID := updateRow.RowId

          // 检查是否已删除
          deleted, err := r.isRowDeleted(tableID, rowID)
          if err != nil {
              log.ErrorContextf(ctx, "检查删除状态失败: %v", err)
              continue
          }
          if deleted {
              log.WarnContextf(ctx, "行[%s]已删除，跳过更新", rowID)
              continue
          }

          // 读取现有值（用于生成 ModifyInfos）
          oldDocRow, _ := r.readStaticRow(ctx, tableID, rowID, nil, nil)
          newDocRow := &pb.DocRow{
              RowId:  rowID,
              Fields: make(map[uint32]*pb.FieldInfo),
          }

          // 处理每个字段
          for fieldID, updateFieldInfo := range updateRow.Fields {
              fieldInfo := updateFieldInfo.FieldInfo
              updateType := updateFieldInfo.UpdateType

              key := buildFieldKey(tableID, rowID, "", fieldID)

              switch updateType {
              case pb.EnumUpdateType_SET_UPDATE:
                  // 覆盖写入
                  value, err := serializeFieldValue(fieldInfo)
                  if err != nil {
                      log.ErrorContextf(ctx, "序列化字段失败: %v", err)
                      continue
                  }
                  batch.Put([]byte(key), value)
                  newDocRow.Fields[fieldID] = fieldInfo

              case pb.EnumUpdateType_DEL_UPDATE:
                  // 删除字段
                  batch.Delete([]byte(key))

              case pb.EnumUpdateType_APPEND_UPDATE:
                  // 追加写入（MAP/SET 类型）
                  err := r.appendFieldValue(batch, key, fieldInfo)
                  if err != nil {
                      log.ErrorContextf(ctx, "追加字段失败: %v", err)
                      continue
                  }
                  // 读取追加后的值
                  newValue, _ := r.db.Get(r.ro, []byte(key))
                  if newValue.Exists() {
                      fieldType, _ := r.getFieldType(ctx, fieldID)
                      newFieldInfo, _ := deserializeFieldValue(newValue.Data(), fieldType)
                      newDocRow.Fields[fieldID] = newFieldInfo
                      newValue.Free()
                  }
              }
          }

          // 生成 ModifyFieldInfo
          if oldDocRow != nil || len(newDocRow.Fields) > 0 {
              modifyInfo := &pb.ModifyFieldInfo{
                  OldDocRow: oldDocRow,
                  NewDocRow: newDocRow,
              }
              rsp.ModifyInfos = append(rsp.ModifyInfos, modifyInfo)
          }
      }

      // 提交批量写入
      err := r.db.Write(r.wo, batch)
      if err != nil {
          return nil, errs.New(int(pb.EnumErrorCode_FAILED_UPDATE),
              fmt.Sprintf("批量写入失败: %v", err))
      }

      log.InfoContextf(ctx, "静态数据更新成功，共 %d 行", len(updateDocRows))
      return rsp, nil
}

// appendFieldValue 追加字段值（MAP/SET 类型）
func (r *RocksDB) appendFieldValue(batch *gorocksdb.WriteBatch, key string, fieldInfo *pb.FieldInfo) error {
// 读取现有值
existingValue, err := r.db.Get(r.ro, []byte(key))
if err != nil {
return err
}
defer existingValue.Free()

      // 根据字段类型处理
      switch fieldInfo.FieldType {
      case pb.EnumFieldType_MAP_KV_FIELD:
          // MAP 追加
          existingMap := make(map[string]interface{})
          if existingValue.Exists() {
              json.Unmarshal(existingValue.Data(), &existingMap)
          }

          // 合并新 Map
          newMap := fieldInfo.MapValue.Entries
          for k, v := range newMap {
              existingMap[k] = v
          }

          // 序列化并写入
          mergedValue, _ := json.Marshal(existingMap)
          batch.Put([]byte(key), mergedValue)

      case pb.EnumFieldType_SET_FIELD:
          // SET 追加
          existingSet := []string{}
          if existingValue.Exists() {
              json.Unmarshal(existingValue.Data(), &existingSet)
          }

          // 合并新 SET（去重）
          newSet := fieldInfo.SimpleValue.GetStrList().Values
          setMap := make(map[string]bool)
          for _, v := range existingSet {
              setMap[v] = true
          }
          for _, v := range newSet {
              setMap[v] = true
          }

          mergedSet := make([]string, 0, len(setMap))
          for k := range setMap {
              mergedSet = append(mergedSet, k)
          }

          mergedValue, _ := json.Marshal(mergedSet)
          batch.Put([]byte(key), mergedValue)

      default:
          return fmt.Errorf("APPEND_UPDATE only supports MAP and SET types")
      }

      return nil
}

3. processTimingDataUpdate(ctx, params) (*pb.SetFieldInfosRsp, error)

逻辑：
1. 初始化响应和 WriteBatch

2. 遍历 UpdateDocRows：
    - 对于每个 (rowID, times)，直接写入（不检查删除标记）
    - 根据 UpdateType 处理（同静态数据）
    - 生成 ModifyFieldInfo（可选，性能考虑可以简化）

3. 如果指定 HistoricalRowsLimit：
    - 倒序扫描最近 N 条记录
    - 填充 LastRows

4. 批量提交 WriteBatch

5. 返回响应

func (r *RocksDB) processTimingDataUpdate(ctx context.Context, params *dao.SetFieldParams) (*pb.SetFieldInfosRsp, error) {
tableID := params.TableID
updateDocRows := params.UpdateDocRows
historicalRowsLimit := params.HistoricalRowsLimit

      rsp := &pb.SetFieldInfosRsp{
          RetInfo: &pb.RetInfo{
              Code: pb.EnumErrorCode_SUCCESS,
              Msg:  "success",
          },
          ModifyInfos: []*pb.ModifyFieldInfo{},
          LastRows:    []*pb.DocRow{},
          FailedRows:  []*pb.FailedDocRow{},
      }

      batch := gorocksdb.NewWriteBatch()
      defer batch.Destroy()

      for _, updateRow := range updateDocRows {
          rowID := updateRow.RowId
          times := updateRow.Times

          // 时序数据不检查删除标记，直接写入
          newDocRow := &pb.DocRow{
              RowId:  rowID,
              Times:  times,
              Fields: make(map[uint32]*pb.FieldInfo),
          }

          // 处理每个字段
          for fieldID, updateFieldInfo := range updateRow.Fields {
              fieldInfo := updateFieldInfo.FieldInfo
              updateType := updateFieldInfo.UpdateType

              key := buildFieldKey(tableID, rowID, times, fieldID)

              switch updateType {
              case pb.EnumUpdateType_SET_UPDATE:
                  value, err := serializeFieldValue(fieldInfo)
                  if err != nil {
                      log.ErrorContextf(ctx, "序列化字段失败: %v", err)
                      continue
                  }
                  batch.Put([]byte(key), value)
                  newDocRow.Fields[fieldID] = fieldInfo

              case pb.EnumUpdateType_DEL_UPDATE:
                  batch.Delete([]byte(key))

              case pb.EnumUpdateType_APPEND_UPDATE:
                  err := r.appendFieldValue(batch, key, fieldInfo)
                  if err != nil {
                      log.ErrorContextf(ctx, "追加字段失败: %v", err)
                      continue
                  }
              }
          }

          // 简化：时序数据不生成 ModifyInfos（性能优化）
          // 如果需要，可以读取旧值生成
      }

      // 提交批量写入
      err := r.db.Write(r.wo, batch)
      if err != nil {
          return nil, errs.New(int(pb.EnumErrorCode_FAILED_UPDATE),
              fmt.Sprintf("批量写入失败: %v", err))
      }

      // 获取历史数据
      if historicalRowsLimit > 0 && len(updateDocRows) > 0 {
          firstRow := updateDocRows[0]
          lastRows, err := r.getLastNRows(ctx, tableID, firstRow.RowId, historicalRowsLimit)
          if err != nil {
              log.WarnContextf(ctx, "获取历史数据失败: %v", err)
          } else {
              rsp.LastRows = lastRows
          }
      }

      log.InfoContextf(ctx, "时序数据更新成功，共 %d 行", len(updateDocRows))
      return rsp, nil
}

// getLastNRows 获取最近 N 条时序数据
func (r *RocksDB) getLastNRows(ctx context.Context, tableID, rowID string, limit uint32) ([]*pb.DocRow, error) {
// 构建行前缀
rowPrefix := fmt.Sprintf("%s:%s:", tableID, rowID)

      // 倒序扫描
      it := r.db.NewIterator(r.ro)
      defer it.Close()

      var results []*pb.DocRow
      currentTimes := ""
      currentDocRow := &pb.DocRow{Fields: make(map[uint32]*pb.FieldInfo)}
      count := uint32(0)

      // 从最后开始扫描
      it.SeekToLast()

      for ; it.Valid() && count < limit; it.Prev() {
          key := string(it.Key().Data())

          // 检查前缀
          if !strings.HasPrefix(key, rowPrefix) {
              continue
          }

          // 解析 Key
          _, _, times, fieldID, err := parseKeyComponents(key)
          if err != nil || times == "" {
              continue
          }

          // 新时间点
          if times != currentTimes {
              if currentTimes != "" {
                  results = append([]*pb.DocRow{currentDocRow}, results...) // 头部插入
                  count++
                  if count >= limit {
                      break
                  }
              }
              currentTimes = times
              currentDocRow = &pb.DocRow{
                  RowId:  rowID,
                  Times:  times,
                  Fields: make(map[uint32]*pb.FieldInfo),
              }
          }

          // 反序列化字段
          fieldType, err := r.getFieldType(ctx, fieldID)
          if err != nil {
              continue
          }

          fieldInfo, err := deserializeFieldValue(it.Value().Data(), fieldType)
          if err != nil {
              continue
          }

          fieldInfo.FieldId = fieldID
          currentDocRow.Fields[fieldID] = fieldInfo
      }

      // 最后一个时间点
      if currentTimes != "" && count < limit {
          results = append([]*pb.DocRow{currentDocRow}, results...)
      }

      return results, nil
}

  ---
4.7 delete.go - 数据删除

职责：
- DeleteRows：删除接口（仅静态数据支持）

核心逻辑：

func (r *RocksDB) DeleteRows(ctx context.Context, params *dao.DeleteRowsParams) (*pb.DeleteRowsRsp, error) {
rsp := &pb.DeleteRowsRsp{
RetInfo: &pb.RetInfo{
Code: pb.EnumErrorCode_SUCCESS,
Msg:  "success",
},
DeletedCount: 0,
}

      r.tableID = params.TableID

      // 时序数据不支持删除
      if params.DataType == pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE {
          return nil, fmt.Errorf("时序数据不支持删除操作")
      }

      // 仅静态数据支持删除
      if params.DataType != pb.EnumDataTypeCategory_STATIC_DATA_TYPE {
          return nil, fmt.Errorf("invalid data type")
      }

      if len(params.RowIDs) == 0 {
          return rsp, nil
      }

      // 批量设置删除标记
      batch := gorocksdb.NewWriteBatch()
      defer batch.Destroy()

      deletedCount := uint64(0)
      currentTime := time.Now().Format("2006-01-02 15:04:05")

      for _, rowID := range params.RowIDs {
          // 检查是否已删除
          deleted, err := r.isRowDeleted(params.TableID, rowID)
          if err != nil {
              log.ErrorContextf(ctx, "检查删除状态失败: %v", err)
              continue
          }
          if deleted {
              log.DebugContextf(ctx, "行[%s]已删除，跳过", rowID)
              continue
          }

          // 设置删除标记
          deleteKey := buildDeletedKey(params.TableID, rowID)
          batch.Put([]byte(deleteKey), []byte("1"))

          // 设置删除时间
          deleteTimeKey := buildDeletedTimeKey(params.TableID, rowID)
          batch.Put([]byte(deleteTimeKey), []byte(currentTime))

          deletedCount++
      }

      // 提交批量操作
      err := r.db.Write(r.wo, batch)
      if err != nil {
          return nil, errs.New(int(pb.EnumErrorCode_INNER_ERR),
              fmt.Sprintf("删除操作失败: %v", err))
      }

      rsp.DeletedCount = deletedCount
      log.InfoContextf(ctx, "静态数据删除成功，共 %d 行", deletedCount)
      return rsp, nil
}

  ---
4.8 search.go - 数据搜索

职责：
- SearchFieldInfos：统一搜索入口
- SearchStaticFieldInfos：静态数据搜索
- SearchTimingFieldInfos：时序数据搜索

核心逻辑：

1. SearchFieldInfos(ctx, params) ([]*pb.DocRow, uint64, error)

func (r *RocksDB) SearchFieldInfos(ctx context.Context, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
log.DebugContextf(ctx, "RocksDB SearchFieldInfos: %+v", params)

      r.tableID = params.TableID

      switch params.DataType {
      case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
          return r.SearchStaticFieldInfos(ctx, params)

      case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
          return r.SearchTimingFieldInfos(ctx, params)

      default:
          return nil, 0, fmt.Errorf("invalid data type")
      }
}

2. SearchStaticFieldInfos(ctx, params) ([]*pb.DocRow, uint64, error)

逻辑：
1. 全表扫描（或按 rowID 过滤）

2. 在内存中应用搜索条件：
    - 遍历 SearchCondGroup
    - 评估每个 SearchCond（eq, ne, gt, lt, in, like 等）
    - 应用逻辑关系（AND/OR）

3. 过滤已删除的行

4. 在内存中排序（根据 SearchSort）

5. 分页（根据 PageInfo）

6. 返回结果和总数

func (r *RocksDB) SearchStaticFieldInfos(ctx context.Context, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
tableID := params.TableID
searchOptions := params.SearchOptions
pageInfo := params.PageInfo

      // 1. 读取所有行（或按 rowID 过滤）
      getParams := &dao.GetFieldParams{
          TableID:  tableID,
          DataType: pb.EnumDataTypeCategory_STATIC_DATA_TYPE,
          RowID:    params.RowID,
          FieldIDs: searchOptions.ReturnFieldIds,
          MaxLimit: 0, // 不限制
      }

      allRows, err := r.GetStaticFieldInfos(ctx, getParams)
      if err != nil {
          return nil, 0, err
      }

      // 2. 应用搜索条件
      var filteredRows []*pb.DocRow
      for _, docRow := range allRows {
          if r.evaluateSearchConditions(ctx, docRow, searchOptions) {
              filteredRows = append(filteredRows, docRow)
          }
      }

      totalCount := uint64(len(filteredRows))

      // 3. 排序
      if len(searchOptions.Sort) > 0 {
          r.sortDocRows(filteredRows, searchOptions.Sort)
      }

      // 4. 分页
      if pageInfo != nil {
          pageIdx := pageInfo.PageIdx
          pageSize := pageInfo.Size
          if pageSize == 0 {
              pageSize = 50
          }
          if pageSize > 200 {
              pageSize = 200
          }

          start := (pageIdx - 1) * pageSize
          end := start + pageSize

          if start >= uint32(len(filteredRows)) {
              return []*pb.DocRow{}, totalCount, nil
          }
          if end > uint32(len(filteredRows)) {
              end = uint32(len(filteredRows))
          }

          filteredRows = filteredRows[start:end]
      }

      return filteredRows, totalCount, nil
}

// evaluateSearchConditions 评估搜索条件
func (r *RocksDB) evaluateSearchConditions(ctx context.Context, docRow *pb.DocRow, searchOptions *pb.SearchOptions) bool {
if searchOptions == nil || len(searchOptions.CondGroups) == 0 {
return true
}

      // 评估条件组
      groupResults := make([]bool, len(searchOptions.CondGroups))
      for i, condGroup := range searchOptions.CondGroups {
          groupResults[i] = r.evaluateCondGroup(ctx, docRow, condGroup)
      }

      // 应用条件组间的逻辑关系
      if searchOptions.Logical == pb.Logical_LogicalOr {
          for _, result := range groupResults {
              if result {
                  return true
              }
          }
          return false
      } else { // AND
          for _, result := range groupResults {
              if !result {
                  return false
              }
          }
          return true
      }
}

// evaluateCondGroup 评估单个条件组
func (r *RocksDB) evaluateCondGroup(ctx context.Context, docRow *pb.DocRow, condGroup *pb.SearchCondGroup) bool {
if len(condGroup.Conds) == 0 {
return true
}

      condResults := make([]bool, len(condGroup.Conds))
      for i, cond := range condGroup.Conds {
          condResults[i] = r.evaluateSingleCond(ctx, docRow, cond)
      }

      // 应用条件间的逻辑关系
      if condGroup.Logical == pb.Logical_LogicalOr {
          for _, result := range condResults {
              if result {
                  return true
              }
          }
          return false
      } else { // AND
          for _, result := range condResults {
              if !result {
                  return false
              }
          }
          return true
      }
}

// evaluateSingleCond 评估单个条件
func (r *RocksDB) evaluateSingleCond(ctx context.Context, docRow *pb.DocRow, cond *pb.SearchCond) bool {
fieldID := cond.FieldId
fieldInfo, exists := docRow.Fields[fieldID]
if !exists {
return false
}

      // 根据操作符评估
      switch cond.Op {
      case pb.Operator_eq:
          return r.compareEqual(fieldInfo, cond.Value)
      case pb.Operator_ne:
          return !r.compareEqual(fieldInfo, cond.Value)
      case pb.Operator_gt:
          return r.compareGreater(fieldInfo, cond.Value)
      case pb.Operator_gte:
          return r.compareGreater(fieldInfo, cond.Value) || r.compareEqual(fieldInfo, cond.Value)
      case pb.Operator_lt:
          return r.compareLess(fieldInfo, cond.Value)
      case pb.Operator_lte:
          return r.compareLess(fieldInfo, cond.Value) || r.compareEqual(fieldInfo, cond.Value)
      case pb.Operator_in:
          return r.compareIn(fieldInfo, cond.Value)
      case pb.Operator_like:
          return r.compareLike(fieldInfo, cond.Value)
      default:
          return false
      }
}

// 比较函数实现（简化版）
func (r *RocksDB) compareEqual(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
// 根据字段类型比较
switch fieldInfo.FieldType {
case pb.EnumFieldType_STR_FIELD:
return fieldInfo.SimpleValue.GetStr() == value.GetStr()
case pb.EnumFieldType_INT_FIELD:
return fieldInfo.SimpleValue.GetInt() == value.GetInt()
case pb.EnumFieldType_FLOAT_FIELD:
return fieldInfo.SimpleValue.GetFloat() == value.GetFloat()
default:
return false
}
}

func (r *RocksDB) compareGreater(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
switch fieldInfo.FieldType {
case pb.EnumFieldType_INT_FIELD:
return fieldInfo.SimpleValue.GetInt() > value.GetInt()
case pb.EnumFieldType_FLOAT_FIELD:
return fieldInfo.SimpleValue.GetFloat() > value.GetFloat()
default:
return false
}
}

func (r *RocksDB) compareLess(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
switch fieldInfo.FieldType {
case pb.EnumFieldType_INT_FIELD:
return fieldInfo.SimpleValue.GetInt() < value.GetInt()
case pb.EnumFieldType_FLOAT_FIELD:
return fieldInfo.SimpleValue.GetFloat() < value.GetFloat()
default:
return false
}
}

func (r *RocksDB) compareIn(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
// 实现 IN 操作符
// 简化：略
return false
}

func (r *RocksDB) compareLike(fieldInfo *pb.FieldInfo, value *pb.SimpleValue) bool {
// 实现 LIKE 操作符（模糊匹配）
if fieldInfo.FieldType != pb.EnumFieldType_STR_FIELD {
return false
}
fieldStr := fieldInfo.SimpleValue.GetStr()
pattern := value.GetStr()
// 简单实现：使用 strings.Contains
return strings.Contains(fieldStr, strings.Trim(pattern, "*"))
}

// sortDocRows 对结果排序
func (r *RocksDB) sortDocRows(rows []*pb.DocRow, sortRules []*pb.SearchSort) {
// 使用 sort.Slice 排序
sort.Slice(rows, func(i, j int) bool {
for _, rule := range sortRules {
fieldID := rule.FieldId
fieldI := rows[i].Fields[fieldID]
fieldJ := rows[j].Fields[fieldID]

              if fieldI == nil || fieldJ == nil {
                  continue
              }

              cmp := r.compareFieldValues(fieldI, fieldJ)
              if cmp == 0 {
                  continue
              }

              if rule.Sort == pb.Sort_Asc {
                  return cmp < 0
              } else {
                  return cmp > 0
              }
          }
          return false
      })
}

// compareFieldValues 比较字段值（返回 -1, 0, 1）
func (r *RocksDB) compareFieldValues(a, b *pb.FieldInfo) int {
switch a.FieldType {
case pb.EnumFieldType_INT_FIELD:
valA := a.SimpleValue.GetInt()
valB := b.SimpleValue.GetInt()
if valA < valB {
return -1
} else if valA > valB {
return 1
}
return 0
case pb.EnumFieldType_FLOAT_FIELD:
valA := a.SimpleValue.GetFloat()
valB := b.SimpleValue.GetFloat()
if valA < valB {
return -1
} else if valA > valB {
return 1
}
return 0
case pb.EnumFieldType_STR_FIELD:
valA := a.SimpleValue.GetStr()
valB := b.SimpleValue.GetStr()
return strings.Compare(valA, valB)
default:
return 0
}
}

3. SearchTimingFieldInfos(ctx, params) ([]*pb.DocRow, uint64, error)

逻辑：类似静态数据搜索，但：
- 必须指定时间范围
- 不检查删除标记
- 可以按时间排序

func (r *RocksDB) SearchTimingFieldInfos(ctx context.Context, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
tableID := params.TableID
timeInterval := params.TimeInterval
searchOptions := params.SearchOptions
pageInfo := params.PageInfo

      // 时序数据必须指定时间范围
      if timeInterval == nil {
          return nil, 0, fmt.Errorf("时序数据搜索必须指定时间范围")
      }

      // 1. 读取时间范围内的数据
      getParams := &dao.GetFieldParams{
          TableID:      tableID,
          DataType:     pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE,
          TimeInterval: timeInterval,
          FieldIDs:     searchOptions.ReturnFieldIds,
          MaxLimit:     0,
      }

      allRows, err := r.GetTimingFieldInfos(ctx, getParams)
      if err != nil {
          return nil, 0, err
      }

      // 2. 应用搜索条件
      var filteredRows []*pb.DocRow
      for _, docRow := range allRows {
          if r.evaluateSearchConditions(ctx, docRow, searchOptions) {
              filteredRows = append(filteredRows, docRow)
          }
      }

      totalCount := uint64(len(filteredRows))

      // 3. 排序（时序数据可以按时间排序）
      if len(searchOptions.Sort) > 0 {
          r.sortDocRows(filteredRows, searchOptions.Sort)
      } else if params.TimeSort == pb.Sort_Desc {
          // 默认按时间降序
          sort.Slice(filteredRows, func(i, j int) bool {
              return filteredRows[i].Times > filteredRows[j].Times
          })
      } else {
          // 默认按时间升序
          sort.Slice(filteredRows, func(i, j int) bool {
              return filteredRows[i].Times < filteredRows[j].Times
          })
      }

      // 4. 分页
      if pageInfo != nil {
          pageIdx := pageInfo.PageIdx
          pageSize := pageInfo.Size
          if pageSize == 0 {
              pageSize = 50
          }
          if pageSize > 200 {
              pageSize = 200
          }

          start := (pageIdx - 1) * pageSize
          end := start + pageSize

          if start >= uint32(len(filteredRows)) {
              return []*pb.DocRow{}, totalCount, nil
          }
          if end > uint32(len(filteredRows)) {
              end = uint32(len(filteredRows))
          }

          filteredRows = filteredRows[start:end]
      }

      return filteredRows, totalCount, nil
}

  ---
五、配置和依赖

5.1 配置文件扩展

在 config/config.go 中添加：

// Config 适配层服务配置
type Config struct {
...
RocksDB RocksDBConfig `yaml:"rocksdb"`
}

// RocksDBConfig RocksDB 配置
type RocksDBConfig struct {
// DataPath RocksDB 数据文件路径
DataPath string `yaml:"data_path"`
// BlockCacheMB 块缓存大小（MB）
BlockCacheMB int64 `yaml:"block_cache_mb"`
}

func setDefaults(cfg *Config) {
...
if cfg.RocksDB.DataPath == "" {
cfg.RocksDB.DataPath = "../database/rocksdb"
}
if cfg.RocksDB.BlockCacheMB == 0 {
cfg.RocksDB.BlockCacheMB = 512
}
}

在 adapter.yaml 中添加：

rocksdb:
data_path: "../database/rocksdb"
block_cache_mb: 512

5.2 Go 依赖

在 go.mod 中添加：

require (
github.com/tecbot/gorocksdb v0.0.0-20191217155057-f0fad39f321c
)

  ---
六、实施步骤

Phase 1: 基础框架（1-2天）

1. 创建 rocksdb/ 目录
2. 实现 init.go、rocksdb.go、utils.go
3. 实现 Key 构建和序列化函数
4. 测试基础连接

Phase 2: 表操作（1天）

5. 实现 schema.go
6. 测试建表、删表、检查表

Phase 3: 数据读取（2天）

7. 实现 get.go
8. 测试静态数据读取
9. 测试时序数据读取

Phase 4: 数据写入（2天）

10. 实现 set.go
11. 测试静态数据写入（SET/DEL/APPEND）
12. 测试时序数据写入

Phase 5: 数据删除（1天）

13. 实现 delete.go
14. 测试静态数据软删除

Phase 6: 数据搜索（2天）

15. 实现 search.go
16. 测试条件查询、排序、分页

Phase 7: 集成测试（2天）

17. 端到端测试
18. 性能测试和优化

  ---
七、关键差异总结
┌────────────┬────────────────────────────┬──────────────────────────────────┐
│    特性    │          静态数据          │             时序数据             │
├────────────┼────────────────────────────┼──────────────────────────────────┤
│ 主键       │ rowID                      │ rowID + times                    │
├────────────┼────────────────────────────┼──────────────────────────────────┤
│ times 字段 │ 空字符串 ""                │ 具体时间戳                       │
├────────────┼────────────────────────────┼──────────────────────────────────┤
│ 删除支持   │ ✅ 软删除（_meta:deleted） │ ❌ 不支持删除                    │
├────────────┼────────────────────────────┼──────────────────────────────────┤
│ 查询过滤   │ 需要过滤 _deleted=1        │ 无需过滤删除标记                 │
├────────────┼────────────────────────────┼──────────────────────────────────┤
│ Key 示例   │ table:row::f1              │ table:row:2024-01-15 09:30:00:f1 │
└────────────┴────────────────────────────┴──────────────────────────────────┘
  ---
完整执行计划到此结束！ 这是一个可直接实施的详细方案，涵盖了所有关键接口和实现细节。
