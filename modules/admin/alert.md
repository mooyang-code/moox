# MooX 通用告警模块设计方案

## 一、模块概述

### 1.1 设计理念

告警模块是一个**通用的、可扩展的告警系统**，不仅服务于心跳监控，还可以为整个系统的各个模块提供统一的告警能力。

**核心特性：**
- **通用性**: 支持任意业务模块接入告警
- **可扩展**: 支持多种告警渠道(日志、Webhook、邮件、短信、企业微信等)
- **规则引擎**: 支持灵活的告警规则配置(阈值、频率、聚合等)
- **告警分组**: 支持告警分组和聚合，避免告警风暴
- **告警升级**: 支持告警级别升级和自动升级
- **告警生命周期**: 完整的告警生命周期管理(触发、通知、处理、关闭)

### 1.2 应用场景

- **心跳监控**: 节点心跳超时、节点离线、节点异常
- **资源监控**: CPU/内存/磁盘使用率超限
- **任务监控**: 任务失败、任务超时、任务堆积
- **业务监控**: 错误率过高、响应时间过长、吞吐量异常
- **系统监控**: 服务宕机、数据库连接失败、第三方服务异常

---

## 二、目录结构

```
internal/service/alert/
├── README.md                           # 模块说明文档
├── interface.go                        # 服务接口定义
│
├── types/                              # 类型定义包
│   ├── constants.go                    # 常量定义
│   ├── types.go                        # 核心类型
│   ├── request.go                      # 请求类型
│   ├── response.go                     # 响应类型
│   └── errors.go                       # 错误定义
│
├── model/                              # 数据模型
│   ├── alert_record.go                 # 告警记录模型
│   ├── alert_rule.go                   # 告警规则模型
│   ├── alert_group.go                  # 告警分组模型
│   └── alert_channel.go                # 告警渠道模型
│
├── dao/                                # 数据访问层
│   ├── alert_record_dao.go            # 告警记录DAO
│   ├── alert_rule_dao.go              # 告警规则DAO
│   ├── alert_group_dao.go             # 告警分组DAO
│   └── alert_channel_dao.go           # 告警渠道DAO
│
├── impl/                               # 服务实现
│   ├── service_impl.go                # 主服务实现
│   ├── rule_engine.go                 # 规则引擎
│   ├── aggregator.go                  # 告警聚合器
│   ├── notifier.go                    # 通知器
│   ├── scheduler.go                   # 调度器(定时检查、自动升级等)
│   └── cache.go                       # 缓存管理
│
├── channel/                            # 告警渠道实现
│   ├── interface.go                   # 渠道接口
│   ├── registry.go                    # 渠道注册表
│   ├── log_channel.go                 # 日志渠道
│   ├── webhook_channel.go             # Webhook渠道
│   ├── email_channel.go               # 邮件渠道
│   ├── sms_channel.go                 # 短信渠道
│   ├── wechat_channel.go              # 企业微信渠道
│   └── dingtalk_channel.go            # 钉钉渠道
│
├── rule/                               # 规则引擎
│   ├── interface.go                   # 规则接口
│   ├── threshold_rule.go              # 阈值规则
│   ├── frequency_rule.go              # 频率规则
│   ├── duration_rule.go               # 持续时间规则
│   ├── change_rule.go                 # 变化率规则
│   └── composite_rule.go              # 复合规则
│
├── api/                                # HTTP API
│   ├── handler.go                     # 处理器
│   ├── router.go                      # 路由注册
│   ├── types.go                       # API类型
│   └── middleware.go                  # 中间件
│
└── config/                             # 配置
    └── config.go                      # 配置结构
```

---

## 三、数据库表设计

### 3.1 告警记录表 (t_alert_records)

