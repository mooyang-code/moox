# MooX 心跳管理模块设计方案 V2.0

## 重要说明

**本模块职责**: 心跳模块专注于节点健康监控和状态管理，**不包含告警功能**。

**告警集成**: 当检测到节点异常时，通过调用独立的告警服务接口触发告警。详见 `alert.md` 和 `HEARTBEAT_ALERT_SEPARATION.md`。

---

## 1. 概览

### 1.1 功能定位

心跳管理模块是一个通用的节点健康监控系统，支持：
- 接收和记录各类节点的心跳数据
- 实时监控节点在线状态
- 主动探测超时节点
- 提供节点统计和健康分析
- 集成告警服务进行异常通知

### 1.2 核心特性

1. **通用性**: 支持多种节点类型（云函数、服务器、容器、K8s Pod等）
2. **可扩展性**: 通过适配器模式支持不同节点的探测策略
3. **高性能**: 基于内存缓存的高效状态检查
4. **灵活配置**: 支持节点级别的个性化配置
5. **告警集成**: 无缝对接告警服务，实现异常通知

### 1.3 架构图

```
┌─────────────────────────────────────────────────────────────┐
│                        业务服务层                             │
│  (CloudNode Service, K8s Service, Container Service, etc.)  │
└─────────────────────┬───────────────────────────────────────┘
                      │ 定期上报心跳
                      ↓
┌─────────────────────────────────────────────────────────────┐
│                    心跳管理服务 (Heartbeat Service)           │
├─────────────────────────────────────────────────────────────┤
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │  心跳接收器   │  │  状态监控器   │  │  主动探测器   │      │
│  │  (Receiver)  │  │  (Monitor)   │  │  (Prober)    │      │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘      │
│         │                  │                  │              │
│         ↓                  ↓                  ↓              │
│  ┌──────────────────────────────────────────────────┐       │
│  │              内存缓存 (Cache)                      │       │
│  └──────────────────────────────────────────────────┘       │
│         │                  │                  │              │
│         ↓                  ↓                  ↓              │
│  ┌──────────────────────────────────────────────────┐       │
│  │              数据访问层 (DAO)                      │       │
│  └──────────────────┬───────────────────────────────┘       │
└────────────────────┼────────────────────────────────────────┘
                     │                           │
                     ↓                           ↓
         ┌────────────────────┐      ┌────────────────────┐
         │   SQLite Database  │      │   告警服务 (Alert)  │
         └────────────────────┘      └────────────────────┘
```

---

## 2. 目录结构

```
internal/service/heartbeat/
├── README.md                           # 模块说明文档
├── interface.go                        # 服务接口定义
│
├── types/                              # 类型定义
│   ├── heartbeat.go                    # 心跳相关类型
│   ├── node.go                         # 节点相关类型
│   ├── probe.go                        # 探测相关类型
│   ├── statistics.go                   # 统计相关类型
│   └── filter.go                       # 过滤器类型
│
├── model/                              # 数据模型
│   ├── heartbeat_record.go             # 心跳记录模型
│   ├── probe_log.go                    # 探测日志模型
│   └── heartbeat_history.go            # 心跳历史模型
│
├── dao/                                # 数据访问层
│   ├── heartbeat_record_dao.go         # 心跳记录DAO
│   ├── probe_log_dao.go                # 探测日志DAO
│   └── heartbeat_history_dao.go        # 心跳历史DAO
│
├── impl/                               # 服务实现
│   ├── service_impl.go                 # 服务主实现
│   ├── receiver.go                     # 心跳接收器
│   ├── monitor.go                      # 状态监控器
│   ├── prober.go                       # 主动探测器
│   ├── cache.go                        # 内存缓存
│   └── statistics.go                   # 统计分析
│
├── adapter/                            # 探测适配器
│   ├── adapter.go                      # 适配器接口
│   ├── registry.go                     # 适配器注册表
│   ├── scf_adapter.go                  # 云函数适配器
│   ├── http_adapter.go                 # HTTP服务适配器
│   ├── tcp_adapter.go                  # TCP服务适配器
│   └── k8s_adapter.go                  # K8s Pod适配器
│
├── strategy/                           # 探测策略
│   ├── strategy.go                     # 策略接口
│   ├── registry.go                     # 策略注册表
│   ├── default_strategy.go             # 默认策略
│   ├── aggressive_strategy.go          # 激进策略
│   └── conservative_strategy.go        # 保守策略
│
├── api/                                # HTTP API层
│   ├── router.go                       # 路由注册
│   ├── heartbeat_handler.go            # 心跳接口
│   ├── node_handler.go                 # 节点管理接口
│   ├── statistics_handler.go           # 统计接口
│   └── types.go                        # API请求/响应类型
│
├── gateway/                            # tRPC网关层
│   ├── handler.go                      # tRPC处理器
│   └── register.go                     # 服务注册
│
└── config/                             # 配置
    └── config.go                       # 配置定义
```

---

## 3. 数据库设计

### 3.1 心跳记录表 (t_heartbeat_records)

存储节点的最新心跳状态和统计信息。

