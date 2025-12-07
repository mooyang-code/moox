# 采集任务管理模块重构

## 一、项目概述

### 1.1 目标

当用户操作任务规则表（新增、修改、禁用）时，自动同步生成各云节点的采集任务实例，同时支持定时任务处理标的列表动态变化。

### 1.2 核心特性

- 一个交易对对应一个任务实例（按对象拆分）
- 增量更新（只处理变化的实例）
- 禁用后再启用重新生成
- 策略模式（不同数据类型实现不同分配策略）
- 混合触发（用户操作立即生效 + 定时任务兜底）

### 1.3 目录重命名

collector → collectmgr

---

## 二、整体架构

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              系统架构图                                        │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                               │
│  ┌─────────────┐   ┌─────────────┐                                          │
│  │ 用户操作规则 │   │  定时任务   │                      ← 触发层              │
│  └──────┬──────┘   └──────┬──────┘                                          │
│         │                 │                                                  │
│         └────────┬────────┘                                                  │
│                  ▼                                                           │
│  ┌─────────────────────────────────┐                                        │
│  │      TaskPlannerService         │                     ← 规划器层          │
│  │   (统一入口,幂等,增量更新)       │                                         │
│  └─────────────────┬───────────────┘                                        │
│                    │                                                         │
│                    ▼                                                         │
│  ┌─────────────────────────────────┐                                        │
│  │     DistributorRegistry         │                     ← 注册中心          │
│  │      (分配器注册与获取)          │                                         │
│  └─────────────────┬───────────────┘                                        │
│                    │                                                         │
│    ┌───────────────┼───────────────┬───────────────┐                        │
│    ▼               ▼               ▼               ▼                        │
│ ┌───────┐     ┌───────┐     ┌───────┐     ┌───────┐                        │
│ │ Kline │     │Ticker │     │ Trade │     │ News  │      ← 策略实现层       │
│ └───┬───┘     └───┬───┘     └───┬───┘     └───┬───┘                        │
│     │             │             │             │                             │
│     └─────────────┴─────────────┴─────────────┘                             │
│                         │                                                    │
│                         ▼                                                    │
│         ┌───────────────────────────────┐                                   │
│         │       BaseDistributor         │                ← 基础能力层        │
│         │  (节点匹配、参数构建等通用逻辑) │                                    │
│         └───────────────┬───────────────┘                                   │
│                         │                                                    │
│         ┌───────────────┼───────────────┐                                   │
│         ▼               ▼               ▼                                   │
│  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐                           │
│  │SymbolProvider│ │  NodeDAO   │ │ InstanceDAO │          ← 数据访问层      │
│  └─────────────┘ └─────────────┘ └─────────────┘                           │
│                                                                               │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 三、核心接口设计

### 3.1 TaskDistributor（任务分配器接口）

```go
// TaskDistributor 任务分配器接口
// 不同数据类型实现不同的分配策略
type TaskDistributor interface {
    // GetDataType 返回此分配器支持的数据类型
    GetDataType() string

    // GetTargetObjects 获取目标对象列表
    // - 需要按对象拆分：返回 ["BTC-USDT", "ETH-USDT", ...]
    // - 不需要拆分：返回 [] 或 nil（统一处理为一个 symbol="" 的实例）
    GetTargetObjects(ctx context.Context, rule *TaskRuleDTO) ([]string, error)

    // BuildTaskParams 为指定对象构建任务参数
    // object 为空字符串时表示不按对象拆分
    BuildTaskParams(ctx context.Context, rule *TaskRuleDTO, object string) (string, error)

    // GetMatchingNodes 根据分配策略获取匹配的节点列表
    GetMatchingNodes(ctx context.Context, rule *TaskRuleDTO) ([]*CloudNodeDTO, error)
}
```

### 3.2 SymbolProvider（标的提供者接口）

```go
// SymbolProvider 标的提供者接口
// 用于获取交易对列表，支持多种数据源
type SymbolProvider interface {
    // GetSymbols 获取指定数据源的所有标的
    // dataSource: binance, okx 等
    GetSymbols(ctx context.Context, dataSource string) ([]string, error)
}
```

### 3.3 TaskPlannerService（任务规划器接口）