```sql
-- ============ 通用告警模块表设计 ============

-- ************ 告警记录表 ************
CREATE TABLE IF NOT EXISTS t_alert_records (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,  -- 主键ID
    c_alert_id TEXT NOT NULL,                         -- 告警ID(全局唯一)

    -- 告警来源
    c_source_service TEXT NOT NULL,                   -- 来源服务(heartbeat/collector/asynctask/custom等)
    c_source_type TEXT NOT NULL,                      -- 来源类型(node/task/resource/business等)
    c_source_id TEXT NOT NULL,                        -- 来源对象ID(节点ID/任务ID/资源ID等)
    c_source_name TEXT DEFAULT '',                    -- 来源对象名称(便于显示)

    -- 告警分类
    c_alert_type TEXT NOT NULL,                       -- 告警类型(timeout/offline/failed/threshold_exceeded等)
    c_alert_category TEXT NOT NULL DEFAULT 'system',  -- 告警分类(system/business/security等)
    c_alert_level TEXT NOT NULL,                      -- 告警级别(info/warning/error/critical)
    c_severity INTEGER NOT NULL DEFAULT 0,            -- 严重程度(0-100,便于排序)

    -- 告警内容
    c_alert_title TEXT NOT NULL,                      -- 告警标题(简短描述)
    c_alert_message TEXT NOT NULL,                    -- 告警消息(详细描述)
    c_alert_details TEXT DEFAULT '{}',                -- 告警详情(JSON格式,包含更多上下文)
    c_alert_tags TEXT DEFAULT '[]',                   -- 告警标签(JSON数组,用于分组和过滤)

    -- 告警规则
    c_rule_id TEXT DEFAULT '',                        -- 关联的规则ID(如果由规则触发)
    c_rule_name TEXT DEFAULT '',                      -- 规则名称

    -- 告警时间
    c_triggered_at DATETIME NOT NULL,                 -- 触发时间
    c_first_triggered_at DATETIME,                    -- 首次触发时间(用于聚合)
    c_trigger_count INTEGER DEFAULT 1,                -- 触发次数(用于聚合)

    -- 告警状态
    c_status INTEGER NOT NULL DEFAULT 0,              -- 状态(0=触发,1=通知中,2=已通知,3=处理中,4=已处理,5=已解决,6=已关闭,7=已忽略)
    c_is_notified INTEGER DEFAULT 0,                  -- 是否已通知(0=未通知,1=已通知)
    c_notified_at DATETIME,                           -- 通知时间
    c_notification_result TEXT DEFAULT '',            -- 通知结果

    -- 告警处理
    c_assigned_to TEXT DEFAULT '',                    -- 分配给(处理人)
    c_assigned_at DATETIME,                           -- 分配时间
    c_handled_by TEXT DEFAULT '',                     -- 处理人
    c_handled_at DATETIME,                            -- 处理时间
    c_handled_note TEXT DEFAULT '',                   -- 处理备注

    -- 告警分组
    c_group_id TEXT DEFAULT '',                       -- 分组ID(用于聚合相同告警)
    c_is_aggregated INTEGER DEFAULT 0,                -- 是否已聚合(0=否,1=是)
    c_parent_alert_id TEXT DEFAULT '',                -- 父告警ID(如果是聚合的子告警)

    -- 告警升级
    c_escalation_level INTEGER DEFAULT 0,             -- 升级级别(0=无升级,1=一级升级,2=二级升级等)
    c_escalated_at DATETIME,                          -- 升级时间

    -- 元数据
    c_metadata TEXT DEFAULT '{}',                     -- 扩展元数据(JSON格式)
    c_fingerprint TEXT DEFAULT '',                    -- 告警指纹(用于去重和聚合,基于告警特征生成的哈希值)

    -- 审计字段
    c_is_deleted TEXT NOT NULL DEFAULT 'false',             -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,       -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,       -- 修改时间

    UNIQUE (c_alert_id)
);

-- ************ 告警规则表 ************
CREATE TABLE IF NOT EXISTS t_alert_rules (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,  -- 主键ID
    c_rule_id TEXT NOT NULL,                          -- 规则ID(全局唯一)
    c_rule_name TEXT NOT NULL,                        -- 规则名称
    c_rule_description TEXT DEFAULT '',               -- 规则描述

    -- 规则应用范围
    c_source_service TEXT NOT NULL,                   -- 应用服务(heartbeat/collector/asynctask等)
    c_source_type TEXT NOT NULL,                      -- 应用对象类型(node/task/resource等)
    c_source_filter TEXT DEFAULT '{}',                -- 应用对象过滤条件(JSON格式)

    -- 规则类型和条件
    c_rule_type TEXT NOT NULL,                        -- 规则类型(threshold/frequency/duration/change/composite)
    c_condition TEXT NOT NULL,                        -- 规则条件(JSON格式,具体格式依赖规则类型)
    c_evaluation_interval INTEGER DEFAULT 60,         -- 评估间隔(秒)
    c_evaluation_window INTEGER DEFAULT 300,          -- 评估窗口(秒,用于统计分析)

    -- 告警配置
    c_alert_level TEXT NOT NULL,                      -- 告警级别(info/warning/error/critical)
    c_alert_title TEXT NOT NULL,                      -- 告警标题模板
    c_alert_message TEXT NOT NULL,                    -- 告警消息模板(支持变量替换)
    c_alert_tags TEXT DEFAULT '[]',                   -- 告警标签(JSON数组)

    -- 通知配置
    c_notification_channels TEXT DEFAULT '[]',        -- 通知渠道列表(JSON数组,如["log","webhook","email"])
    c_notification_targets TEXT DEFAULT '{}',         -- 通知目标(JSON格式,每个渠道的具体配置)
    c_notification_throttle INTEGER DEFAULT 0,        -- 通知节流(秒,同一告警在此时间内不重复通知)

    -- 聚合配置
    c_aggregation_enabled INTEGER DEFAULT 0,          -- 是否启用聚合(0=否,1=是)
    c_aggregation_window INTEGER DEFAULT 300,         -- 聚合窗口(秒)
    c_aggregation_keys TEXT DEFAULT '[]',             -- 聚合键(JSON数组,用于分组)

    -- 升级配置
    c_escalation_enabled INTEGER DEFAULT 0,           -- 是否启用升级(0=否,1=是)
    c_escalation_rules TEXT DEFAULT '[]',             -- 升级规则(JSON数组)

    -- 规则状态
    c_enabled INTEGER NOT NULL DEFAULT 1,             -- 是否启用(0=禁用,1=启用)
    c_priority INTEGER DEFAULT 0,                     -- 优先级(数值越大优先级越高)

    -- 审计字段
    c_created_by TEXT DEFAULT '',                     -- 创建人
    c_updated_by TEXT DEFAULT '',                     -- 更新人
    c_is_deleted TEXT NOT NULL DEFAULT 'false',             -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,       -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,       -- 修改时间

    UNIQUE (c_rule_id)
);

-- ************ 告警分组表 ************
CREATE TABLE IF NOT EXISTS t_alert_groups (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,  -- 主键ID
    c_group_id TEXT NOT NULL,                         -- 分组ID(全局唯一)
    c_group_name TEXT NOT NULL,                       -- 分组名称
    c_group_description TEXT DEFAULT '',              -- 分组描述

    -- 分组条件
    c_source_service TEXT NOT NULL,                   -- 服务过滤
    c_source_type TEXT NOT NULL,                      -- 类型过滤
    c_grouping_keys TEXT NOT NULL,                    -- 分组键(JSON数组)
    c_grouping_filter TEXT DEFAULT '{}',              -- 分组过滤条件(JSON格式)

    -- 聚合配置
    c_aggregation_window INTEGER DEFAULT 300,         -- 聚合窗口(秒)
    c_max_alerts INTEGER DEFAULT 100,                 -- 最大告警数(超过后停止聚合)

    -- 通知配置
    c_notification_enabled INTEGER DEFAULT 1,         -- 是否启用通知(0=否,1=是)
    c_notification_channels TEXT DEFAULT '[]',        -- 通知渠道列表
    c_notification_summary_only INTEGER DEFAULT 0,    -- 是否仅发送摘要(0=否,1=是)

    -- 分组统计
    c_alert_count INTEGER DEFAULT 0,                  -- 当前告警数
    c_total_alert_count INTEGER DEFAULT 0,            -- 总告警数
    c_first_alert_at DATETIME,                        -- 首个告警时间
    c_last_alert_at DATETIME,                         -- 最后告警时间

    -- 审计字段
    c_created_by TEXT DEFAULT '',                     -- 创建人
    c_is_deleted TEXT NOT NULL DEFAULT 'false',             -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,       -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,       -- 修改时间

    UNIQUE (c_group_id)
);

-- ************ 告警渠道配置表 ************
CREATE TABLE IF NOT EXISTS t_alert_channels (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,  -- 主键ID
    c_channel_id TEXT NOT NULL,                       -- 渠道ID(全局唯一)
    c_channel_name TEXT NOT NULL,                     -- 渠道名称
    c_channel_type TEXT NOT NULL,                     -- 渠道类型(log/webhook/email/sms/wechat/dingtalk)
    c_channel_description TEXT DEFAULT '',            -- 渠道描述

    -- 渠道配置
    c_config TEXT NOT NULL,                           -- 渠道配置(JSON格式,不同类型配置不同)
    -- 例如:
    -- webhook: {"url":"http://...", "method":"POST", "headers":{}, "timeout":10}
    -- email: {"smtp_host":"smtp.qq.com", "smtp_port":587, "from":"alert@example.com", "to":["admin@example.com"]}
    -- wechat: {"corp_id":"xxx", "agent_id":"xxx", "secret":"xxx"}

    -- 渠道状态
    c_enabled INTEGER NOT NULL DEFAULT 1,             -- 是否启用(0=禁用,1=启用)
    c_test_result TEXT DEFAULT '',                    -- 测试结果
    c_last_test_at DATETIME,                          -- 最后测试时间

    -- 使用统计
    c_total_sent INTEGER DEFAULT 0,                   -- 总发送次数
    c_success_sent INTEGER DEFAULT 0,                 -- 成功发送次数
    c_failed_sent INTEGER DEFAULT 0,                  -- 失败发送次数
    c_last_sent_at DATETIME,                          -- 最后发送时间

    -- 审计字段
    c_created_by TEXT DEFAULT '',                     -- 创建人
    c_updated_by TEXT DEFAULT '',                     -- 更新人
    c_is_deleted TEXT NOT NULL DEFAULT 'false',             -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,       -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,       -- 修改时间

    UNIQUE (c_channel_id)
);





-- ************ 告警通知日志表 ************
CREATE TABLE IF NOT EXISTS t_alert_notification_logs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,  -- 主键ID
    c_alert_id TEXT NOT NULL,                         -- 告警ID
    c_channel_type TEXT NOT NULL,                     -- 渠道类型
    c_channel_id TEXT NOT NULL,                       -- 渠道ID
    c_sent_at DATETIME NOT NULL,                      -- 发送时间
    c_status INTEGER NOT NULL,                        -- 发送状态(0=失败,1=成功,2=部分成功)
    c_request_data TEXT DEFAULT '{}',                 -- 请求数据(JSON格式)
    c_response_data TEXT DEFAULT '{}',                -- 响应数据(JSON格式)
    c_error_message TEXT DEFAULT '',                  -- 错误信息
    c_retry_count INTEGER DEFAULT 0,                  -- 重试次数
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP        -- 记录时间
);

-- ************ 创建索引 ************

-- 告警记录表索引
CREATE INDEX IF NOT EXISTS idx_alert_records_alert_id ON t_alert_records(c_alert_id);
CREATE INDEX IF NOT EXISTS idx_alert_records_source ON t_alert_records(c_source_service, c_source_type, c_source_id);
CREATE INDEX IF NOT EXISTS idx_alert_records_status ON t_alert_records(c_status);
CREATE INDEX IF NOT EXISTS idx_alert_records_level ON t_alert_records(c_alert_level);
CREATE INDEX IF NOT EXISTS idx_alert_records_triggered_at ON t_alert_records(c_triggered_at DESC);
CREATE INDEX IF NOT EXISTS idx_alert_records_group ON t_alert_records(c_group_id);
CREATE INDEX IF NOT EXISTS idx_alert_records_fingerprint ON t_alert_records(c_fingerprint);
CREATE INDEX IF NOT EXISTS idx_alert_records_rule ON t_alert_records(c_rule_id);

-- 告警规则表索引
CREATE INDEX IF NOT EXISTS idx_alert_rules_rule_id ON t_alert_rules(c_rule_id);
CREATE INDEX IF NOT EXISTS idx_alert_rules_source ON t_alert_rules(c_source_service, c_source_type);
CREATE INDEX IF NOT EXISTS idx_alert_rules_enabled ON t_alert_rules(c_enabled);
CREATE INDEX IF NOT EXISTS idx_alert_rules_priority ON t_alert_rules(c_priority DESC);

-- 告警分组表索引
CREATE INDEX IF NOT EXISTS idx_alert_groups_group_id ON t_alert_groups(c_group_id);
CREATE INDEX IF NOT EXISTS idx_alert_groups_source ON t_alert_groups(c_source_service, c_source_type);

-- 告警渠道表索引
CREATE INDEX IF NOT EXISTS idx_alert_channels_channel_id ON t_alert_channels(c_channel_id);
CREATE INDEX IF NOT EXISTS idx_alert_channels_type ON t_alert_channels(c_channel_type);
CREATE INDEX IF NOT EXISTS idx_alert_channels_enabled ON t_alert_channels(c_enabled);



-- 告警通知日志表索引
CREATE INDEX IF NOT EXISTS idx_alert_notification_logs_alert_id ON t_alert_notification_logs(c_alert_id);
CREATE INDEX IF NOT EXISTS idx_alert_notification_logs_channel ON t_alert_notification_logs(c_channel_id);
CREATE INDEX IF NOT EXISTS idx_alert_notification_logs_time ON t_alert_notification_logs(c_sent_at DESC);

-- ************ 创建触发器 ************

-- 告警记录表更新触发器
DROP TRIGGER IF EXISTS update_alert_records_mtime;
CREATE TRIGGER update_alert_records_mtime
AFTER UPDATE ON t_alert_records
BEGIN
    UPDATE t_alert_records
    SET c_mtime = CURRENT_TIMESTAMP
    WHERE rowid = NEW.rowid;
END;

-- 告警规则表更新触发器
DROP TRIGGER IF EXISTS update_alert_rules_mtime;
CREATE TRIGGER update_alert_rules_mtime
AFTER UPDATE ON t_alert_rules
BEGIN
    UPDATE t_alert_rules
    SET c_mtime = CURRENT_TIMESTAMP
    WHERE rowid = NEW.rowid;
END;

-- 告警分组表更新触发器
DROP TRIGGER IF EXISTS update_alert_groups_mtime;
CREATE TRIGGER update_alert_groups_mtime
AFTER UPDATE ON t_alert_groups
BEGIN
    UPDATE t_alert_groups
    SET c_mtime = CURRENT_TIMESTAMP
    WHERE rowid = NEW.rowid;
END;

-- 告警渠道表更新触发器
DROP TRIGGER IF EXISTS update_alert_channels_mtime;
CREATE TRIGGER update_alert_channels_mtime
AFTER UPDATE ON t_alert_channels
BEGIN
    UPDATE t_alert_channels
    SET c_mtime = CURRENT_TIMESTAMP
    WHERE rowid = NEW.rowid;
END;


```