```sql
-- ************ 心跳记录表 ************
-- 说明: 存储每个节点的最新心跳状态和统计信息
-- 特点: 每个节点只有一条记录，持续更新

CREATE TABLE IF NOT EXISTS t_heartbeat_records (
    -- 主键
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,

    -- 节点标识
    c_node_id TEXT NOT NULL,                       -- 节点ID
    c_node_type TEXT NOT NULL,                     -- 节点类型: scf/server/container/k8s
    c_source_service TEXT NOT NULL DEFAULT '',     -- 来源服务名称

    -- 状态信息
    c_status INTEGER NOT NULL DEFAULT 0,           -- 状态: 0=离线 1=在线 2=超时 3=异常
    c_last_heartbeat DATETIME,                     -- 最后心跳时间
    c_first_heartbeat DATETIME,                    -- 首次心跳时间

    -- 心跳配置
    c_heartbeat_interval INTEGER DEFAULT 10,       -- 心跳间隔(秒)
    c_timeout_threshold INTEGER DEFAULT 30,        -- 超时阈值(秒)

    -- 统计信息
    c_consecutive_timeouts INTEGER DEFAULT 0,     -- 连续超时次数
    c_total_timeouts INTEGER DEFAULT 0,           -- 总超时次数
    c_total_heartbeats INTEGER DEFAULT 0,         -- 总心跳次数
    c_uptime_seconds INTEGER DEFAULT 0,           -- 在线时长(秒)
    c_availability_rate REAL DEFAULT 100.0,       -- 可用率(%)

    -- 附加数据
    c_metrics TEXT DEFAULT '{}',                  -- 节点指标(JSON): CPU/内存/磁盘等
    c_metadata TEXT DEFAULT '{}',                 -- 元数据(JSON): 版本/标签等

    -- 探测配置
    c_probe_enabled INTEGER DEFAULT 1,            -- 是否启用探测: 0=禁用 1=启用
    c_probe_url TEXT DEFAULT '',                  -- 探测URL/地址
    c_probe_strategy TEXT DEFAULT 'default',      -- 探测策略
    c_last_probe_time DATETIME,                   -- 最后探测时间
    c_last_probe_result INTEGER DEFAULT 0,        -- 最后探测结果: 0=失败 1=成功

    -- 通用字段
    c_invalid INTEGER NOT NULL DEFAULT 0,         -- 软删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,   -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,   -- 修改时间

    UNIQUE (c_node_id, c_node_type, c_invalid)
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_heartbeat_records_node
    ON t_heartbeat_records(c_node_id, c_node_type);
CREATE INDEX IF NOT EXISTS idx_heartbeat_records_status
    ON t_heartbeat_records(c_status);
CREATE INDEX IF NOT EXISTS idx_heartbeat_records_last_heartbeat
    ON t_heartbeat_records(c_last_heartbeat);
CREATE INDEX IF NOT EXISTS idx_heartbeat_records_source
    ON t_heartbeat_records(c_source_service);
CREATE INDEX IF NOT EXISTS idx_heartbeat_records_type
    ON t_heartbeat_records(c_node_type);

-- 触发器: 自动更新 c_mtime
CREATE TRIGGER IF NOT EXISTS update_heartbeat_records_mtime
AFTER UPDATE ON t_heartbeat_records
BEGIN
    UPDATE t_heartbeat_records
    SET c_mtime = CURRENT_TIMESTAMP
    WHERE rowid = NEW.rowid;
END;
```

### 3.2 探测日志表 (t_heartbeat_probe_logs)

记录主动探测的详细日志。

```sql
-- ************ 探测日志表 ************
-- 说明: 记录主动探测的详细日志
-- 特点: 追加写入，用于分析和调试

CREATE TABLE IF NOT EXISTS t_heartbeat_probe_logs (
    -- 主键
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,

    -- 探测标识
    c_probe_id TEXT NOT NULL,                     -- 探测ID(UUID)
    c_node_id TEXT NOT NULL,                      -- 节点ID
    c_node_type TEXT NOT NULL,                    -- 节点类型

    -- 探测信息
    c_probe_time DATETIME NOT NULL,               -- 探测时间
    c_probe_url TEXT DEFAULT '',                  -- 探测地址
    c_probe_method TEXT DEFAULT 'GET',            -- 探测方法
    c_probe_timeout INTEGER DEFAULT 5,            -- 探测超时(秒)

    -- 探测结果
    c_result INTEGER NOT NULL DEFAULT 0,          -- 探测结果: 0=失败 1=成功
    c_status_code INTEGER DEFAULT 0,              -- HTTP状态码
    c_response_time INTEGER DEFAULT 0,            -- 响应时间(ms)
    c_error_message TEXT DEFAULT '',              -- 错误信息

    -- 探测详情
    c_adapter_type TEXT DEFAULT '',               -- 适配器类型
    c_strategy_name TEXT DEFAULT '',              -- 策略名称
    c_request_details TEXT DEFAULT '{}',          -- 请求详情(JSON)
    c_response_details TEXT DEFAULT '{}',         -- 响应详情(JSON)

    -- 通用字段
    c_invalid INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_probe_logs_node
    ON t_heartbeat_probe_logs(c_node_id, c_node_type);
CREATE INDEX IF NOT EXISTS idx_probe_logs_time
    ON t_heartbeat_probe_logs(c_probe_time);
CREATE INDEX IF NOT EXISTS idx_probe_logs_result
    ON t_heartbeat_probe_logs(c_result);
CREATE INDEX IF NOT EXISTS idx_probe_logs_probe_id
    ON t_heartbeat_probe_logs(c_probe_id);
```

---

## 4. 接口设计

### 4.1 服务接口 (Service Interface)

```go
package heartbeat

import (
    "context"
    "time"

    "github.com/mooyang-code/moox/server/internal/service/heartbeat/types"
)

// Service 心跳服务接口
type Service interface {
    // ========== 心跳上报 ==========

    // ReportHeartbeat 上报心跳
    ReportHeartbeat(ctx context.Context, req *types.ReportHeartbeatRequest) error

    // BatchReportHeartbeat 批量上报心跳
    BatchReportHeartbeat(ctx context.Context, req *types.BatchReportHeartbeatRequest) error

    // ========== 节点管理 ==========

    // RegisterNode 注册节点
    RegisterNode(ctx context.Context, req *types.RegisterNodeRequest) (*types.HeartbeatRecord, error)

    // UnregisterNode 注销节点
    UnregisterNode(ctx context.Context, nodeID, nodeType string) error

    // GetNode 获取节点信息
    GetNode(ctx context.Context, nodeID, nodeType string) (*types.HeartbeatRecord, error)

    // ListNodes 列出节点
    ListNodes(ctx context.Context, filter *types.NodeFilter) ([]*types.HeartbeatRecord, int64, error)

    // UpdateNodeConfig 更新节点配置
    UpdateNodeConfig(ctx context.Context, req *types.UpdateNodeConfigRequest) error

    // ========== 探测管理 ==========

    // ProbeNode 手动探测节点
    ProbeNode(ctx context.Context, nodeID, nodeType string) (*types.ProbeResult, error)

    // GetProbeLog 获取探测日志
    GetProbeLog(ctx context.Context, probeID string) (*types.ProbeLog, error)

    // ListProbeLogs 列出探测日志
    ListProbeLogs(ctx context.Context, filter *types.ProbeLogFilter) ([]*types.ProbeLog, int64, error)

    // ========== 历史记录 ==========

    // GetStatusHistory 获取状态历史
    GetStatusHistory(ctx context.Context, nodeID, nodeType string, timeRange *types.TimeRange) ([]*types.HeartbeatHistory, error)

    // ========== 服务控制 ==========

    // Start 启动服务
    Start(ctx context.Context) error

    // Stop 停止服务
    Stop(ctx context.Context) error

    // GetStatus 获取服务状态
    GetStatus(ctx context.Context) (*types.ServiceStatus, error)
}
```