```go
// TaskPlannerService 任务规划器服务接口
type TaskPlannerService interface {
    // SyncRuleInstances 同步指定规则的任务实例（幂等操作）
    // 用于：用户创建/修改/启用规则时立即调用
    SyncRuleInstances(ctx context.Context, ruleID string) (*SyncResult, error)

    // InvalidateRuleInstances 使规则的所有实例失效（软删除）
    // 用于：用户禁用规则时调用
    InvalidateRuleInstances(ctx context.Context, ruleID string) error

    // SyncAllEnabledRules 同步所有启用的规则（定时任务调用）
    SyncAllEnabledRules(ctx context.Context) (*BatchSyncResult, error)
}

// SyncResult 单规则同步结果
type SyncResult struct {
    RuleID    string `json:"rule_id"`
    Created   int    `json:"created"`   // 新建实例数
    Updated   int    `json:"updated"`   // 更新实例数
    Deleted   int    `json:"deleted"`   // 删除实例数
    Unchanged int    `json:"unchanged"` // 未变化数
}

// BatchSyncResult 批量同步结果
type BatchSyncResult struct {
    TotalRules   int     `json:"total_rules"`
    SyncedRules  int     `json:"synced_rules"`
    FailedRules  int     `json:"failed_rules"`
    TotalCreated int     `json:"total_created"`
    TotalUpdated int     `json:"total_updated"`
    TotalDeleted int     `json:"total_deleted"`
    Errors       []error `json:"-"`
}
```

---

## 四、数据模型

### 4.1 任务实例模型扩展

**文件:** `collectmgr/model/collector_task_instance.go`

```go
type CollectorTaskInstance struct {
    ID         int        `gorm:"primaryKey;column:c_id;autoIncrement"`
    TaskID     string     `gorm:"column:c_task_id;uniqueIndex"`
    RuleID     string     `gorm:"column:c_rule_id;index:idx_rule_id"`
    NodeID     string     `gorm:"column:c_node_id;index:idx_node_status"`

    // 新增：标的字段（用于唯一约束和快速查询）
    Symbol     string     `gorm:"column:c_symbol;default:''"`

    TaskParams string     `gorm:"column:c_task_params;type:text;default:'{}'"`
    Status     int        `gorm:"column:c_status;index:idx_node_status;default:0"`
    StartTime  *time.Time `gorm:"column:c_start_time"`
    EndTime    *time.Time `gorm:"column:c_end_time"`
    Result     string     `gorm:"column:c_result;type:text;default:'{}'"`

    // 已有字段，确认使用
    Invalid    int        `gorm:"column:c_invalid;default:0"`

    CreateTime time.Time  `gorm:"column:c_ctime"`
    ModifyTime time.Time  `gorm:"column:c_mtime"`
}
```

### 4.2 常量定义

**文件:** `collectmgr/model/constants.go`

```go
package model

// 数据类型常量
const (
    DataTypeKline     = "kline"
    DataTypeTicker    = "ticker"
    DataTypeOrderbook = "orderbook"
    DataTypeTrade     = "trade"
    DataTypeNews      = "news"
    DataTypeList      = "list"
)

// 分配类型常量
const (
    AssignmentTypeAuto    = "auto"    // 自动分配
    AssignmentTypeFixed   = "fixed"   // 固定节点
    AssignmentTypePattern = "pattern" // 通配符匹配
)

// 任务实例状态常量
const (
    InstanceStatusPending    = 0 // 待执行
    InstanceStatusRunning    = 1 // 执行中
    InstanceStatusSuccess    = 2 // 成功
    InstanceStatusPartFailed = 3 // 部分失败
    InstanceStatusFailed     = 4 // 失败
)

// Invalid 常量
const (
    InvalidNo  = 0 // 有效
    InvalidYes = 1 // 无效
)
```

### 4.3 采集参数结构

**文件:** `collectmgr/types.go`（新增）

```go
// CollectParams 采集参数（从 JSON 解析）
type CollectParams struct {
    Objects   []string `json:"objects"`   // 标的列表 ["BTC-USDT", "ETH-USDT"]
    Intervals []string `json:"intervals"` // K线周期 ["1m", "5m", "1h"]
    Limit     int      `json:"limit"`     // 数据条数限制
    Depth     int      `json:"depth"`     // 订单簿深度
    Sources   []string `json:"sources"`   // 新闻来源
    Keywords  []string `json:"keywords"`  // 关键词
}

// TaskParams 任务执行参数
type TaskParams struct {
    Symbol     string   `json:"symbol"`      // 标的
    Intervals  []string `json:"intervals"`   // K线周期
    Limit      int      `json:"limit"`       // 数据条数
    Depth      int      `json:"depth"`       // 订单簿深度
    DataSource string   `json:"data_source"` // 数据源
    Sources    []string `json:"sources"`     // 新闻来源
    Keywords   []string `json:"keywords"`    // 关键词
}
```