---

## 四、核心接口设计

### 4.1 主服务接口 (interface.go)

```go
package alert

import (
    "context"

    "github.com/mooyang-code/moox/modules/admin/internal/service/alert/types"
)

// Service 告警服务接口
type Service interface {
    // ========== 告警触发 ==========
    // TriggerAlert 触发告警
    TriggerAlert(ctx context.Context, req *types.TriggerAlertRequest) (*types.Alert, error)

    // BatchTriggerAlerts 批量触发告警
    BatchTriggerAlerts(ctx context.Context, reqs []*types.TriggerAlertRequest) ([]*types.Alert, error)

    // CloseAlert 关闭告警
    CloseAlert(ctx context.Context, alertID string, closedBy string) error

    // ========== 告警查询 ==========
    // GetAlert 获取告警详情
    GetAlert(ctx context.Context, alertID string) (*types.Alert, error)

    // ListAlerts 列出告警
    ListAlerts(ctx context.Context, filter *types.AlertFilter) ([]*types.Alert, int64, error)

    // GetAlertStatistics 获取告警统计
    GetAlertStatistics(ctx context.Context, filter *types.AlertFilter) (*types.AlertStatistics, error)

    // ========== 告警处理 ==========
    // AssignAlert 分配告警
    AssignAlert(ctx context.Context, alertID string, assignedTo string, assignedBy string) error

    // HandleAlert 处理告警
    HandleAlert(ctx context.Context, alertID string, handledBy string, handledNote string) error

    // ========== 告警规则管理 ==========
    // CreateRule 创建告警规则
    CreateRule(ctx context.Context, rule *types.AlertRule) error

    // UpdateRule 更新告警规则
    UpdateRule(ctx context.Context, rule *types.AlertRule) error

    // DeleteRule 删除告警规则
    DeleteRule(ctx context.Context, ruleID string) error

    // GetRule 获取告警规则
    GetRule(ctx context.Context, ruleID string) (*types.AlertRule, error)

    // ListRules 列出告警规则
    ListRules(ctx context.Context, filter *types.RuleFilter) ([]*types.AlertRule, int64, error)

    // EvaluateRule 评估规则(手动触发评估)
    EvaluateRule(ctx context.Context, ruleID string) error

    // ========== 告警渠道管理 ==========
    // CreateChannel 创建告警渠道
    CreateChannel(ctx context.Context, channel *types.AlertChannel) error

    // UpdateChannel 更新告警渠道
    UpdateChannel(ctx context.Context, channel *types.AlertChannel) error

    // DeleteChannel 删除告警渠道
    DeleteChannel(ctx context.Context, channelID string) error

    // GetChannel 获取告警渠道
    GetChannel(ctx context.Context, channelID string) (*types.AlertChannel, error)

    // ListChannels 列出告警渠道
    ListChannels(ctx context.Context, filter *types.ChannelFilter) ([]*types.AlertChannel, int64, error)

    // TestChannel 测试告警渠道
    TestChannel(ctx context.Context, channelID string) error

    // ========== 告警静默管理 ==========
    // ========== 生命周期 ==========
    // Start 启动服务
    Start(ctx context.Context) error

    // Stop 停止服务
    Stop(ctx context.Context) error
}
```