---

## 5. 核心类型定义

### 5.1 心跳相关类型 (types/heartbeat.go)

```go
package types

import "time"

// NodeStatus 节点状态
type NodeStatus int

const (
    NodeStatusOffline NodeStatus = 0 // 离线
    NodeStatusOnline  NodeStatus = 1 // 在线
    NodeStatusTimeout NodeStatus = 2 // 超时
    NodeStatusAbnormal NodeStatus = 3 // 异常
)

// NodeType 节点类型
const (
    NodeTypeSCF       = "scf"       // 云函数
    NodeTypeServer    = "server"    // 服务器
    NodeTypeContainer = "container" // 容器
    NodeTypeK8s       = "k8s"       // K8s Pod
    NodeTypeCustom    = "custom"    // 自定义
)

// HeartbeatRecord 心跳记录
type HeartbeatRecord struct {
    ID              int64                  `json:"id"`
    NodeID          string                 `json:"node_id"`
    NodeType        string                 `json:"node_type"`
    SourceService   string                 `json:"source_service"`
    Status          NodeStatus             `json:"status"`
    LastHeartbeat   *time.Time             `json:"last_heartbeat"`
    FirstHeartbeat  *time.Time             `json:"first_heartbeat"`

    // 配置
    HeartbeatInterval int                  `json:"heartbeat_interval"`
    TimeoutThreshold  int                  `json:"timeout_threshold"`

    // 统计
    ConsecutiveTimeouts int                `json:"consecutive_timeouts"`
    TotalTimeouts       int                `json:"total_timeouts"`
    TotalHeartbeats     int                `json:"total_heartbeats"`
    UptimeSeconds       int                `json:"uptime_seconds"`
    AvailabilityRate    float64            `json:"availability_rate"`

    // 附加数据
    Metrics         map[string]interface{} `json:"metrics"`
    Metadata        map[string]interface{} `json:"metadata"`

    // 探测配置
    ProbeEnabled    bool                   `json:"probe_enabled"`
    ProbeURL        string                 `json:"probe_url"`
    ProbeStrategy   string                 `json:"probe_strategy"`
    LastProbeTime   *time.Time             `json:"last_probe_time"`
    LastProbeResult bool                   `json:"last_probe_result"`

    // 时间戳
    CreatedAt       time.Time              `json:"created_at"`
    UpdatedAt       time.Time              `json:"updated_at"`
}

// ReportHeartbeatRequest 上报心跳请求
type ReportHeartbeatRequest struct {
    NodeID          string                 `json:"node_id" binding:"required"`
    NodeType        string                 `json:"node_type" binding:"required"`
    SourceService   string                 `json:"source_service"`
    Timestamp       *time.Time             `json:"timestamp"`
    Metrics         map[string]interface{} `json:"metrics"`
    Metadata        map[string]interface{} `json:"metadata"`
}

// BatchReportHeartbeatRequest 批量上报心跳请求
type BatchReportHeartbeatRequest struct {
    Heartbeats []ReportHeartbeatRequest `json:"heartbeats" binding:"required"`
}

// RegisterNodeRequest 注册节点请求
type RegisterNodeRequest struct {
    NodeID            string                 `json:"node_id" binding:"required"`
    NodeType          string                 `json:"node_type" binding:"required"`
    SourceService     string                 `json:"source_service"`
    HeartbeatInterval int                    `json:"heartbeat_interval"`
    TimeoutThreshold  int                    `json:"timeout_threshold"`
    ProbeEnabled      bool                   `json:"probe_enabled"`
    ProbeURL          string                 `json:"probe_url"`
    ProbeStrategy     string                 `json:"probe_strategy"`
    Metadata          map[string]interface{} `json:"metadata"`
}

// UpdateNodeConfigRequest 更新节点配置请求
type UpdateNodeConfigRequest struct {
    NodeID            string  `json:"node_id" binding:"required"`
    NodeType          string  `json:"node_type" binding:"required"`
    HeartbeatInterval *int    `json:"heartbeat_interval"`
    TimeoutThreshold  *int    `json:"timeout_threshold"`
    ProbeEnabled      *bool   `json:"probe_enabled"`
    ProbeURL          *string `json:"probe_url"`
    ProbeStrategy     *string `json:"probe_strategy"`
}

// NodeFilter 节点过滤器
type NodeFilter struct {
    NodeIDs       []string     `json:"node_ids"`
    NodeTypes     []string     `json:"node_types"`
    SourceService *string      `json:"source_service"`
    Status        *NodeStatus  `json:"status"`
    ProbeEnabled  *bool        `json:"probe_enabled"`
    Keyword       string       `json:"keyword"`
    Page          int          `json:"page"`
    PageSize      int          `json:"page_size"`
    SortBy        string       `json:"sort_by"`
    SortOrder     string       `json:"sort_order"`
}
```

### 5.2 探测相关类型 (types/probe.go)

```go
package types

import "time"

// ProbeLog 探测日志
type ProbeLog struct {
    ID              int64                  `json:"id"`
    ProbeID         string                 `json:"probe_id"`
    NodeID          string                 `json:"node_id"`
    NodeType        string                 `json:"node_type"`
    ProbeTime       time.Time              `json:"probe_time"`
    ProbeURL        string                 `json:"probe_url"`
    ProbeMethod     string                 `json:"probe_method"`
    ProbeTimeout    int                    `json:"probe_timeout"`
    Result          bool                   `json:"result"`
    StatusCode      int                    `json:"status_code"`
    ResponseTime    int                    `json:"response_time"`
    ErrorMessage    string                 `json:"error_message"`
    AdapterType     string                 `json:"adapter_type"`
    StrategyName    string                 `json:"strategy_name"`
    RequestDetails  map[string]interface{} `json:"request_details"`
    ResponseDetails map[string]interface{} `json:"response_details"`
    CreatedAt       time.Time              `json:"created_at"`
}

// ProbeResult 探测结果
type ProbeResult struct {
    ProbeID      string                 `json:"probe_id"`
    Success      bool                   `json:"success"`
    StatusCode   int                    `json:"status_code"`
    ResponseTime int                    `json:"response_time"`
    ErrorMessage string                 `json:"error_message"`
    Details      map[string]interface{} `json:"details"`
    ProbeTime    time.Time              `json:"probe_time"`
}

// ProbeLogFilter 探测日志过滤器
type ProbeLogFilter struct {
    NodeID    string      `json:"node_id"`
    NodeType  string      `json:"node_type"`
    ProbeID   string      `json:"probe_id"`
    Result    *bool       `json:"result"`
    StartTime *time.Time  `json:"start_time"`
    EndTime   *time.Time  `json:"end_time"`
    Page      int         `json:"page"`
    PageSize  int         `json:"page_size"`
}
```