---

## 五、核心算法

### 5.1 实例同步算法

```
函数: SyncRuleInstances(ctx, ruleID)
输入: ruleID
输出: SyncResult

1. 获取规则详情
   rule = GetTaskRule(ruleID)
   if rule == nil:
       return error("rule not found")
   if rule.Enabled != "true":
       return SyncResult{} // 规则未启用，跳过

2. 获取分配器
   distributor = registry.Get(rule.DataType)
   if distributor == nil:
       return error("distributor not found")

3. 计算应有的实例列表
   computedInstances = []

   // 3.1 获取匹配的节点
   nodes = distributor.GetMatchingNodes(ctx, rule)
   if len(nodes) == 0:
       log.Warn("no matching nodes")

   // 3.2 获取目标对象
   objects = distributor.GetTargetObjects(ctx, rule)
   if len(objects) == 0:
       objects = [""]  // 统一处理：不按对象拆分时生成一个 symbol="" 的实例

   // 3.3 为每个 node × object 组合生成实例
   for each node in nodes:
       for each object in objects:
           params = distributor.BuildTaskParams(ctx, rule, object)
           instance = {
               RuleID:     rule.RuleID,
               NodeID:     node.NodeID,
               Symbol:     object,
               TaskParams: params,
               Status:     0,  // 待执行
               Invalid:    0,
           }
           computedInstances.append(instance)

4. 获取现有有效实例
   existingInstances = instanceDAO.GetActiveInstancesByRule(ruleID)

5. 执行增量更新
   result = diffAndSync(existingInstances, computedInstances)

6. 返回结果
   return result
```

### 5.2 差异对比与同步算法

```
函数: diffAndSync(existing, computed)
输入: existing []Instance, computed []Instance
输出: SyncResult

// 构建索引，Key = RuleID + NodeID + Symbol
existingMap = {}
for inst in existing:
    key = fmt.Sprintf("%s:%s:%s", inst.RuleID, inst.NodeID, inst.Symbol)
    existingMap[key] = inst

computedMap = {}
for inst in computed:
    key = fmt.Sprintf("%s:%s:%s", inst.RuleID, inst.NodeID, inst.Symbol)
    computedMap[key] = inst

result = SyncResult{RuleID: ruleID}

toCreate = []
toUpdate = []

// 处理新增和更新
for key, computed in computedMap:
    if existing, exists = existingMap[key]; exists:
        // 已存在，检查参数是否变化
        if existing.TaskParams != computed.TaskParams:
            toUpdate.append({ID: existing.ID, TaskParams: computed.TaskParams})
            result.Updated++
        else:
            result.Unchanged++
        delete(existingMap, key)
    else:
        // 不存在，需要新建
        computed.TaskID = generateTaskID()
        toCreate.append(computed)
        result.Created++

// 处理删除（existingMap 中剩余的）
toInvalidate = []
for key, existing in existingMap:
    toInvalidate.append(existing.TaskID)
    result.Deleted++

// 批量执行数据库操作
if len(toCreate) > 0:
    instanceDAO.BatchCreateInstances(toCreate)
if len(toUpdate) > 0:
    instanceDAO.BatchUpdateParams(toUpdate)
if len(toInvalidate) > 0:
    instanceDAO.BatchInvalidate(toInvalidate)

return result
```

### 5.3 节点匹配算法

```
函数: GetMatchingNodes(ctx, rule)
输入: rule (包含 AssignmentType, AssignedNodes, NodePattern, DataType)
输出: []*CloudNode

switch rule.AssignmentType:
    case "auto":
        // 自动分配：查找所有支持该数据类型的有效节点
        // SQL: WHERE c_supported_collectors LIKE '%"kline"%' AND c_invalid = 0
        return nodeDAO.GetNodesBySupportedCollector(rule.DataType)
    
    case "fixed":
        // 固定分配：解析 JSON 数组，查询指定节点
        nodeIDs = json.Unmarshal(rule.AssignedNodes)  // ["node-01", "node-02"]
        return nodeDAO.GetNodesByIDs(nodeIDs)
    
    case "pattern":
        // 通配符匹配：将 * 转换为 SQL LIKE 的 %
        // 例如：scf-collector-* → scf-collector-%
        pattern = strings.ReplaceAll(rule.NodePattern, "*", "%")
        return nodeDAO.GetNodesByPattern(pattern)

    default:
        return []
```