---

## 五、核心类型定义

### 5.1 常量定义 (types/constants.go)

```go
package types

// ========== 告警状态 ==========
type AlertStatus int

const (
    AlertStatusTriggered  AlertStatus = 0 // 触发
    AlertStatusNotifying  AlertStatus = 1 // 通知中
    AlertStatusNotified   AlertStatus = 2 // 已通知
    AlertStatusHandling   AlertStatus = 3 // 处理中
    AlertStatusHandled    AlertStatus = 4 // 已处理
    AlertStatusResolved   AlertStatus = 5 // 已解决
    AlertStatusClosed     AlertStatus = 6 // 已关闭
    AlertStatusIgnored    AlertStatus = 7 // 已忽略
)

var AlertStatusText = map[AlertStatus]string{
    AlertStatusTriggered:  "触发",
    AlertStatusNotifying:  "通知中",
    AlertStatusNotified:   "已通知",
    AlertStatusHandling:   "处理中",
    AlertStatusHandled:    "已处理",
    AlertStatusResolved:   "已解决",
    AlertStatusClosed:     "已关闭",
    AlertStatusIgnored:    "已忽略",
}

// ========== 告警级别 ==========
const (
    AlertLevelInfo     = "info"     // 信息
    AlertLevelWarning  = "warning"  // 警告
    AlertLevelError    = "error"    // 错误
    AlertLevelCritical = "critical" // 严重
)

// 告警级别对应的严重程度(用于排序)
var AlertLevelSeverity = map[string]int{
    AlertLevelInfo:     10,
    AlertLevelWarning:  30,
    AlertLevelError:    60,
    AlertLevelCritical: 90,
}

// ========== 告警分类 ==========
const (
    AlertCategorySystem   = "system"   // 系统
    AlertCategoryBusiness = "business" // 业务
    AlertCategorySecurity = "security" // 安全
    AlertCategoryCustom   = "custom"   // 自定义
)

// ========== 来源服务 ==========
const (
    SourceServiceHeartbeat = "heartbeat"  // 心跳服务
    SourceServiceCollector = "collector"  // 采集服务
    SourceServiceAsyncTask = "asynctask"  // 异步任务服务
    SourceServiceCloudNode = "cloudnode"  // 云节点服务
    SourceServiceCustom    = "custom"     // 自定义服务
)

// ========== 来源类型 ==========
const (
    SourceTypeNode     = "node"     // 节点
    SourceTypeTask     = "task"     // 任务
    SourceTypeResource = "resource" // 资源
    SourceTypeBusiness = "business" // 业务
    SourceTypeCustom   = "custom"   // 自定义
)

// ========== 告警类型(可扩展) ==========
const (
    // 心跳相关
    AlertTypeHeartbeatTimeout = "heartbeat_timeout" // 心跳超时
    AlertTypeNodeOffline      = "node_offline"      // 节点离线
    AlertTypeNodeAbnormal     = "node_abnormal"     // 节点异常
    AlertTypeNodeRecovered    = "node_recovered"    // 节点恢复

    // 任务相关
    AlertTypeTaskFailed  = "task_failed"  // 任务失败
    AlertTypeTaskTimeout = "task_timeout" // 任务超时
    AlertTypeTaskStalled = "task_stalled" // 任务堆积

    // 资源相关
    AlertTypeResourceExhausted   = "resource_exhausted"   // 资源耗尽
    AlertTypeThresholdExceeded   = "threshold_exceeded"   // 阈值超限
    AlertTypeAbnormalBehavior    = "abnormal_behavior"    // 异常行为

    // 业务相关
    AlertTypeHighErrorRate       = "high_error_rate"      // 高错误率
    AlertTypeSlowResponse        = "slow_response"        // 响应缓慢
    AlertTypeAbnormalThroughput  = "abnormal_throughput"  // 吞吐量异常
)

// ========== 规则类型 ==========
const (
    RuleTypeThreshold  = "threshold"  // 阈值规则
    RuleTypeFrequency  = "frequency"  // 频率规则
    RuleTypeDuration   = "duration"   // 持续时间规则
    RuleTypeChange     = "change"     // 变化率规则
    RuleTypeComposite  = "composite"  // 复合规则
)

// ========== 渠道类型 ==========
const (
    ChannelTypeLog      = "log"      // 日志
    ChannelTypeWebhook  = "webhook"  // Webhook
    ChannelTypeEmail    = "email"    // 邮件
    ChannelTypeSMS      = "sms"      // 短信
    ChannelTypeWeChat   = "wechat"   // 企业微信
    ChannelTypeDingTalk = "dingtalk" // 钉钉
)

// ========== 历史操作 ==========
const (
    ActionCreated   = "created"   // 创建
    ActionNotified  = "notified"  // 通知
    ActionAssigned  = "assigned"  // 分配
    ActionHandled   = "handled"   // 处理
    ActionResolved  = "resolved"  // 解决
    ActionClosed    = "closed"    // 关闭
    ActionEscalated = "escalated" // 升级
    ActionIgnored   = "ignored"   // 忽略
)
```