### 5.3 统计相关类型 (types/statistics.go)

```go
package types

import "time"

// NodeStatistics 节点统计
type NodeStatistics struct {
    NodeID              string    `json:"node_id"`
    NodeType            string    `json:"node_type"`
    TotalHeartbeats     int       `json:"total_heartbeats"`
    TotalTimeouts       int       `json:"total_timeouts"`
    TotalProbes         int       `json:"total_probes"`
    SuccessfulProbes    int       `json:"successful_probes"`
    FailedProbes        int       `json:"failed_probes"`
    UptimeSeconds       int       `json:"uptime_seconds"`
    DowntimeSeconds     int       `json:"downtime_seconds"`
    AvailabilityRate    float64   `json:"availability_rate"`
    AvgResponseTime     float64   `json:"avg_response_time"`
    CurrentStatus       NodeStatus `json:"current_status"`
    LastHeartbeat       *time.Time `json:"last_heartbeat"`
    LastStatusChange    *time.Time `json:"last_status_change"`
}

// OverallStatistics 整体统计
type OverallStatistics struct {
    TotalNodes          int                `json:"total_nodes"`
    OnlineNodes         int                `json:"online_nodes"`
    OfflineNodes        int                `json:"offline_nodes"`
    TimeoutNodes        int                `json:"timeout_nodes"`
    AbnormalNodes       int                `json:"abnormal_nodes"`
    TotalHeartbeats     int                `json:"total_heartbeats"`
    TotalProbes         int                `json:"total_probes"`
    AvgAvailability     float64            `json:"avg_availability"`
    NodesByType         map[string]int     `json:"nodes_by_type"`
    NodesByStatus       map[string]int     `json:"nodes_by_status"`
    StatisticsTime      time.Time          `json:"statistics_time"`
}

// HealthReport 健康报告
type HealthReport struct {
    ReportTime          time.Time              `json:"report_time"`
    OverallHealth       string                 `json:"overall_health"` // healthy/warning/critical
    TotalNodes          int                    `json:"total_nodes"`
    HealthyNodes        int                    `json:"healthy_nodes"`
    UnhealthyNodes      int                    `json:"unhealthy_nodes"`
    AvgAvailability     float64                `json:"avg_availability"`
    TopIssues           []HealthIssue          `json:"top_issues"`
    NodeHealthDetails   []*NodeHealthDetail    `json:"node_health_details"`
}

// HealthIssue 健康问题
type HealthIssue struct {
    IssueType    string    `json:"issue_type"`
    Severity     string    `json:"severity"`
    AffectedNodes int      `json:"affected_nodes"`
    Description  string    `json:"description"`
}

// NodeHealthDetail 节点健康详情
type NodeHealthDetail struct {
    NodeID           string     `json:"node_id"`
    NodeType         string     `json:"node_type"`
    HealthStatus     string     `json:"health_status"` // healthy/warning/critical
    AvailabilityRate float64    `json:"availability_rate"`
    Issues           []string   `json:"issues"`
    LastHeartbeat    *time.Time `json:"last_heartbeat"`
}

// TimeRange 时间范围
type TimeRange struct {
    StartTime time.Time `json:"start_time"`
    EndTime   time.Time `json:"end_time"`
}

// StatisticsFilter 统计过滤器
type StatisticsFilter struct {
    NodeTypes     []string   `json:"node_types"`
    SourceService *string    `json:"source_service"`
    TimeRange     *TimeRange `json:"time_range"`
}

// HealthReportFilter 健康报告过滤器
type HealthReportFilter struct {
    NodeTypes     []string `json:"node_types"`
    MinAvailability *float64 `json:"min_availability"`
    IncludeDetails  bool     `json:"include_details"`
}

// HeartbeatHistory 心跳历史
type HeartbeatHistory struct {
    ID           int64                  `json:"id"`
    RecordID     int64                  `json:"record_id"`
    NodeID       string                 `json:"node_id"`
    NodeType     string                 `json:"node_type"`
    OldStatus    NodeStatus             `json:"old_status"`
    NewStatus    NodeStatus             `json:"new_status"`
    ChangeTime   time.Time              `json:"change_time"`
    ChangeReason string                 `json:"change_reason"`
    Snapshot     map[string]interface{} `json:"snapshot"`
    CreatedAt    time.Time              `json:"created_at"`
}

// ServiceStatus 服务状态
type ServiceStatus struct {
    Running         bool      `json:"running"`
    MonitorRunning  bool      `json:"monitor_running"`
    ProberRunning   bool      `json:"prober_running"`
    CacheSize       int       `json:"cache_size"`
    MonitoredNodes  int       `json:"monitored_nodes"`
    StartTime       time.Time `json:"start_time"`
    Uptime          int64     `json:"uptime"`
}
```

---

## 6. 核心组件实现

### 6.1 心跳接收器 (Receiver)

**职责**: 接收和处理心跳上报请求

**核心逻辑**:
```go
// impl/receiver.go

type Receiver struct {
    heartbeatDAO dao.HeartbeatRecordDAO
    historyDAO dao.HeartbeatHistoryDAO
    cache     *Cache
}

func (r *Receiver) HandleHeartbeat(ctx context.Context, req *types.ReportHeartbeatRequest) error {
    // 1. 查询或创建节点记录
    record, err := r.heartbeatDAO.GetByNode(ctx, req.NodeID, req.NodeType)
    if err != nil {
        // 首次心跳，创建记录
        record = r.createNewRecord(req)
    }

    // 2. 更新心跳数据
    now := time.Now()
    if req.Timestamp != nil {
        now = *req.Timestamp
    }

    oldStatus := record.Status
    record.LastHeartbeat = &now
    record.TotalHeartbeats++
    record.ConsecutiveTimeouts = 0
    record.Status = types.NodeStatusOnline

    if req.Metrics != nil {
        record.Metrics = req.Metrics
    }
    if req.Metadata != nil {
        record.Metadata = req.Metadata
    }

    // 3. 计算可用率
    r.calculateAvailability(record)

    // 4. 保存到数据库
    if err := r.heartbeatDAO.Update(ctx, record); err != nil {
        return err
    }

    // 5. 更新缓存
    r.cache.Set(record)

    // 6. 记录状态变更历史
    if oldStatus != record.Status {
        r.recordStatusChange(ctx, record, oldStatus, record.Status, "heartbeat_received")
    }

    return nil
}
```