---

## 六、各分配器实现

### 6.1 BaseDistributor（基础分配器）

提供通用能力，被具体分配器组合使用：

```go
type BaseDistributor struct {
    nodeDAO        cloudnodedao.CloudNodeDAO
    symbolProvider SymbolProvider
}

// GetMatchingNodes 通用的节点匹配逻辑（三种分配策略）
func (b *BaseDistributor) GetMatchingNodes(ctx, rule, dataType) ([]*CloudNodeDTO, error)

// ParseCollectParams 解析采集参数 JSON
func (b *BaseDistributor) ParseCollectParams(params string) (*CollectParams, error)
```

### 6.2 KlineDistributor（K线分配器）

```go
type KlineDistributor struct {
    base *BaseDistributor
}

func (d *KlineDistributor) GetDataType() string {
    return "kline"
}

func (d *KlineDistributor) GetTargetObjects(ctx, rule) ([]string, error) {
    // 1. 从规则参数解析 objects
    params := d.base.ParseCollectParams(rule.CollectParams)
    objectsFromRule := params.Objects

    // 2. 从 SymbolProvider 获取动态标的（可选）
    objectsFromProvider := d.base.symbolProvider.GetSymbols(ctx, rule.DataSource)

    // 3. 合并去重
    return mergeUnique(objectsFromRule, objectsFromProvider), nil
}

func (d *KlineDistributor) BuildTaskParams(ctx, rule, object) (string, error) {
    params := d.base.ParseCollectParams(rule.CollectParams)

    taskParams := TaskParams{
        Symbol:     object,
        Intervals:  params.Intervals,
        Limit:      params.Limit,
        DataSource: rule.DataSource,
    }

    return json.Marshal(taskParams)
}

func (d *KlineDistributor) GetMatchingNodes(ctx, rule) ([]*CloudNodeDTO, error) {
    return d.base.GetMatchingNodes(ctx, rule, d.GetDataType())
}
```

### 6.3 NewsDistributor（新闻分配器）

```go
type NewsDistributor struct {
    base *BaseDistributor
}

func (d *NewsDistributor) GetDataType() string {
    return "news"
}

func (d *NewsDistributor) GetTargetObjects(ctx, rule) ([]string, error) {
    // 新闻不按标的拆分，返回空数组
    // 统一处理逻辑会生成一个 symbol="" 的实例
    return nil, nil
}

func (d *NewsDistributor) BuildTaskParams(ctx, rule, object) (string, error) {
    params := d.base.ParseCollectParams(rule.CollectParams)

    taskParams := TaskParams{
        Sources:    params.Sources,
        Keywords:   params.Keywords,
        DataSource: rule.DataSource,
    }

    return json.Marshal(taskParams)
}

func (d *NewsDistributor) GetMatchingNodes(ctx, rule) ([]*CloudNodeDTO, error) {
    return d.base.GetMatchingNodes(ctx, rule, d.GetDataType())
}
```

### 6.4 其他分配器

| 分配器                  | 数据类型      | GetTargetObjects | 说明       |
|----------------------|-----------|------------------|----------|
| TickerDistributor    | ticker    | 返回交易对列表          | 类似 Kline |
| OrderbookDistributor | orderbook | 返回交易对列表          | 类似 Kline |
| TradeDistributor     | trade     | 返回交易对列表          | 类似 Kline |
| ListDistributor      | list      | 返回空              | 类似 News  |

---

## 七、触发机制

### 7.1 用户操作触发

**修改文件:** `collectmgr/impl_task_rules.go`