### 5.2 核心类型 (types/types.go)

```go
package types

import "time"

// ========== 告警 ==========
// Alert 告警
type Alert struct {
    AlertID         string                 `json:"alert_id"`          // 告警ID
    SourceService   string                 `json:"source_service"`    // 来源服务
    SourceType      string                 `json:"source_type"`       // 来源类型
    SourceID        string                 `json:"source_id"`         // 来源对象ID
    SourceName      string                 `json:"source_name"`       // 来源对象名称
    AlertType       string                 `json:"alert_type"`        // 告警类型
    AlertCategory   string                 `json:"alert_category"`    // 告警分类
    AlertLevel      string                 `json:"alert_level"`       // 告警级别
    Severity        int                    `json:"severity"`          // 严重程度
    AlertTitle      string                 `json:"alert_title"`       // 告警标题
    AlertMessage    string                 `json:"alert_message"`     // 告警消息
    AlertDetails    map[string]interface{} `json:"alert_details"`     // 告警详情
    AlertTags       []string               `json:"alert_tags"`        // 告警标签
    RuleID          string                 `json:"rule_id"`           // 规则ID
    RuleName        string                 `json:"rule_name"`         // 规则名称
    TriggeredAt     time.Time              `json:"triggered_at"`      // 触发时间
    FirstTriggeredAt *time.Time            `json:"first_triggered_at"`// 首次触发时间
    TriggerCount    int                    `json:"trigger_count"`     // 触发次数
    Status          AlertStatus            `json:"status"`            // 状态
    IsNotified      bool                   `json:"is_notified"`       // 是否已通知
    NotifiedAt      *time.Time             `json:"notified_at"`       // 通知时间
    AssignedTo      string                 `json:"assigned_to"`       // 分配给
    AssignedAt      *time.Time             `json:"assigned_at"`       // 分配时间
    HandledBy       string                 `json:"handled_by"`        // 处理人
    HandledAt       *time.Time             `json:"handled_at"`        // 处理时间
    HandledNote     string                 `json:"handled_note"`      // 处理备注
    GroupID         string                 `json:"group_id"`          // 分组ID
    IsAggregated    bool                   `json:"is_aggregated"`     // 是否已聚合
    ParentAlertID   string                 `json:"parent_alert_id"`   // 父告警ID
    EscalationLevel int                    `json:"escalation_level"`  // 升级级别
    EscalatedAt     *time.Time             `json:"escalated_at"`      // 升级时间
    Metadata        map[string]interface{} `json:"metadata"`          // 元数据
    Fingerprint     string                 `json:"fingerprint"`       // 告警指纹
    CreatedAt       time.Time              `json:"created_at"`        // 创建时间
    UpdatedAt       time.Time              `json:"updated_at"`        // 更新时间
}

// ========== 告警规则 ==========
// AlertRule 告警规则
type AlertRule struct {
    RuleID              string                 `json:"rule_id"`               // 规则ID
    RuleName            string                 `json:"rule_name"`             // 规则名称
    RuleDescription     string                 `json:"rule_description"`      // 规则描述
    SourceService       string                 `json:"source_service"`        // 应用服务
    SourceType          string                 `json:"source_type"`           // 应用对象类型
    SourceFilter        map[string]interface{} `json:"source_filter"`         // 应用对象过滤条件
    RuleType            string                 `json:"rule_type"`             // 规则类型
    Condition           map[string]interface{} `json:"condition"`             // 规则条件
    EvaluationInterval  int                    `json:"evaluation_interval"`   // 评估间隔(秒)
    EvaluationWindow    int                    `json:"evaluation_window"`     // 评估窗口(秒)
    AlertLevel          string                 `json:"alert_level"`           // 告警级别
    AlertTitle          string                 `json:"alert_title"`           // 告警标题模板
    AlertMessage        string                 `json:"alert_message"`         // 告警消息模板
    AlertTags           []string               `json:"alert_tags"`            // 告警标签
    NotificationChannels []string              `json:"notification_channels"` // 通知渠道列表
    NotificationTargets map[string]interface{} `json:"notification_targets"`  // 通知目标
    NotificationThrottle int                   `json:"notification_throttle"` // 通知节流(秒)
    AggregationEnabled  bool                   `json:"aggregation_enabled"`   // 是否启用聚合
    AggregationWindow   int                    `json:"aggregation_window"`    // 聚合窗口(秒)
    AggregationKeys     []string               `json:"aggregation_keys"`      // 聚合键
    EscalationEnabled   bool                   `json:"escalation_enabled"`    // 是否启用升级
    EscalationRules     []EscalationRule       `json:"escalation_rules"`      // 升级规则
    Enabled             bool                   `json:"enabled"`               // 是否启用
    Priority            int                    `json:"priority"`              // 优先级
    CreatedBy           string                 `json:"created_by"`            // 创建人
    UpdatedBy           string                 `json:"updated_by"`            // 更新人
    CreatedAt           time.Time              `json:"created_at"`            // 创建时间
    UpdatedAt           time.Time              `json:"updated_at"`            // 更新时间
}

// EscalationRule 升级规则
type EscalationRule struct {
    Level       int      `json:"level"`        // 升级级别
    Delay       int      `json:"delay"`        // 延迟时间(秒)
    Channels    []string `json:"channels"`     // 通知渠道
    Targets     []string `json:"targets"`      // 通知目标
    AlertLevel  string   `json:"alert_level"`  // 告警级别(可升级)
}

// ========== 告警渠道 ==========
// AlertChannel 告警渠道
type AlertChannel struct {
    ChannelID          string                 `json:"channel_id"`          // 渠道ID
    ChannelName        string                 `json:"channel_name"`        // 渠道名称
    ChannelType        string                 `json:"channel_type"`        // 渠道类型
    ChannelDescription string                 `json:"channel_description"` // 渠道描述
    Config             map[string]interface{} `json:"config"`              // 渠道配置
    Enabled            bool                   `json:"enabled"`             // 是否启用
    TestResult         string                 `json:"test_result"`         // 测试结果
    LastTestAt         *time.Time             `json:"last_test_at"`        // 最后测试时间
    TotalSent          int                    `json:"total_sent"`          // 总发送次数
    SuccessSent        int                    `json:"success_sent"`        // 成功发送次数
    FailedSent         int                    `json:"failed_sent"`         // 失败发送次数
    LastSentAt         *time.Time             `json:"last_sent_at"`        // 最后发送时间
    CreatedBy          string                 `json:"created_by"`          // 创建人
    UpdatedBy          string                 `json:"updated_by"`          // 更新人
    CreatedAt          time.Time              `json:"created_at"`          // 创建时间
    UpdatedAt          time.Time              `json:"updated_at"`          // 更新时间
}

// ========== 告警统计 ==========
// AlertStatistics 告警统计
type AlertStatistics struct {
    TotalAlerts      int                       `json:"total_alerts"`       // 总告警数
    ActiveAlerts     int                       `json:"active_alerts"`      // 活跃告警数
    ResolvedAlerts   int                       `json:"resolved_alerts"`    // 已解决告警数
    ByLevel          map[string]int            `json:"by_level"`           // 按级别分组
    ByType           map[string]int            `json:"by_type"`            // 按类型分组
    BySource         map[string]int            `json:"by_source"`          // 按来源分组
    ByStatus         map[AlertStatus]int       `json:"by_status"`          // 按状态分组
    TrendData        []StatisticPoint          `json:"trend_data"`         // 趋势数据
}

// StatisticPoint 统计数据点
type StatisticPoint struct {
    Timestamp time.Time `json:"timestamp"` // 时间点
    Count     int       `json:"count"`     // 数量
}

```