### 6.2 状态监控器 (Monitor)

**职责**: 定期扫描节点状态，检测超时节点，触发告警

**核心逻辑**:
```go
// impl/monitor.go

type Monitor struct {
    heartbeatDAO    dao.HeartbeatRecordDAO
    alertService alert.Service  // 告警服务
    cache        *Cache
    config       *config.Config
    ticker       *time.Ticker
    stopCh       chan struct{}
}

func (m *Monitor) Start() {
    m.ticker = time.NewTicker(m.config.Monitor.ScanInterval)
    m.stopCh = make(chan struct{})

    go m.run()
}

func (m *Monitor) run() {
    for {
        select {
        case <-m.ticker.C:
            m.scanNodes(context.Background())
        case <-m.stopCh:
            return
        }
    }
}

func (m *Monitor) scanNodes(ctx context.Context) {
    // 1. 从缓存获取所有节点
    records := m.cache.GetAll()

    now := time.Now()
    for _, record := range records {
        // 2. 检查心跳超时
        if m.isTimeout(record, now) {
            m.handleTimeout(ctx, record)
        }
    }
}

func (m *Monitor) isTimeout(record *types.HeartbeatRecord, now time.Time) bool {
    if record.LastHeartbeat == nil {
        return true
    }

    elapsed := now.Sub(*record.LastHeartbeat).Seconds()
    return elapsed > float64(record.TimeoutThreshold)
}

func (m *Monitor) handleTimeout(ctx context.Context, record *types.HeartbeatRecord) {
    // 1. 更新超时状态
    oldStatus := record.Status
    record.Status = types.NodeStatusTimeout
    record.ConsecutiveTimeouts++
    record.TotalTimeouts++

    // 2. 保存到数据库
    m.heartbeatDAO.Update(ctx, record)
    m.cache.Set(record)

    // 3. 记录状态变更
    if oldStatus != record.Status {
        m.recordStatusChange(ctx, record, oldStatus, record.Status, "heartbeat_timeout")
    }

    // 4. 触发告警(调用告警服务)
    m.triggerTimeoutAlert(ctx, record)
}

func (m *Monitor) triggerTimeoutAlert(ctx context.Context, record *types.HeartbeatRecord) {
    if m.alertService == nil {
        return
    }

    // 调用告警服务触发告警
    m.alertService.TriggerAlert(ctx, &alert.TriggerAlertRequest{
        SourceService: "heartbeat",
        SourceType:    "node",
        SourceID:      record.NodeID,
        SourceName:    record.NodeID,
        AlertType:     "heartbeat_timeout",
        AlertLevel:    "warning",
        AlertTitle:    "节点心跳超时",
        AlertMessage:  fmt.Sprintf("节点 %s 已超过 %d 秒未上报心跳",
            record.NodeID, record.TimeoutThreshold),
        AlertDetails: map[string]interface{}{
            "node_id":            record.NodeID,
            "node_type":          record.NodeType,
            "last_heartbeat":     record.LastHeartbeat,
            "timeout_threshold":  record.TimeoutThreshold,
            "consecutive_timeouts": record.ConsecutiveTimeouts,
        },
        AlertTags: []string{"heartbeat", "timeout", record.NodeType},
    })
}
```

### 6.3 主动探测器 (Prober)

**职责**: 对超时节点进行主动探测，验证节点真实状态

**核心逻辑**:
```go
// impl/prober.go

type Prober struct {
    heartbeatDAO       dao.HeartbeatRecordDAO
    probeLogDAO     dao.ProbeLogDAO
    alertService    alert.Service
    adapterRegistry adapter.AdapterRegistry
    strategyRegistry strategy.StrategyRegistry
    cache           *Cache
    config          *config.Config
    ticker          *time.Ticker
    stopCh          chan struct{}
}

func (p *Prober) Start() {
    p.ticker = time.NewTicker(p.config.Prober.ScanInterval)
    p.stopCh = make(chan struct{})

    go p.run()
}

func (p *Prober) run() {
    for {
        select {
        case <-p.ticker.C:
            p.probeTimeoutNodes(context.Background())
        case <-p.stopCh:
            return
        }
    }
}

func (p *Prober) probeTimeoutNodes(ctx context.Context) {
    // 1. 获取超时节点
    records := p.cache.GetByStatus(types.NodeStatusTimeout)

    for _, record := range records {
        if !record.ProbeEnabled {
            continue
        }

        // 2. 执行探测
        go p.probeNode(ctx, record)
    }
}

func (p *Prober) probeNode(ctx context.Context, record *types.HeartbeatRecord) (*types.ProbeResult, error) {
    // 1. 选择适配器
    adapter := p.adapterRegistry.Get(record.NodeType)
    if adapter == nil {
        return nil, fmt.Errorf("adapter not found for type: %s", record.NodeType)
    }

    // 2. 选择策略
    strategy := p.strategyRegistry.Get(record.ProbeStrategy)
    if strategy == nil {
        strategy = p.strategyRegistry.Get("default")
    }

    // 3. 执行探测
    probeID := uuid.New().String()
    startTime := time.Now()

    result, err := adapter.Probe(ctx, &adapter.ProbeRequest{
        NodeID:   record.NodeID,
        NodeType: record.NodeType,
        ProbeURL: record.ProbeURL,
        Timeout:  strategy.GetTimeout(),
        Metadata: record.Metadata,
    })

    responseTime := time.Since(startTime).Milliseconds()

    // 4. 记录探测日志
    probeLog := &types.ProbeLog{
        ProbeID:      probeID,
        NodeID:       record.NodeID,
        NodeType:     record.NodeType,
        ProbeTime:    startTime,
        ProbeURL:     record.ProbeURL,
        ProbeMethod:  "AUTO",
        ProbeTimeout: strategy.GetTimeout(),
        Result:       err == nil && result.Success,
        StatusCode:   result.StatusCode,
        ResponseTime: int(responseTime),
        AdapterType:  record.NodeType,
        StrategyName: record.ProbeStrategy,
    }

    if err != nil {
        probeLog.ErrorMessage = err.Error()
    }

    p.probeLogDAO.Create(ctx, probeLog)

    // 5. 更新节点状态
    record.LastProbeTime = &startTime
    record.LastProbeResult = probeLog.Result

    oldStatus := record.Status
    if probeLog.Result {
        // 探测成功，但心跳仍超时，标记为异常
        record.Status = types.NodeStatusAbnormal
        p.triggerAbnormalAlert(ctx, record)
    } else {
        // 探测失败，确认离线
        record.Status = types.NodeStatusOffline
        p.triggerOfflineAlert(ctx, record)
    }

    p.heartbeatDAO.Update(ctx, record)
    p.cache.Set(record)

    if oldStatus != record.Status {
        p.recordStatusChange(ctx, record, oldStatus, record.Status, "probe_completed")
    }

    return &types.ProbeResult{
        ProbeID:      probeID,
        Success:      probeLog.Result,
        StatusCode:   probeLog.StatusCode,
        ResponseTime: probeLog.ResponseTime,
        ErrorMessage: probeLog.ErrorMessage,
        ProbeTime:    startTime,
    }, nil
}

func (p *Prober) triggerOfflineAlert(ctx context.Context, record *types.HeartbeatRecord) {
    if p.alertService == nil {
        return
    }

    p.alertService.TriggerAlert(ctx, &alert.TriggerAlertRequest{
        SourceService: "heartbeat",
        SourceType:    "node",
        SourceID:      record.NodeID,
        SourceName:    record.NodeID,
        AlertType:     "node_offline",
        AlertLevel:    "error",
        AlertTitle:    "节点离线",
        AlertMessage:  fmt.Sprintf("节点 %s 心跳超时且探测失败，确认离线", record.NodeID),
        AlertDetails: map[string]interface{}{
            "node_id":         record.NodeID,
            "node_type":       record.NodeType,
            "last_heartbeat":  record.LastHeartbeat,
            "last_probe_time": record.LastProbeTime,
            "probe_result":    record.LastProbeResult,
        },
        AlertTags: []string{"heartbeat", "offline", record.NodeType},
    })
}
```