```go
type TaskRulesServiceImpl struct {
    taskRulesDAO collectmgrdao.CollectorTaskRulesDAO
    nodeDAO      cloudnodedao.CloudNodeDAO
    taskPlanner  TaskPlannerService  // 新增
    snowflake    *snowflake.Node
}

// CreateTaskRule 创建任务规则
func (s *TaskRulesServiceImpl) CreateTaskRule(ctx, rule) (string, error) {
    // 1. 创建规则记录
    ruleID, err := s.createRule(ctx, rule)
    if err != nil {
        return "", err
    }

    // 2. 如果启用，立即同步实例
    if rule.Enabled == "true" {
        if _, err := s.taskPlanner.SyncRuleInstances(ctx, ruleID); err != nil {
            log.Warnf("sync instances failed: %v", err)
            // 不影响规则创建，定时任务会兜底
        }
    }

    return ruleID, nil
}

// UpdateTaskRule 更新任务规则
func (s *TaskRulesServiceImpl) UpdateTaskRule(ctx, rule) error {
    // 1. 更新规则记录
    if err := s.updateRule(ctx, rule); err != nil {
        return err
    }

    // 2. 根据启用状态处理实例
    if rule.Enabled == "true" {
        if _, err := s.taskPlanner.SyncRuleInstances(ctx, rule.RuleID); err != nil {
            log.Warnf("sync instances failed: %v", err)
        }
    } else {
        if err := s.taskPlanner.InvalidateRuleInstances(ctx, rule.RuleID); err != nil {
            log.Warnf("invalidate instances failed: %v", err)
        }
    }

    return nil
}

// DisableTaskRule 禁用任务规则
func (s *TaskRulesServiceImpl) DisableTaskRule(ctx, ruleID) error {
    // 1. 禁用规则
    if err := s.disableRule(ctx, ruleID); err != nil {
        return err
    }

    // 2. 使所有实例失效
    if err := s.taskPlanner.InvalidateRuleInstances(ctx, ruleID); err != nil {
        log.Warnf("invalidate instances failed: %v", err)
    }

    return nil
}
```

### 7.2 定时任务触发

**新建文件:** `collectmgr/timer_task_sync.go`

```go
package collectmgr

import (
    "context"
    "sync"

    "trpc.group/trpc-go/trpc-go/log"
)

var (
    globalTaskSyncInstance *TaskSyncScheduler
    taskSyncOnce           sync.Once
)

// TaskSyncScheduler 任务同步调度器
type TaskSyncScheduler struct {
    taskPlanner TaskPlannerService
}

// InitTaskSyncInstance 初始化全局实例（供 bootstrap 调用）
func InitTaskSyncInstance(taskPlanner TaskPlannerService) {
    taskSyncOnce.Do(func() {
        log.Info("[TaskSync] Initializing global task sync instance...")
        globalTaskSyncInstance = &TaskSyncScheduler{
            taskPlanner: taskPlanner,
        }
        log.Info("[TaskSync] Global task sync instance initialized")
    })
}

// TaskSyncSchedule trpc定时器[入口函数] - 定时同步任务实例
func TaskSyncSchedule(ctx context.Context, params string) error {
    log.InfoContextf(ctx, "[TaskSync] Starting task sync schedule, params: %s", params)

    if globalTaskSyncInstance == nil {
        err := fmt.Errorf("task sync instance not initialized")
        log.ErrorContextf(ctx, "[TaskSync] %v", err)
        return err
    }

    result, err := globalTaskSyncInstance.taskPlanner.SyncAllEnabledRules(ctx)
    if err != nil {
        log.ErrorContextf(ctx, "[TaskSync] sync failed: %v", err)
        return err
    }

    log.InfoContextf(ctx, "[TaskSync] completed: total=%d, synced=%d, created=%d, updated=%d, deleted=%d",
        result.TotalRules, result.SyncedRules,
        result.TotalCreated, result.TotalUpdated, result.TotalDeleted)

    return nil
}
```

**修改文件:** `bootstrap/bootstrap.go`

```go
// 在 Initialize 函数中添加

// 5. 注册采集任务同步定时器
timer.RegisterScheduler("taskSyncSchedule", &timer.DefaultScheduler{})
timer.RegisterHandlerService(s.Service("trpc.taskSync.timer"), collectmgr.TaskSyncSchedule)
```

---

## 八、DAO 层扩展

### 8.1 CloudNodeDAO 扩展

**文件:** `cloudnode/dao/cloud_node.go`