---

## 六、子功能模块设计

### 6.1 规则引擎 (Rule Engine)

**功能**:
- 支持多种规则类型(阈值、频率、持续时间、变化率、复合规则)
- 定时评估规则
- 支持规则优先级
- 支持规则启用/禁用

**核心接口**:
```go
// RuleEvaluator 规则评估器接口
type RuleEvaluator interface {
    // Evaluate 评估规则
    Evaluate(ctx context.Context, rule *AlertRule, data interface{}) (bool, error)

    // GetRuleType 获取规则类型
    GetRuleType() string
}
```

**规则类型示例**:

1. **阈值规则** (Threshold Rule):
```json
{
    "rule_type": "threshold",
    "condition": {
        "metric": "cpu_usage",
        "operator": ">",
        "threshold": 80,
        "duration": 300
    }
}
```

2. **频率规则** (Frequency Rule):
```json
{
    "rule_type": "frequency",
    "condition": {
        "event": "task_failed",
        "count": 5,
        "window": 600
    }
}
```

3. **变化率规则** (Change Rule):
```json
{
    "rule_type": "change",
    "condition": {
        "metric": "qps",
        "change_percent": 50,
        "baseline": "1h_avg",
        "direction": "increase"
    }
}
```

### 6.2 通知渠道 (Notification Channels)