### 6.4 内存缓存 (Cache)

**职责**: 缓存节点状态，提高查询性能

```go
// impl/cache.go

type Cache struct {
    mu    sync.RWMutex
    data  map[string]*types.HeartbeatRecord // key: nodeID:nodeType
    index *CacheIndex
}

type CacheIndex struct {
    byStatus map[types.NodeStatus][]*types.HeartbeatRecord
    byType   map[string][]*types.HeartbeatRecord
}

func NewCache() *Cache {
    return &Cache{
        data: make(map[string]*types.HeartbeatRecord),
        index: &CacheIndex{
            byStatus: make(map[types.NodeStatus][]*types.HeartbeatRecord),
            byType:   make(map[string][]*types.HeartbeatRecord),
        },
    }
}

func (c *Cache) Set(record *types.HeartbeatRecord) {
    c.mu.Lock()
    defer c.mu.Unlock()

    key := fmt.Sprintf("%s:%s", record.NodeID, record.NodeType)
    c.data[key] = record
    c.rebuildIndex()
}

func (c *Cache) Get(nodeID, nodeType string) *types.HeartbeatRecord {
    c.mu.RLock()
    defer c.mu.RUnlock()

    key := fmt.Sprintf("%s:%s", nodeID, nodeType)
    return c.data[key]
}

func (c *Cache) GetAll() []*types.HeartbeatRecord {
    c.mu.RLock()
    defer c.mu.RUnlock()

    records := make([]*types.HeartbeatRecord, 0, len(c.data))
    for _, record := range c.data {
        records = append(records, record)
    }
    return records
}

func (c *Cache) GetByStatus(status types.NodeStatus) []*types.HeartbeatRecord {
    c.mu.RLock()
    defer c.mu.RUnlock()

    return c.index.byStatus[status]
}
```

---

## 7. 适配器模式

### 7.1 适配器接口

```go
// adapter/adapter.go

package adapter

import "context"

// Adapter 探测适配器接口
type Adapter interface {
    // Name 适配器名称
    Name() string

    // Probe 执行探测
    Probe(ctx context.Context, req *ProbeRequest) (*ProbeResponse, error)
}

// ProbeRequest 探测请求
type ProbeRequest struct {
    NodeID   string
    NodeType string
    ProbeURL string
    Timeout  int
    Metadata map[string]interface{}
}

// ProbeResponse 探测响应
type ProbeResponse struct {
    Success      bool
    StatusCode   int
    ResponseTime int64
    Details      map[string]interface{}
}
```

### 7.2 HTTP 适配器示例

```go
// adapter/http_adapter.go

type HTTPAdapter struct {
    client *http.Client
}

func NewHTTPAdapter() *HTTPAdapter {
    return &HTTPAdapter{
        client: &http.Client{
            Timeout: 10 * time.Second,
        },
    }
}

func (a *HTTPAdapter) Name() string {
    return "http"
}

func (a *HTTPAdapter) Probe(ctx context.Context, req *ProbeRequest) (*ProbeResponse, error) {
    startTime := time.Now()

    httpReq, err := http.NewRequestWithContext(ctx, "GET", req.ProbeURL, nil)
    if err != nil {
        return nil, err
    }

    resp, err := a.client.Do(httpReq)
    responseTime := time.Since(startTime).Milliseconds()

    if err != nil {
        return &ProbeResponse{
            Success:      false,
            ResponseTime: responseTime,
        }, err
    }
    defer resp.Body.Close()

    return &ProbeResponse{
        Success:      resp.StatusCode >= 200 && resp.StatusCode < 300,
        StatusCode:   resp.StatusCode,
        ResponseTime: responseTime,
        Details: map[string]interface{}{
            "status": resp.Status,
        },
    }, nil
}
```

---

## 8. API 路由设计

### 8.1 HTTP API 路由