```go
// 新增方法

// GetNodesBySupportedCollector 获取支持指定采集器类型的节点
// SQL: WHERE c_supported_collectors LIKE '%"kline"%' AND c_invalid = 0
func (d *cloudNodeDaoImpl) GetNodesBySupportedCollector(ctx context.Context, collectorType string) ([]*model.CloudNode, error)

// GetNodesByPattern 根据节点ID通配符匹配获取节点
// SQL: WHERE c_node_id LIKE 'scf-collector-%' AND c_invalid = 0
func (d *cloudNodeDaoImpl) GetNodesByPattern(ctx context.Context, pattern string) ([]*model.CloudNode, error)

// GetNodesByIDs 根据节点ID列表获取节点
// SQL: WHERE c_node_id IN (...) AND c_invalid = 0
func (d *cloudNodeDaoImpl) GetNodesByIDs(ctx context.Context, nodeIDs []string) ([]*model.CloudNode, error)
```

### 8.2 CollectorTaskInstanceDAO 扩展

**文件:** `collectmgr/dao/collector_task_instance.go`

```go
// 新增方法

// GetActiveInstancesByRule 获取规则的有效实例
// SQL: WHERE c_rule_id = ? AND c_invalid = 0
func (d *daoImpl) GetActiveInstancesByRule(ctx context.Context, ruleID string) ([]*model.CollectorTaskInstance, error)

// InvalidateInstancesByRule 批量标记规则实例为无效
// SQL: UPDATE ... SET c_invalid = 1 WHERE c_rule_id = ? AND c_invalid = 0
func (d *daoImpl) InvalidateInstancesByRule(ctx context.Context, ruleID string) error

// BatchUpdateParams 批量更新实例参数
// 输入: [{ID, TaskParams}, ...]
func (d *daoImpl) BatchUpdateParams(ctx context.Context, updates []InstanceParamUpdate) error

// BatchInvalidate 批量使实例失效
// SQL: UPDATE ... SET c_invalid = 1 WHERE c_task_id IN (...)
func (d *daoImpl) BatchInvalidate(ctx context.Context, taskIDs []string) error
```

---

## 九、数据库变更

```sql
-- 1. 为任务实例表添加 symbol 字段
ALTER TABLE t_collector_task_instances
ADD COLUMN c_symbol TEXT NOT NULL DEFAULT '';

-- 2. 添加复合唯一索引（规则+节点+标的，仅有效记录）
-- 注意：SQLite 不支持部分索引的 WHERE 子句，需要使用触发器或应用层保证
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_task_instances_rule_node_symbol
ON t_collector_task_instances(c_rule_id, c_node_id, c_symbol);

-- 3. 删除原有可能冲突的索引（如有）
-- DROP INDEX IF EXISTS idx_xxx;
```

---

## 十、目录结构（重命名后）

```
internal/service/collectmgr/
├── api/                                    # API 处理层
│   ├── router.go
│   ├── types.go
│   ├── collector_task_rule_handler.go
│   ├── collector_task_instance_handler.go
│   └── collector_data_type_config_handler.go
│
├── dao/                                    # 数据访问层
│   ├── collector_task_rules.go
│   ├── collector_task_instance.go          # 扩展
│   ├── collector_data_type_configs.go
│   └── collector_field_configs.go
│
├── model/                                  # 数据模型
│   ├── constants.go                        # 修改：添加常量
│   ├── collector_task_rules.go
│   ├── collector_task_instance.go          # 修改：添加 Symbol 字段
│   ├── collector_data_type_configs.go
│   └── collector_field_configs.go
│
├── distributor/                            # 【新建】分配器模块
│   ├── interface.go                        # 接口定义
│   ├── registry.go                         # 注册中心
│   ├── base_distributor.go                 # 基础分配器
│   ├── symbol_provider.go                  # 标的提供者
│   ├── kline_distributor.go                # K线分配器
│   ├── ticker_distributor.go               # Ticker分配器
│   ├── orderbook_distributor.go            # OrderBook分配器
│   ├── trade_distributor.go                # Trade分配器
│   └── news_distributor.go                 # 新闻分配器
│
├── gateway/                                # 网关层
│   ├── handler.go
│   └── register.go
│
├── service.go                              # 服务接口：添加 TaskPlannerService
├── types.go                                # 业务类型：添加 CollectParams, TaskParams 等
├── impl_task_rules.go                      # 修改：集成 TaskPlanner
├── impl_task_instance.go
├── impl_data_type_config.go
├── impl_task_planner.go                    # 【重写】任务规划器实现
└── timer_task_sync.go                      # 【新建】定时任务
```

---

## 十一、文件清单

### 11.1 新建文件（9个）