**功能**:
- 支持多种通知渠道
- 支持异步通知
- 支持重试机制
- 支持通知模板

**核心接口**:
```go
// NotificationChannel 通知渠道接口
type NotificationChannel interface {
    // GetChannelType 获取渠道类型
    GetChannelType() string

    // Send 发送通知
    Send(ctx context.Context, alert *Alert, config map[string]interface{}) error

    // Test 测试渠道
    Test(ctx context.Context, config map[string]interface{}) error

    // Validate 验证配置
    Validate(config map[string]interface{}) error
}
```

**渠道配置示例**:

1. **Webhook**:
```json
{
    "url": "https://example.com/webhook",
    "method": "POST",
    "headers": {
        "Authorization": "Bearer xxx"
    },
    "timeout": 10
}
```

2. **Email**:
```json
{
    "smtp_host": "smtp.qq.com",
    "smtp_port": 587,
    "username": "alert@example.com",
    "password": "xxx",
    "from": "alert@example.com",
    "to": ["admin@example.com"]
}
```

3. **企业微信**:
```json
{
    "corp_id": "ww123456",
    "agent_id": "1000001",
    "secret": "xxx",
    "to_user": "@all"
}
```

### 6.3 告警聚合器 (Alert Aggregator)

**功能**:
- 避免告警风暴
- 按条件聚合相似告警
- 支持自定义聚合键
- 支持聚合窗口

**聚合策略**:
1. 按告警指纹聚合(相同来源、类型的告警)
2. 按时间窗口聚合
3. 按自定义键聚合

### 6.4 告警调度器 (Scheduler)

**功能**:
- 定时评估规则
- 自动升级告警
- 自动清理过期告警

**调度任务**:
1. 规则评估任务(每分钟)
2. 告警升级任务(每5分钟)
3. 静默更新任务(每分钟)
4. 数据清理任务(每天)

---

## 七、HTTP API 设计

### 7.1 告警管理 API

```
# 告警触发和管理
POST   /api/v1/alert/trigger              # 触发告警
POST   /api/v1/alert/batch-trigger        # 批量触发告警
POST   /api/v1/alert/:id/resolve          # 解决告警
POST   /api/v1/alert/:id/close            # 关闭告警
POST   /api/v1/alert/:id/assign           # 分配告警
POST   /api/v1/alert/:id/handle           # 处理告警

# 告警查询
GET    /api/v1/alert/list                 # 列出告警
GET    /api/v1/alert/:id                  # 获取告警详情
GET    /api/v1/alert/:id/history          # 获取告警历史
GET    /api/v1/alert/statistics           # 获取告警统计
```

### 7.2 规则管理 API

```
POST   /api/v1/alert/rule                 # 创建规则
PUT    /api/v1/alert/rule/:id             # 更新规则
DELETE /api/v1/alert/rule/:id             # 删除规则
GET    /api/v1/alert/rule/:id             # 获取规则
GET    /api/v1/alert/rule/list            # 列出规则
POST   /api/v1/alert/rule/:id/evaluate    # 评估规则
POST   /api/v1/alert/rule/:id/enable      # 启用规则
POST   /api/v1/alert/rule/:id/disable     # 禁用规则
```

### 7.3 渠道管理 API