```go
// api/router.go

func RegisterRoutes(r *gin.RouterGroup, svc Service) {
    heartbeat := r.Group("/heartbeat")
    {
        // 心跳上报
        heartbeat.POST("/report", handleReportHeartbeat(svc))
        heartbeat.POST("/batch-report", handleBatchReportHeartbeat(svc))

        // 节点管理
        heartbeat.POST("/nodes/register", handleRegisterNode(svc))
        heartbeat.DELETE("/nodes/:node_id/:node_type", handleUnregisterNode(svc))
        heartbeat.GET("/nodes/:node_id/:node_type", handleGetNode(svc))
        heartbeat.GET("/nodes", handleListNodes(svc))
        heartbeat.PUT("/nodes/:node_id/:node_type/config", handleUpdateNodeConfig(svc))

        // 探测管理
        heartbeat.POST("/probe/:node_id/:node_type", handleProbeNode(svc))
        heartbeat.GET("/probe/logs/:probe_id", handleGetProbeLog(svc))
        heartbeat.GET("/probe/logs", handleListProbeLogs(svc))

        // 统计分析
        heartbeat.GET("/statistics/node/:node_id/:node_type", handleGetNodeStatistics(svc))
        heartbeat.GET("/statistics/overall", handleGetOverallStatistics(svc))
        heartbeat.GET("/health-report", handleGetHealthReport(svc))

        // 历史记录
        heartbeat.GET("/history/:node_id/:node_type", handleGetStatusHistory(svc))

        // 服务状态
        heartbeat.GET("/service/status", handleGetServiceStatus(svc))
    }
}
```

**API 路由清单**:

```
POST   /api/v1/heartbeat/report                          # 上报心跳
POST   /api/v1/heartbeat/batch-report                    # 批量上报心跳

POST   /api/v1/heartbeat/nodes/register                  # 注册节点
DELETE /api/v1/heartbeat/nodes/:node_id/:node_type       # 注销节点
GET    /api/v1/heartbeat/nodes/:node_id/:node_type       # 获取节点
GET    /api/v1/heartbeat/nodes                           # 列出节点
PUT    /api/v1/heartbeat/nodes/:node_id/:node_type/config # 更新节点配置

POST   /api/v1/heartbeat/probe/:node_id/:node_type       # 手动探测
GET    /api/v1/heartbeat/probe/logs/:probe_id            # 获取探测日志
GET    /api/v1/heartbeat/probe/logs                      # 列出探测日志

GET    /api/v1/heartbeat/statistics/node/:node_id/:node_type # 节点统计
GET    /api/v1/heartbeat/statistics/overall              # 整体统计
GET    /api/v1/heartbeat/health-report                   # 健康报告

GET    /api/v1/heartbeat/history/:node_id/:node_type     # 状态历史

GET    /api/v1/heartbeat/service/status                  # 服务状态
```

---

## 9. 配置设计

```go
// config/config.go

package config

import "time"

type Config struct {
    Monitor MonitorConfig `yaml:"monitor"`
    Prober  ProberConfig  `yaml:"prober"`
    Cache   CacheConfig   `yaml:"cache"`
    Alert   AlertConfig   `yaml:"alert"`
}

// MonitorConfig 监控器配置
type MonitorConfig struct {
    Enabled      bool          `yaml:"enabled"`       // 是否启用监控
    ScanInterval time.Duration `yaml:"scan_interval"` // 扫描间隔
}

// ProberConfig 探测器配置
type ProberConfig struct {
    Enabled      bool          `yaml:"enabled"`       // 是否启用探测
    ScanInterval time.Duration `yaml:"scan_interval"` // 扫描间隔
    MaxConcurrent int          `yaml:"max_concurrent"` // 最大并发探测数
}

// CacheConfig 缓存配置
type CacheConfig struct {
    Enabled bool `yaml:"enabled"` // 是否启用缓存
    TTL     time.Duration `yaml:"ttl"` // 缓存过期时间
}

// AlertConfig 告警集成配置
type AlertConfig struct {
    Enabled             bool   `yaml:"enabled"`               // 是否启用告警触发
    TimeoutAlertLevel   string `yaml:"timeout_alert_level"`   // 超时告警级别
    OfflineAlertLevel   string `yaml:"offline_alert_level"`   // 离线告警级别
    AbnormalAlertLevel  string `yaml:"abnormal_alert_level"`  // 异常告警级别
    ProbeFailedThreshold int   `yaml:"probe_failed_threshold"` // 探测失败N次后触发告警
}
```

**配置文件示例 (config.yaml)**:

```yaml
heartbeat:
  monitor:
    enabled: true
    scan_interval: 10s

  prober:
    enabled: true
    scan_interval: 30s
    max_concurrent: 10

  cache:
    enabled: true
    ttl: 1h

  # 告警集成配置
  alert:
    enabled: true
    timeout_alert_level: warning
    offline_alert_level: error
    abnormal_alert_level: warning
    probe_failed_threshold: 3
```

---

## 10. 使用示例

### 10.1 服务初始化

```go
package main

import (
    "github.com/mooyang-code/moox/server/internal/service/heartbeat"
    "github.com/mooyang-code/moox/server/internal/service/heartbeat/config"
    "github.com/mooyang-code/moox/server/internal/service/alert"
    "gorm.io/gorm"
)

func initHeartbeatService(db *gorm.DB, alertSvc alert.Service) heartbeat.Service {
    cfg := &config.Config{
        Monitor: config.MonitorConfig{
            Enabled:      true,
            ScanInterval: 10 * time.Second,
        },
        Prober: config.ProberConfig{
            Enabled:      true,
            ScanInterval: 30 * time.Second,
            MaxConcurrent: 10,
        },
        Alert: config.AlertConfig{
            Enabled:             true,
            TimeoutAlertLevel:   "warning",
            OfflineAlertLevel:   "error",
            AbnormalAlertLevel:  "warning",
            ProbeFailedThreshold: 3,
        },
    }

    // 创建心跳服务(注入告警服务)
    svc := heartbeat.NewService(db, alertSvc, cfg)

    // 启动服务
    svc.Start(context.Background())

    return svc
}
```

### 10.2 上报心跳

```go
// 云函数服务上报心跳
err := heartbeatSvc.ReportHeartbeat(ctx, &types.ReportHeartbeatRequest{
    NodeID:        "scf-abc123",
    NodeType:      "scf",
    SourceService: "cloudnode",
    Metrics: map[string]interface{}{
        "cpu_usage":    45.2,
        "memory_usage": 512,
        "invocations":  1000,
    },
    Metadata: map[string]interface{}{
        "version": "1.0.0",
        "region":  "ap-guangzhou",
    },
})
```

### 10.3 注册节点