| 文件路径                                            | 说明                |
|-------------------------------------------------|-------------------|
| collectmgr/distributor/interface.go             | 分配器接口、标的提供者接口定义   |
| collectmgr/distributor/registry.go              | 分配器注册中心           |
| collectmgr/distributor/base_distributor.go      | 基础分配器（通用逻辑）       |
| collectmgr/distributor/symbol_provider.go       | 默认标的提供者实现         |
| collectmgr/distributor/kline_distributor.go     | K线分配器             |
| collectmgr/distributor/ticker_distributor.go    | Ticker分配器         |
| collectmgr/distributor/orderbook_distributor.go | OrderBook分配器      |
| collectmgr/distributor/trade_distributor.go     | Trade分配器          |
| collectmgr/distributor/news_distributor.go      | 新闻分配器             |
| collectmgr/timer_task_sync.go                   | 定时同步任务            |

### 11.2 修改文件（8个）

| 文件路径                                        | 修改内容                                    |
|---------------------------------------------|-------------------------------------------|
| collectmgr/model/constants.go               | 添加数据类型、分配类型、状态常量                        |
| collectmgr/model/collector_task_instance.go | 添加 Symbol 字段                             |
| collectmgr/dao/collector_task_instance.go   | 添加增量更新相关方法（4个）                           |
| collectmgr/service.go                       | 添加 TaskPlannerService 接口                 |
| collectmgr/types.go                         | 添加 CollectParams、TaskParams、SyncResult 等类型 |
| collectmgr/impl_task_planner.go             | 重写：实现任务规划器核心逻辑                          |
| collectmgr/impl_task_rules.go               | 集成 TaskPlanner，在 CRUD 时触发同步              |
| cloudnode/dao/cloud_node.go                 | 添加节点查询方法（3个）                             |
| bootstrap/bootstrap.go                      | 注册定时任务                                  |

### 11.3 目录重命名

`internal/service/collector` → `internal/service/collectmgr`

需要同步修改所有引用该包的 import 路径。

---

## 十二、实现顺序

### 阶段一：准备工作
- **Step 1:** 重命名目录 `collector` → `collectmgr`
- **Step 2:** 更新所有 import 路径
- **Step 3:** 验证编译通过

### 阶段二：基础设施
- **Step 4:** 更新 `model/constants.go`（添加常量）
- **Step 5:** 更新 `model/collector_task_instance.go`（添加 Symbol 字段）
- **Step 6:** 执行数据库变更（添加字段和索引）
- **Step 7:** 扩展 `cloudnode/dao/cloud_node.go`（3个方法）
- **Step 8:** 扩展 `collectmgr/dao/collector_task_instance.go`（4个方法）

### 阶段三：分配器模块
- **Step 9:** 创建 `distributor/interface.go`（接口定义）
- **Step 10:** 创建 `distributor/symbol_provider.go`（标的提供者）
- **Step 11:** 创建 `distributor/registry.go`（注册中心）
- **Step 12:** 创建 `distributor/base_distributor.go`（基础能力）
- **Step 13:** 创建 `distributor/kline_distributor.go`
- **Step 14:** 创建 `distributor/news_distributor.go`
- **Step 15:** 创建其他分配器（ticker/orderbook/trade）

### 阶段四：规划器实现
- **Step 16:** 更新 `types.go`（添加新类型）
- **Step 17:** 更新 `service.go`（添加 TaskPlannerService 接口）
- **Step 18:** 重写 `impl_task_planner.go`（核心同步逻辑）
- **Step 19:** 修改 `impl_task_rules.go`（集成规划器）

### 阶段五：定时任务
- **Step 20:** 创建 `timer_task_sync.go`
- **Step 21:** 修改 `bootstrap/bootstrap.go`（注册定时任务）
- **Step 22:** 配置 `trpc_go.yaml`（添加定时器配置）

### 阶段六：测试验证
- **Step 23:** 编译验证
- **Step 24:** 单元测试
- **Step 25:** 集成测试

---

## 十三、扩展性说明

### 添加新的数据类型分配器

1. 创建 `collectmgr/distributor/xxx_distributor.go`
2. 实现 `TaskDistributor` 接口
3. 在 `registry.go` 的 `NewDistributorRegistry()` 中注册
4. 完成

### 添加新的标的来源

1. 实现新的 `SymbolProvider`（如 `ExchangeAPISymbolProvider`）
2. 在创建分配器时注入
3. 完成