```
POST   /api/v1/alert/channel              # 创建渠道
PUT    /api/v1/alert/channel/:id          # 更新渠道
DELETE /api/v1/alert/channel/:id          # 删除渠道
GET    /api/v1/alert/channel/:id          # 获取渠道
GET    /api/v1/alert/channel/list         # 列出渠道
POST   /api/v1/alert/channel/:id/test     # 测试渠道
```

```

---

## 八、使用示例

### 8.1 触发告警

```go
// 心跳服务触发告警
alertSvc.TriggerAlert(ctx, &types.TriggerAlertRequest{
    SourceService: types.SourceServiceHeartbeat,
    SourceType:    types.SourceTypeNode,
    SourceID:      "node-123",
    SourceName:    "DataCollector-Master-123",
    AlertType:     types.AlertTypeHeartbeatTimeout,
    AlertLevel:    types.AlertLevelWarning,
    AlertTitle:    "节点心跳超时",
    AlertMessage:  "节点 DataCollector-Master-123 已超过30秒未上报心跳",
    AlertDetails: map[string]interface{}{
        "node_id": "node-123",
        "last_heartbeat": "2025-01-23 15:00:00",
        "timeout_threshold": 30,
    },
    AlertTags: []string{"heartbeat", "timeout"},
})
```

### 8.2 创建告警规则

```go
// 创建节点心跳超时规则
alertSvc.CreateRule(ctx, &types.AlertRule{
    RuleName:        "节点心跳超时告警",
    RuleDescription: "当节点超过30秒未上报心跳时触发告警",
    SourceService:   types.SourceServiceHeartbeat,
    SourceType:      types.SourceTypeNode,
    RuleType:        types.RuleTypeThreshold,
    Condition: map[string]interface{}{
        "metric": "heartbeat_timeout",
        "operator": ">",
        "threshold": 30,
    },
    AlertLevel:   types.AlertLevelWarning,
    AlertTitle:   "节点心跳超时",
    AlertMessage: "节点 {{.node_id}} 已超过 {{.timeout}} 秒未上报心跳",
    NotificationChannels: []string{"webhook", "log"},
    Enabled: true,
})
```

### 8.3 创建通知渠道

```go
// 创建Webhook渠道
alertSvc.CreateChannel(ctx, &types.AlertChannel{
    ChannelName: "主Webhook通知",
    ChannelType: types.ChannelTypeWebhook,
    Config: map[string]interface{}{
        "url": "https://example.com/webhook",
        "method": "POST",
        "headers": map[string]string{
            "Content-Type": "application/json",
        },
    },
    Enabled: true,
})
```

### 8.4 创建告警静默

```go
// 创建维护期静默

```

---

## 九、与其他模块集成

### 9.1 心跳模块集成

```go
// 心跳模块在检测到节点超时时触发告警
func (s *HeartbeatService) handleNodeTimeout(ctx context.Context, nodeID string) {
    // 触发告警
    alertSvc.TriggerAlert(ctx, &alert.TriggerAlertRequest{
        SourceService: alert.SourceServiceHeartbeat,
        SourceType:    alert.SourceTypeNode,
        SourceID:      nodeID,
        AlertType:     alert.AlertTypeHeartbeatTimeout,
        AlertLevel:    alert.AlertLevelWarning,
        // ...
    })
}

// 节点恢复时解决告警
func (s *HeartbeatService) handleNodeRecovered(ctx context.Context, nodeID string) {
    // 解决对应的告警
    alerts, _ := alertSvc.ListAlerts(ctx, &alert.AlertFilter{
        SourceID: nodeID,
        Status:   alert.AlertStatusTriggered,
    })
    for _, a := range alerts {}
}
```

### 9.2 异步任务模块集成

```go
// 任务失败时触发告警
func (s *AsyncTaskService) handleTaskFailed(ctx context.Context, taskID string, err error) {
    alertSvc.TriggerAlert(ctx, &alert.TriggerAlertRequest{
        SourceService: alert.SourceServiceAsyncTask,
        SourceType:    alert.SourceTypeTask,
        SourceID:      taskID,
        AlertType:     alert.AlertTypeTaskFailed,
        AlertLevel:    alert.AlertLevelError,
        AlertTitle:    "异步任务执行失败",
        AlertMessage:  fmt.Sprintf("任务 %s 执行失败: %v", taskID, err),
        // ...
    })
}
```

---

## 十、配置示例

```yaml
# alert配置示例
alert:
  # 规则评估
  evaluation:
    interval: 60s              # 评估间隔
    concurrent_limit: 10       # 并发评估数限制

  # 通知配置
  notification:
    async: true                # 异步通知
    retry_count: 3             # 重试次数
    retry_interval: 5s         # 重试间隔
    timeout: 30s               # 超时时间

  # 聚合配置
  aggregation:
    enabled: true              # 是否启用聚合
    window: 5m                 # 聚合窗口
    max_alerts: 100            # 最大聚合告警数

  # 数据清理
  cleanup:
    enabled: true              # 是否启用清理
    retention_days: 90         # 保留天数
    cleanup_interval: 24h      # 清理间隔

  # 性能配置
  performance:
    cache_size: 10000          # 缓存大小
    batch_size: 100            # 批量处理大小
```

---

## 十一、总结

该告警模块设计具有以下优势:

1. **通用性强**: 不绑定特定业务,任何模块都可以接入
2. **功能完善**: 覆盖告警全生命周期
3. **扩展性好**: 支持自定义规则、渠道、聚合策略
4. **易于集成**: 简单的API接口,易于其他模块集成
5. **高性能**: 异步通知、批量处理、缓存优化
6. **可观测性**: 详细的统计和历史记录

该设计完全独立于心跳模块,可以为整个系统提供统一的告警能力。