```go
record, err := heartbeatSvc.RegisterNode(ctx, &types.RegisterNodeRequest{
    NodeID:            "server-001",
    NodeType:          "server",
    SourceService:     "infrastructure",
    HeartbeatInterval: 30,
    TimeoutThreshold:  90,
    ProbeEnabled:      true,
    ProbeURL:          "http://192.168.1.100:8080/health",
    ProbeStrategy:     "default",
    Metadata: map[string]interface{}{
        "hostname": "app-server-01",
        "ip":       "192.168.1.100",
    },
})
```

### 10.4 查询节点状态

```go
// 获取单个节点
node, err := heartbeatSvc.GetNode(ctx, "scf-abc123", "scf")

// 列出所有在线节点
online := types.NodeStatusOnline
nodes, total, err := heartbeatSvc.ListNodes(ctx, &types.NodeFilter{
    Status:   &online,
    Page:     1,
    PageSize: 20,
})

// 获取节点统计
stats, err := heartbeatSvc.GetNodeStatistics(ctx, "scf-abc123", "scf", &types.TimeRange{
    StartTime: time.Now().Add(-24 * time.Hour),
    EndTime:   time.Now(),
})
```

### 10.5 手动探测

```go
result, err := heartbeatSvc.ProbeNode(ctx, "server-001", "server")
if err != nil {
    log.Errorf("probe failed: %v", err)
} else {
    log.Infof("probe result: success=%v, status=%d, time=%dms",
        result.Success, result.StatusCode, result.ResponseTime)
}
```

---

## 11. 告警集成说明

心跳模块通过依赖注入的方式集成告警服务，在以下场景触发告警:

| 场景 | 告警类型 | 告警级别 | 触发条件 |
|------|---------|---------|---------|
| 心跳超时 | `heartbeat_timeout` | warning | 超过阈值未收到心跳 |
| 节点离线 | `node_offline` | error | 心跳超时且探测失败 |
| 节点异常 | `node_abnormal` | warning | 心跳超时但探测成功 |
| 探测失败 | `probe_failed` | warning/error | 连续探测失败N次 |

**告警恢复**: 当节点重新上报心跳并恢复在线状态时，心跳服务会查询该节点的活跃告警，并调用告警服务的接口自动解除告警。

详见: `HEARTBEAT_ALERT_SEPARATION.md` 第2.2节和第2.3节

---

## 12. 核心流程图

### 12.1 心跳上报流程

```
业务服务
    │
    │ 1. POST /heartbeat/report
    ↓
心跳接收器
    │
    ├─→ 2. 查询/创建记录
    │
    ├─→ 3. 更新心跳数据
    │
    ├─→ 4. 计算可用率
    │
    ├─→ 5. 保存数据库
    │
    ├─→ 6. 更新缓存
    │
    └─→ 7. 记录状态变更
```

### 12.2 超时监控流程

```
定时器(10s)
    │
    ↓
状态监控器
    │
    ├─→ 1. 扫描所有节点
    │
    ├─→ 2. 检查心跳超时
    │
    ├─→ 3. 更新超时状态
    │
    ├─→ 4. 保存数据库
    │
    ├─→ 5. 更新缓存
    │
    └─→ 6. 触发告警(调用告警服务)
```

### 12.3 主动探测流程

```
定时器(30s)
    │
    ↓
主动探测器
    │
    ├─→ 1. 获取超时节点
    │
    ├─→ 2. 选择适配器
    │
    ├─→ 3. 选择策略
    │
    ├─→ 4. 执行探测
    │
    ├─→ 5. 记录探测日志
    │
    ├─→ 6. 更新节点状态
    │       │
    │       ├─→ 探测成功 → 标记异常 → 触发异常告警
    │       │
    │       └─→ 探测失败 → 标记离线 → 触发离线告警
    │
    └─→ 7. 保存数据库 & 更新缓存
```

---

## 13. 性能优化

### 13.1 缓存策略
- 使用内存缓存存储节点状态
- 定期从数据库同步到缓存
- 写操作同时更新缓存和数据库

### 13.2 批量处理
- 支持批量上报心跳
- 监控器批量扫描节点
- 探测器并发探测多个节点

### 13.3 索引优化
- 节点ID和类型组合索引
- 状态字段索引
- 时间字段索引

### 13.4 异步处理
- 探测操作异步执行
- 告警触发异步调用
- 历史记录异步写入

---

## 14. 扩展性

### 14.1 新增节点类型
通过实现 `Adapter` 接口支持新的节点类型:

```go
type CustomAdapter struct{}

func (a *CustomAdapter) Name() string {
    return "custom"
}

func (a *CustomAdapter) Probe(ctx context.Context, req *ProbeRequest) (*ProbeResponse, error) {
    // 自定义探测逻辑
    return &ProbeResponse{Success: true}, nil
}

// 注册适配器
adapterRegistry.Register("custom", &CustomAdapter{})
```

### 14.2 新增探测策略
通过实现 `Strategy` 接口支持新的探测策略:

```go
type CustomStrategy struct{}

func (s *CustomStrategy) GetTimeout() int {
    return 10
}

func (s *CustomStrategy) GetRetryCount() int {
    return 3
}

// 注册策略
strategyRegistry.Register("custom", &CustomStrategy{})
```

---

## 15. 监控指标

建议暴露以下 Prometheus 指标:

```
# 心跳接收
heartbeat_reports_total{node_type}           # 心跳上报总数
heartbeat_reports_failed_total{node_type}    # 心跳上报失败数

# 节点状态
heartbeat_nodes_total{node_type,status}      # 节点总数(按状态)
heartbeat_timeout_total{node_type}           # 超时总数

# 探测指标
heartbeat_probes_total{node_type,result}     # 探测总数
heartbeat_probe_duration_seconds{node_type}  # 探测耗时

# 服务指标
heartbeat_monitor_scan_duration_seconds      # 监控扫描耗时
heartbeat_prober_scan_duration_seconds       # 探测扫描耗时
heartbeat_cache_size                         # 缓存大小
```

---

## 16. 总结

本设计方案提供了一个**完整、通用、可扩展**的心跳管理模块:

### 核心优势
1. **职责清晰**: 专注于心跳监控和节点健康管理
2. **告警分离**: 通过依赖注入集成独立的告警服务
3. **通用性强**: 支持多种节点类型和探测方式
4. **性能优异**: 基于缓存的高效状态检查
5. **易于扩展**: 适配器和策略模式支持灵活扩展

### 参考文档
- 告警模块设计: `alert.md`
- 分离说明文档: `HEARTBEAT_ALERT_SEPARATION.md`
- 原始设计文档: `heartbeat.md.backup`
