# MooX 健康监控整合执行计划

> **供 Agent 执行：** 实施时必须使用 `superpowers:subagent-driven-development`（推荐）或 `superpowers:executing-plans`，严格按任务逐项执行。所有步骤使用复选框跟踪，先测试、后实现，不允许跳过失败测试和最终验证。

**目标（Goal）：** 将现有“总览、可用性监控、应用指标”整合为一个面向个人量化用户的“健康监控”页面；删除用户自定义探测、原始指标浏览和自定义告警规则；所有系统告警使用一个全局消息通知通道，支持企业微信和飞书。

**架构（Architecture）：** Monitor 继续保留 Reporter、HostMetric、系统探测、业务新鲜度、告警状态和告警事件等内部链路，但不再向用户暴露监控对象、阈值或规则的增删改能力。新增服务端健康视图，将技术事实聚合为中文业务健康项和核心服务健康项；通知发送能力统一收口到 `packages/notification`，Monitor 数据库只保存一条 `global` 通知通道记录。Reporter 在发送前过滤技术指标，Monitor 在写入 Storage 和 SQLite 前再次过滤，避免 Go、进程、tRPC、HTTP 请求等无业务价值指标继续占用存储。

**技术栈（Tech Stack）：** Go 1.23、tRPC-Go、Protocol Buffers、GORM、SQLite、JetStream、Prometheus Client、Vue 3、TypeScript、Arco Design、Vitest、Node 契约测试、Playwright、Shell 部署契约。

---

## 一、已确认的产品决策

1. `/ops/services` 最终只保留三个页签：`健康监控`、`网关节点`、`服务实例`。
2. 原来的 `总览`、`可用性监控`、`应用指标`全部由`健康监控`替代；旧的 `tab=overview`、`tab=availability`、`tab=metrics`统一归一到 `tab=health`，不保留旧组件。
3. 用户不能新增、编辑、启停、删除或手动运行探测。
4. 用户不能配置探测周期、超时、失败阈值、恢复阈值、提醒间隔或恢复通知规则。
5. 用户不能注册自定义指标上报、浏览原始指标序列、查看原始 labels JSON、查询原始指标历史或创建指标告警规则。
6. 系统检查项、检查周期、告警阈值和恢复规则全部由 MooX 内部代码和运行时事实维护。
7. 所有由 Monitor 管理的告警共用一个全局消息通知通道。
8. 全局通道支持两种类型：`wecom`（企业微信）和 `feishu`（飞书）。当前一次只能选择其中一种，不支持同时向多个通道发送。
9. 全局通道在数据库中使用固定标识 `global`，不按 Space、Dataset、服务或告警项拆分。
10. 通知 URL 为空表示停止外部通知，但监控采集、健康计算、告警状态转换、告警事件记录和页面展示继续运行。
11. 初始化配置只负责首次写入通知类型和 URL；用户在管理页面修改后，Monitor 重启不能再用初始化配置覆盖数据库。
12. 健康页面只展示中文名称、中文备注、状态、当前结论和最后检查时间。节点 ID、实例 ID、Dataset ID 等技术信息只放在详情抽屉，不展示原始 JSON。
13. 第一版业务健康固定聚合为：`行情采集`、`因子计算`、`交易与余额`。
14. `因子计算`覆盖计算失败、被 Factor 拒绝的无效结果、输入输出时序差和结果停滞。本计划不凭空定义每个因子的合理数值区间；统计异常或因子专属范围需要独立的因子定义方案。
15. 全局通知通道覆盖 Monitor 管理的系统检查、业务新鲜度、Market Canary、主机阈值和主机在线状态。
16. Admin 证书检查等必须在 Monitor 不可用时仍能直发的紧急通知，继续使用部署时下发的通知配置；管理页面修改只对 Monitor 管理的告警即时生效。
17. 当前项目尚未上线，不增加旧 RPC、旧表结构、旧环境变量或旧页面的兼容层。正式部署时允许重置 Monitor 控制面数据并由系统重新生成。

## 二、通知模块与命名决策

### 2.1 共享通知库

现有 `packages/msgbox` 已经提供通用 `Message`、`Sender` 和企业微信实现，但名字不够明确，且 Monitor、Admin 仍直接依赖企业微信构造函数。

本次将其更名并扩展为：

```text
packages/notification
```

职责边界：

- 定义统一消息结构和严重级别；
- 根据通道类型创建发送器；
- 实现企业微信、飞书的请求格式、超时、响应校验和错误清洗；
- 不保存通道配置；
- 不保存告警状态；
- 不执行告警去重；
- 不承担独立服务发现或高可用。

### 2.2 数据表

不使用 `t_monitor_alert_receiver`，改为：

```text
t_monitor_notification_channels
```

表仍由 Monitor 数据库所有，因此保留 `t_monitor_` 前缀；`notification_channels` 表示它是通用消息通知通道，而不是告警规则的接收者字段。

当前表中只允许一条记录：

```text
channel_id   = global
channel_type = wecom | feishu
webhook_url  = 通道机器人 URL
```

### 2.3 初始化配置

删除企业微信专用配置：

```toml
[monitoring]
wecom_webhook = ""
```

改为通用配置：

```toml
[notification]
channel_type = "wecom"
webhook_url = ""
```

部署环境变量同步改为：

```text
MOOX_NOTIFICATION_CHANNEL_TYPE
MOOX_NOTIFICATION_WEBHOOK_URL
```

删除 `MOOX_MSGBOX_WECOM_WEBHOOK`。因为项目未上线，不保留双读、回退或迁移别名。

## 三、验收标准

- `/ops/services?tab=health`只发起一次健康快照请求，并展示当前告警、三个业务健康项和核心服务健康项。
- 页面不存在“新增探测”“编辑探测”“手动运行”“Webhook 管理”“新增告警规则”“原始指标”“指标序列”等入口。
- Monitor 公共 tRPC 服务不存在检查项 CRUD、Webhook CRUD、告警规则 CRUD、原始指标查询、指标规则和公开系统同步 RPC。
- 通知通道页面只允许选择企业微信或飞书，并填写一个全局 Webhook URL。
- `t_monitor_notification_channels`始终只有 `global` 一行，不能按 Space 创建多行。
- 更新通知通道后，Monitor 后续发送无需重启即可使用新类型和新 URL。
- 清空 URL 后，告警状态和事件仍正常生成，但不执行外部发送。
- API 永远不返回完整 Webhook 密钥，只返回是否已配置、通道类型和掩码 URL。
- 企业微信和飞书发送器都覆盖成功、超时、非 2xx、平台业务错误码、超大响应和非法 URL。
- `go_*`、`process_*`、tRPC/HTTP 请求量和耗时等指标在 Reporter 发送前被过滤，并在 Monitor 消费端被二次拒绝。
- Reporter 服务在线状态、Dataset/K 线新鲜度、Factor 时序差、余额同步、SCF Timer 容量、主机监控和 Doctor 诊断保持有效。
- 正式环境重置后不存在旧自定义检查、旧 Webhook、旧指标规则和旧技术指标目录数据。
- Monitor race 测试、Web 单测、前端契约、生产构建、工作区验证、桌面和移动端截图验收全部通过。

## 四、文件变更总览

### 4.1 新增文件

- `packages/notification/channel.go`：通道类型、通道配置和验证规则。
- `packages/notification/message.go`：统一消息和严重级别。
- `packages/notification/sender.go`：发送器接口、工厂和公共选项。
- `packages/notification/wecom.go`：企业微信实现。
- `packages/notification/wecom_test.go`：企业微信请求与响应测试。
- `packages/notification/feishu.go`：飞书实现。
- `packages/notification/feishu_test.go`：飞书请求与响应测试。
- `modules/monitor/internal/domain/notification.go`：全局通知通道持久化模型。
- `modules/monitor/internal/store/notification.go`：全局通道读取、更新和首次初始化。
- `modules/monitor/internal/store/notification_test.go`：单行约束、清空和重启不覆盖测试。
- `modules/monitor/internal/healthview/model.go`：健康摘要、健康项、实例详情和当前告警模型。
- `modules/monitor/internal/healthview/catalog.go`：中文名称、备注、分组、顺序和状态优先级。
- `modules/monitor/internal/healthview/builder.go`：健康事实聚合。
- `modules/monitor/internal/healthview/builder_test.go`：聚合、禁用项过滤和最差状态测试。
- `modules/monitor/internal/metrics/health_filter.go`：业务健康指标白名单。
- `modules/monitor/internal/metrics/health_filter_test.go`：指标保留和丢弃测试。
- `modules/monitor/internal/rpc/health.go`：`GetHealthOverview`。
- `modules/monitor/internal/rpc/notification.go`：`GetNotificationChannel`、`UpdateNotificationChannel`。
- `web/src/api/health-monitor/index.ts`：健康页面专用 API。
- `web/src/api/health-monitor/types.ts`：健康页面专用类型。
- `web/src/views/ops/health-monitor/index.vue`：合并后的健康监控页面。
- `web/src/views/ops/health-monitor/health-display.ts`：状态和时间展示函数。
- `web/src/views/ops/health-monitor/health-display.test.ts`：展示函数测试。
- `web/src/views/ops/health-monitor/index.test.ts`：页面、刷新和通知通道测试。
- `web/scripts/check-health-monitor.mjs`：禁止重新引入自定义监控能力的静态契约。

### 4.2 修改文件

- `go.work`：将 `packages/msgbox`替换为`packages/notification`。
- 所有引用 `packages/msgbox` 的 `go.mod`、Go 源码、测试和验证脚本。
- `modules/monitor/proto/monitor.proto`及`modules/monitor/proto/monitorgen/*`：收窄公共接口。
- `modules/monitor/schema/monitor.sql`：增加通知通道表，删除 Webhook 和指标规则表。
- `modules/monitor/schema/schema_test.go`：校验新表和被删除表。
- `modules/monitor/internal/domain/alert.go`：删除 `WebhookChannel` 和 `AlertRule.WebhookID`。
- `modules/monitor/internal/store/repositories.go`：增加 `Notifications` Repository。
- `modules/monitor/internal/store/alert.go`：删除 Webhook CRUD 和引用校验，增加当前 firing 状态查询。
- `modules/monitor/internal/store/check.go`：删除用户启停覆盖语义，保留系统内部 Upsert。
- `modules/monitor/internal/bootstrap/default_alerts.go`：规则始终启用，通知配置仅首次初始化。
- `modules/monitor/internal/bootstrap/bootstrap.go`：向所有通知路径注入运行时通道 Provider。
- `modules/monitor/internal/bootstrap/host_presence.go`：主机状态变化时动态读取通道。
- `modules/monitor/internal/bootstrap/service_runtime.go`：只注册健康、主机和 Doctor RPC。
- `modules/monitor/internal/bootstrap/schedule_timers.go`：删除指标规则 Timer。
- `modules/monitor/internal/bootstrap/data_cleanup_timer.go`：删除指标规则评估清理。
- `modules/monitor/internal/bootstrap/metrics_runtime.go`：写 Storage 和 SQLite 前过滤样本。
- `modules/monitor/internal/bootstrap/market_canary.go`：Canary 改为系统观测来源。
- `modules/monitor/internal/bootstrap/runtime.go`：删除指标规则调度器状态。
- `modules/monitor/internal/alerting/evaluator.go`：每次发送读取全局通知通道。
- `modules/monitor/internal/alerting/webhook.go`：改为使用 `notification.NewSender`。
- `modules/monitor/internal/hostmetrics/alerts.go`：使用相同的全局通知通道。
- `modules/monitor/internal/metrics/stores.go`：仅保留消息、目录和最新值存储。
- `modules/monitor/internal/rpc/service.go`：删除公共配置处理器。
- `modules/monitor/internal/observability/overview.go`：继续提供内部技术事实，不再直接暴露给页面。
- `modules/monitor/config/trpc_go.yaml`：删除 `metric_rule.timer`。
- `packages/report/config.go`：默认只发送 `moox_*`，删除用户 include/exclude 覆盖。
- `moox.toml.example`及相关初始化说明：增加 `[notification]`。
- `modules/cli/internal/setup/config/config.go`：解析和校验通用通知配置。
- `modules/cli/internal/setup/deploy/deploy.go`：生成新的通知环境变量。
- `scripts/deploy/deploy-moox.sh`：读取新的通用通知变量。
- `modules/admin/internal/bootstrap/certificate_watch.go`：通过通用通知工厂创建紧急发送器。
- `web/src/views/ops/service-management/index.vue`：页签改为`health`。
- `web/src/views/ops/service-management/gateway-nodes.test.ts`：断言三个页签。
- `web/scripts/check-service-management-contract.mjs`：要求健康监控，禁止旧页签。
- `web/package.json`：将`check:metric-monitor`替换为`check:health-monitor`。
- `web/src/lang/modules/zhCN.ts`：删除旧监控文案。
- `modules/monitor/README.md`、`docs/监控配置.md`、`docs/运维/MooX指标监控.md`和`skills/moox/references/custom-setup.md`：更新新模型。

### 4.3 删除文件

- `packages/msgbox/*`
- `modules/monitor/internal/store/check_override.go`
- `modules/monitor/internal/store/check_override_test.go`
- `modules/monitor/internal/metrics/rule_store.go`
- `modules/monitor/internal/metrics/rule_store_test.go`
- `modules/monitor/internal/metrics/evaluator.go`
- `modules/monitor/internal/metrics/evaluator_test.go`
- `modules/monitor/internal/metrics/scheduler.go`
- `modules/monitor/internal/metrics/scheduler_test.go`
- `modules/monitor/internal/metrics/notification.go`
- `modules/monitor/internal/metrics/notification_test.go`
- `modules/monitor/internal/rpc/metrics.go`
- `web/src/views/ops/service-monitor/index.vue`
- `web/src/views/ops/metric-monitor/index.vue`
- `web/src/views/ops/metric-monitor/metric-chart.vue`
- `web/src/views/ops/metric-monitor/metric-condition-row.vue`
- `web/src/views/ops/metric-monitor/metric-rule-editor.vue`
- `web/src/views/ops/metric-monitor/metric-display.ts`
- `web/src/views/ops/metric-monitor/metric-display.test.ts`
- `web/src/views/ops/metric-monitor/observability-overview.vue`
- `web/src/views/ops/metric-monitor/observability-overview.test.ts`
- `web/src/api/monitor/index.ts`
- `web/src/api/monitor/types.ts`
- `web/src/api/metric-monitor/index.ts`
- `web/src/api/metric-monitor/types.ts`
- `web/scripts/check-metric-monitor.mjs`

---

## 五、分步执行任务

### 任务 1：先用失败测试锁定新的产品边界

**涉及文件：**

- 修改：`web/scripts/check-service-management-contract.mjs`
- 新增：`web/scripts/check-health-monitor.mjs`
- 修改：`web/src/views/ops/service-management/gateway-nodes.test.ts`
- 新增：`modules/monitor/proto/contract_test.go`
- 修改：`web/package.json`

- [ ] **步骤 1：将服务管理页签契约改为三个页签**

前端静态契约必须要求：

```js
const required = [
  'PageTitleTabs',
  'aria-label="服务管理"',
  /label:\s*["']健康监控["']/,
  /label:\s*["']网关节点["']/,
  /label:\s*["']服务实例["']/
];

const forbidden = [
  /label:\s*["']总览["']/,
  /label:\s*["']可用性监控["']/,
  /label:\s*["']应用指标["']/
];
```

- [ ] **步骤 2：增加健康页面禁止能力契约**

`check-health-monitor.mjs`必须检查新页面、新 API 和全部 Web 源码，并禁止以下接口或文案重新出现：

```js
const forbiddenSymbols = [
  "CreateCheck", "UpdateCheck", "DeleteCheck", "RunCheckOnce",
  "CreateWebhookChannel", "DeleteWebhookChannel",
  "CreateAlertRule", "UpdateAlertRule", "DeleteAlertRule",
  "ListMetricServices", "QueryMetricHistory", "CreateMetricRule"
];

const forbiddenTexts = [
  "新增探测", "编辑探测", "手动运行", "新增告警规则",
  "原始指标名", "序列 ID", "Headers JSON", "Body Template"
];
```

- [ ] **步骤 3：增加 Monitor 公共 RPC 白名单测试**

测试读取 `monitor.proto` 的 `MonitorMgr` 服务体，只允许：

```go
allowed := []string{
	"GetHealthOverview",
	"GetNotificationChannel",
	"UpdateNotificationChannel",
	"ListHostAgents",
	"QueryHostMetricHistory",
	"GetDoctorContext",
}
```

明确禁止检查项 CRUD、Webhook CRUD、告警规则 CRUD、原始指标和指标规则 RPC。

- [ ] **步骤 4：运行测试并确认失败**

```bash
cd web
pnpm check:service-management
pnpm check:health-monitor
cd ../modules/monitor
go test ./proto -run TestMonitorPublicRPCSurface -count=1
```

预期结果：新页面和新 RPC 尚未存在，三个测试均失败；失败原因必须指向缺失的新能力或仍存在的旧能力。

---

### 任务 2：将 `packages/msgbox` 重构为通用通知库

**涉及文件：**

- 新增：`packages/notification/channel.go`
- 新增：`packages/notification/message.go`
- 新增：`packages/notification/sender.go`
- 新增：`packages/notification/wecom.go`
- 新增：`packages/notification/wecom_test.go`
- 新增：`packages/notification/feishu.go`
- 新增：`packages/notification/feishu_test.go`
- 修改：`go.work`
- 修改：所有引用 `packages/msgbox` 的 `go.mod`、Go 文件和脚本
- 删除：`packages/msgbox`

- [ ] **步骤 1：先写通道工厂失败测试**

覆盖类型分发、非法类型和空 URL：

```go
func TestNewSenderSelectsChannelImplementation(t *testing.T) {
	wecom, err := NewSender(Channel{Type: ChannelTypeWeCom, WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"})
	require.NoError(t, err)
	require.IsType(t, &WeComSender{}, wecom)

	feishu, err := NewSender(Channel{Type: ChannelTypeFeishu, WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/test"})
	require.NoError(t, err)
	require.IsType(t, &FeishuSender{}, feishu)

	_, err = NewSender(Channel{Type: "unknown", WebhookURL: "https://example.invalid/hook"})
	require.ErrorContains(t, err, "unsupported notification channel type")
}
```

- [ ] **步骤 2：定义统一通道类型和消息接口**

```go
type ChannelType string

const (
	ChannelTypeWeCom  ChannelType = "wecom"
	ChannelTypeFeishu ChannelType = "feishu"
)

type Channel struct {
	Type       ChannelType
	WebhookURL string
}

type Sender interface {
	Send(context.Context, Message) error
}

func NewSender(Channel, ...Option) (Sender, error)
```

保留当前 `Message` 的 `Key`、`Severity`、`Title`、`Body`、`Labels` 和长度限制。

- [ ] **步骤 3：迁移企业微信实现**

迁移当前 HTTPS、无重定向、10 秒默认超时、64 KiB 响应限制、业务 `errcode` 校验和 Markdown 渲染。错误信息从 `msgbox:` 改为 `notification:`，不得包含完整 Webhook URL。

- [ ] **步骤 4：实现飞书机器人发送器**

飞书发送器使用统一 `Message` 生成飞书消息体，至少覆盖：

- 标题和严重等级；
- 告警标识；
- 正文；
- 排序后的标签；
- HTTP 非 2xx；
- 飞书业务错误码；
- 超时；
- 重定向拒绝；
- 响应大小限制。

- [ ] **步骤 5：更新所有依赖路径**

Monitor、Admin 和验证脚本统一导入：

```go
github.com/mooyang-code/moox/packages/notification
```

不允许生产代码继续引用 `packages/msgbox` 或直接构造企业微信发送器。

- [ ] **步骤 6：运行共享包测试**

```bash
cd packages/notification
go test -race ./... -count=1
cd ../..
rg -n "packages/msgbox|msgbox\." --glob '!docs/superpowers/plans/**'
```

预期结果：测试通过；搜索无生产代码匹配。

- [ ] **步骤 7：提交通用通知库**

```bash
git add go.work packages modules/admin modules/monitor scripts
git commit -m "refactor: generalize notification channels"
```

---

### 任务 3：建立唯一的全局通知通道存储

**涉及文件：**

- 新增：`modules/monitor/internal/domain/notification.go`
- 新增：`modules/monitor/internal/store/notification.go`
- 新增：`modules/monitor/internal/store/notification_test.go`
- 修改：`modules/monitor/internal/domain/alert.go`
- 修改：`modules/monitor/internal/store/repositories.go`
- 修改：`modules/monitor/internal/store/alert.go`
- 修改：`modules/monitor/schema/monitor.sql`
- 修改：`modules/monitor/schema/schema_test.go`
- 修改：`modules/monitor/internal/bootstrap/default_alerts.go`
- 修改：`modules/monitor/internal/bootstrap/default_alerts_test.go`

- [ ] **步骤 1：先写唯一记录和首次初始化测试**

```go
func TestNotificationChannelSeedDoesNotOverwriteUserValue(t *testing.T) {
	repo := openNotificationRepository(t)
	require.NoError(t, repo.SeedIfAbsent(t.Context(), domain.NotificationChannel{
		ChannelID: "global", ChannelType: "wecom", WebhookURL: wecomURL("seed"),
	}))
	require.NoError(t, repo.UpdateGlobal(t.Context(), "feishu", feishuURL("user")))
	require.NoError(t, repo.SeedIfAbsent(t.Context(), domain.NotificationChannel{
		ChannelID: "global", ChannelType: "wecom", WebhookURL: wecomURL("restart"),
	}))

	got, err := repo.GetGlobal(t.Context())
	require.NoError(t, err)
	require.Equal(t, "feishu", got.ChannelType)
	require.Equal(t, feishuURL("user"), got.WebhookURL)
}
```

再增加：

- 只能插入 `global`；
- 第二行写入失败；
- URL 可以显式清空；
- 非法通道类型被拒绝；
- 清空后仍保留用户选择的通道类型。

- [ ] **步骤 2：定义持久化模型**

```go
const GlobalNotificationChannelID = "global"

type NotificationChannel struct {
	ID          int64     `gorm:"column:c_id;primaryKey"`
	ChannelID   string    `gorm:"column:c_channel_id"`
	ChannelType string    `gorm:"column:c_channel_type"`
	WebhookURL  string    `gorm:"column:c_webhook_url"`
	CreatedAt   time.Time `gorm:"column:c_ctime"`
	UpdatedAt   time.Time `gorm:"column:c_mtime"`
}

func (NotificationChannel) TableName() string {
	return "t_monitor_notification_channels"
}
```

- [ ] **步骤 3：定义绿色项目最终 Schema**

```sql
CREATE TABLE t_monitor_notification_channels (
    c_id INTEGER PRIMARY KEY,
    c_channel_id TEXT NOT NULL UNIQUE CHECK (c_channel_id = 'global'),
    c_channel_type TEXT NOT NULL DEFAULT 'wecom'
        CHECK (c_channel_type IN ('wecom', 'feishu')),
    c_webhook_url TEXT NOT NULL DEFAULT '',
    c_ctime TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

删除 `t_monitor_webhooks`，并从 `t_monitor_alert_rules` 删除 `c_webhook_id`。

- [ ] **步骤 4：实现窄 Repository**

```go
type NotificationRepository struct {
	db *gorm.DB
}

func (r *NotificationRepository) GetGlobal(context.Context) (*domain.NotificationChannel, error)
func (r *NotificationRepository) UpdateGlobal(context.Context, string, string) error
func (r *NotificationRepository) SeedIfAbsent(context.Context, domain.NotificationChannel) error
```

不提供 List、Create、Delete，也不接受 Space ID。

- [ ] **步骤 5：告警规则与通知通道解耦**

从 `AlertRule` 删除 `WebhookID`。系统规则只描述检查项、触发次数、恢复次数、提醒间隔和恢复通知，不再绑定通道。

- [ ] **步骤 6：无 URL 时仍创建并启用内置规则**

`ensureDefaultCheckAlertRules`和`ensureDefaultHostAlertRules`必须始终维护规则。初始化逻辑为：

```go
channelType := strings.TrimSpace(os.Getenv("MOOX_NOTIFICATION_CHANNEL_TYPE"))
if channelType == "" {
	channelType = string(notification.ChannelTypeWeCom)
}
webhookURL := strings.TrimSpace(os.Getenv("MOOX_NOTIFICATION_WEBHOOK_URL"))
if webhookURL != "" {
	if _, err := notification.NewSender(notification.Channel{
		Type: notification.ChannelType(channelType), WebhookURL: webhookURL,
	}); err != nil {
		return fmt.Errorf("initialize notification channel: %w", err)
	}
}
return repositories.Notifications.SeedIfAbsent(ctx, domain.NotificationChannel{
	ChannelID: GlobalNotificationChannelID,
	ChannelType: channelType,
	WebhookURL: webhookURL,
})
```

- [ ] **步骤 7：运行 Schema、Store 和初始化测试**

```bash
cd modules/monitor
go test ./schema ./internal/store ./internal/bootstrap -run 'Notification|DefaultAlert|Schema' -count=1
```

预期结果：测试通过；新表存在，旧 Webhook 表不存在，空 URL 不会禁用规则。

- [ ] **步骤 8：提交全局通知通道存储**

```bash
git add modules/monitor/internal/domain modules/monitor/internal/store modules/monitor/internal/bootstrap/default_alerts.go modules/monitor/internal/bootstrap/default_alerts_test.go modules/monitor/schema
git commit -m "refactor: store one global notification channel"
```

---

### 任务 4：统一 Monitor 管理的全部通知发送路径

**涉及文件：**

- 修改：`modules/monitor/internal/alerting/evaluator.go`
- 修改：`modules/monitor/internal/alerting/evaluator_test.go`
- 修改：`modules/monitor/internal/alerting/webhook.go`
- 修改：`modules/monitor/internal/hostmetrics/alerts.go`
- 修改：`modules/monitor/internal/hostmetrics/alerts_test.go`
- 修改：`modules/monitor/internal/bootstrap/host_presence.go`
- 修改：`modules/monitor/internal/bootstrap/host_presence_test.go`
- 修改：`modules/monitor/internal/bootstrap/bootstrap.go`

- [ ] **步骤 1：先写运行时切换通道测试**

```go
func TestEvaluatorReadsGlobalChannelForEverySend(t *testing.T) {
	provider := &fakeNotificationProvider{channel: wecomChannel("first")}
	sender := &recordingSenderFactory{}
	evaluator := newEvaluatorForTest(t, provider, sender)

	require.NoError(t, evaluator.Evaluate(t.Context(), failingCheck(), failingResult()))
	provider.channel = feishuChannel("second")
	require.NoError(t, evaluator.Evaluate(t.Context(), recoveredCheck(), recoveredResult()))

	require.Equal(t, []string{"wecom", "feishu"}, sender.channelTypes)
}
```

同时验证空 URL 仍写入 firing/resolved 事件，但发送次数为零。

- [ ] **步骤 2：统一 Provider 和发送工厂边界**

```go
type NotificationProvider interface {
	GetGlobal(context.Context) (*domain.NotificationChannel, error)
}

type SenderFactory interface {
	New(notification.Channel, ...notification.Option) (notification.Sender, error)
}
```

普通检查、主机阈值和主机状态变化全部依赖这两个边界。

- [ ] **步骤 3：固定事件和发送顺序**

每次状态变化必须按以下顺序处理：

1. 写入告警事件；
2. 写入告警状态；
3. 读取 `global` 通道；
4. URL 为空则成功结束；
5. 根据 `channel_type` 创建发送器；
6. 尝试发送；
7. 发送失败则追加 `send_failed`，但不回滚原始事件和状态。

- [ ] **步骤 4：主机在线状态不再缓存启动时 Sender**

`hostPresenceTransitionSink`每次状态变化重新读取通道。Monitor 的 `bootstrap.go`不再直接读取通知环境变量，也不在启动时固定企业微信 Sender。

- [ ] **步骤 5：运行告警和主机 race 测试**

```bash
cd modules/monitor
go test -race ./internal/alerting ./internal/hostmetrics ./internal/bootstrap -run 'Alert|Presence|Notification' -count=1
```

预期结果：修改类型或 URL 后无需重启即可生效；空 URL 不影响事件和状态。

- [ ] **步骤 6：提交通知链路统一改造**

```bash
git add modules/monitor/internal/alerting modules/monitor/internal/hostmetrics modules/monitor/internal/bootstrap
git commit -m "refactor: route monitor alerts through global channel"
```

---

### 任务 5：构建面向用户的中文健康视图

**涉及文件：**

- 新增：`modules/monitor/internal/healthview/model.go`
- 新增：`modules/monitor/internal/healthview/catalog.go`
- 新增：`modules/monitor/internal/healthview/builder.go`
- 新增：`modules/monitor/internal/healthview/builder_test.go`
- 修改：`modules/monitor/internal/store/alert.go`
- 修改：`modules/monitor/internal/observability/overview.go`
- 修改：`modules/monitor/internal/observability/overview_test.go`

- [ ] **步骤 1：先写业务聚合失败测试**

```go
func TestHealthViewAggregatesBusinessItemsWithWorstStatus(t *testing.T) {
	facts := observability.Overview{
		Datasets: []observability.DatasetFrequencyStatus{
			{Producer: "collector", DatasetID: "dataset_binance_spot_kline_1m", Status: "healthy"},
			{Producer: "collector", DatasetID: "dataset_spot_kline_1h", Status: "stale", Reason: "run stale"},
			{Producer: "factor", DatasetID: "binance_spot_kline_1m_factor", Status: "healthy"},
		},
		BusinessChecks: []observability.BusinessStatus{{Kind: "balance", Status: "healthy"}},
	}

	got := BuildFromFacts(facts, nil)
	require.Equal(t, []string{"行情采集", "因子计算", "交易与余额"}, itemNames(got.BusinessItems))
	require.Equal(t, StatusWarning, got.BusinessItems[0].Status)
}
```

- [ ] **步骤 2：定义页面专用模型**

```go
type Item struct {
	Key, Category, Name, Remark, Status, Conclusion string
	LastCheckedAt                                   time.Time
	Instances                                       []Instance
}

type Instance struct {
	Name, NodeID, InstanceID, Status, Conclusion string
	LastCheckedAt                                  time.Time
}

type ActiveAlert struct {
	Key, Severity, Name, Remark, Conclusion string
	StartedAt, LastCheckedAt                 time.Time
}

type Overview struct {
	GeneratedAt   time.Time
	Summary       Summary
	ActiveAlerts  []ActiveAlert
	BusinessItems []Item
	ServiceItems  []Item
}
```

模型中不出现原始 labels JSON、指标表达式或探测配置。

- [ ] **步骤 3：建立中文名称和备注目录**

```go
var businessCatalog = []Definition{
	{Key: "market", Name: "行情采集", Remark: "检查 K 线采集、数据完整性、更新延迟和 SCF 容量"},
	{Key: "factor", Name: "因子计算", Remark: "检查计算失败、输入输出时间差和被拒绝的无效结果"},
	{Key: "trade", Name: "交易与余额", Remark: "检查账户余额同步、余额差异和交易服务状态"},
}

var serviceCatalog = []Definition{
	{Key: "storage", Name: "数据存储", Remark: "保存原始数据并维护查询视图"},
	{Key: "eventbus", Name: "事件总线", Remark: "在采集、存储和计算模块之间传递任务与数据"},
	{Key: "gateway", Name: "服务网关", Remark: "为管理页面和内部服务提供统一访问入口"},
	{Key: "cloud", Name: "云端执行", Remark: "管理 SCF 节点和远程采集任务"},
	{Key: "monitor", Name: "系统监控", Remark: "汇总服务、主机和业务健康状态"},
}
```

- [ ] **步骤 4：实现确定性的聚合规则**

状态优先级固定为：

```text
down > degraded/stale > unknown > healthy
```

按组取最差状态，去重旧 Boot 实例，过滤禁用检查，异常项优先展示。每组详情最多返回 100 个实例；若被截断，返回 `omitted_instance_count`，不能静默丢弃。

- [ ] **步骤 5：当前告警必须来自 firing 状态**

为 Alert Repository 增加有上限的 `ListFiringStates`。不能根据旧失败结果推断当前告警；主机规则使用 `hostmetrics.ParseHostRuleKey`生成中文名称。

- [ ] **步骤 6：运行健康视图回归测试**

```bash
cd modules/monitor
go test ./internal/healthview ./internal/observability ./internal/store -count=1
```

预期结果：Dataset、Timer 容量、余额、服务和主机事实测试保持通过。

- [ ] **步骤 7：提交健康视图**

```bash
git add modules/monitor/internal/healthview modules/monitor/internal/observability modules/monitor/internal/store
git commit -m "feat: add Chinese business health view"
```

---

### 任务 6：收窄 Monitor 公共 RPC

**涉及文件：**

- 修改：`modules/monitor/proto/monitor.proto`
- 重新生成：`modules/monitor/proto/monitorgen/monitor.pb.go`
- 重新生成：`modules/monitor/proto/monitorgen/monitor.trpc.go`
- 重新生成：`modules/monitor/proto/monitorgen/validation.go`
- 新增：`modules/monitor/internal/rpc/health.go`
- 新增：`modules/monitor/internal/rpc/notification.go`
- 修改：`modules/monitor/internal/rpc/service.go`
- 修改：`modules/monitor/internal/rpc/convert.go`
- 修改：`modules/monitor/internal/rpc/service_test.go`
- 删除：`modules/monitor/internal/rpc/metrics.go`
- 修改：`modules/monitor/internal/bootstrap/service_runtime.go`

- [ ] **步骤 1：先写通知通道 RPC 失败测试**

```go
func TestGetNotificationChannelDoesNotExposeSecret(t *testing.T) {
	service := newNotificationRPCService(t, "wecom", wecomURL("secret-key"))
	rsp, err := service.GetNotificationChannel(t.Context(), &monitorpb.GetNotificationChannelReq{})
	require.NoError(t, err)
	require.True(t, rsp.GetChannel().GetConfigured())
	require.Equal(t, "wecom", rsp.GetChannel().GetChannelType())
	require.NotContains(t, rsp.GetChannel().GetMaskedUrl(), "secret-key")
}
```

另写测试验证：企业微信 URL、飞书 URL、类型与 URL 不匹配、非法 HTTPS URL、显式清空。

- [ ] **步骤 2：将公共服务缩减为六个 RPC**

```proto
service MonitorMgr {
  rpc GetHealthOverview(GetHealthOverviewReq) returns (GetHealthOverviewRsp);
  rpc GetNotificationChannel(GetNotificationChannelReq) returns (GetNotificationChannelRsp);
  rpc UpdateNotificationChannel(UpdateNotificationChannelReq) returns (UpdateNotificationChannelRsp);
  rpc ListHostAgents(ListHostAgentsReq) returns (ListHostAgentsRsp);
  rpc QueryHostMetricHistory(QueryHostMetricHistoryReq) returns (QueryHostMetricHistoryRsp);
  rpc GetDoctorContext(GetDoctorContextReq) returns (GetDoctorContextRsp);
}
```

- [ ] **步骤 3：定义窄通知通道消息**

```proto
message NotificationChannelSetting {
  string channel_type = 1;
  bool configured = 2;
  string masked_url = 3;
  string updated_at = 4;
}

message GetNotificationChannelReq {}
message GetNotificationChannelRsp {
  common.RetInfo ret_info = 1;
  NotificationChannelSetting channel = 2;
}

message UpdateNotificationChannelReq {
  string channel_type = 1;
  string webhook_url = 2;
}

message UpdateNotificationChannelRsp {
  common.RetInfo ret_info = 1;
  NotificationChannelSetting channel = 2;
}
```

- [ ] **步骤 4：删除无用 Proto 消息和 RPC**

删除检查项管理、Webhook、告警规则管理、原始指标目录/历史和指标规则相关消息。Host 和 Doctor 消息保留。

- [ ] **步骤 5：重新生成 tRPC 代码**

```bash
cd modules/monitor/proto
make clean all
```

预期结果：生成服务描述只包含上述六个方法。

- [ ] **步骤 6：实现健康和通知通道 RPC**

`UpdateNotificationChannel`必须：

1. 去除首尾空格；
2. 校验 `wecom` 或 `feishu`；
3. URL 非空时通过 `notification.NewSender`验证；
4. 更新唯一 `global` 行；
5. 只返回掩码结果。

- [ ] **步骤 7：删除公共配置处理器**

从 `service.go`删除检查项 CRUD、手动运行、Webhook CRUD、告警规则 CRUD和系统同步 RPC；删除 `metrics.go`。内部健康视图和 Doctor 仍可直接使用 QueryService。

- [ ] **步骤 8：运行 Proto 和 RPC 测试**

```bash
cd modules/monitor
go test ./proto ./proto/monitorgen ./internal/rpc -count=1
```

预期结果：任务 1 的公共 RPC 白名单测试转为通过。

- [ ] **步骤 9：提交窄 API**

```bash
git add modules/monitor/proto modules/monitor/internal/rpc modules/monitor/internal/bootstrap/service_runtime.go
git commit -m "refactor: expose only health and notification APIs"
```

---

### 任务 7：删除用户覆盖和自定义指标规则引擎

**涉及文件：**

- 删除：`modules/monitor/internal/store/check_override.go`
- 删除：`modules/monitor/internal/store/check_override_test.go`
- 新增：`modules/monitor/internal/store/check_system_test.go`
- 修改：`modules/monitor/internal/store/check.go`
- 修改：`modules/monitor/internal/domain/check.go`
- 修改：`modules/monitor/internal/bootstrap/market_canary.go`
- 删除：`modules/monitor/internal/metrics/rule_store.go`
- 删除：`modules/monitor/internal/metrics/evaluator.go`
- 删除：`modules/monitor/internal/metrics/scheduler.go`
- 删除：`modules/monitor/internal/metrics/notification.go`
- 删除：上述文件对应测试
- 修改：`modules/monitor/internal/metrics/stores.go`
- 修改：`modules/monitor/internal/bootstrap/bootstrap.go`
- 修改：`modules/monitor/internal/bootstrap/runtime.go`
- 修改：`modules/monitor/internal/bootstrap/schedule_timers.go`
- 修改：`modules/monitor/internal/bootstrap/schedule_timers_test.go`
- 修改：`modules/monitor/internal/bootstrap/data_cleanup_timer.go`
- 修改：`modules/monitor/internal/bootstrap/data_cleanup_timer_test.go`
- 修改：`modules/monitor/config/trpc_go.yaml`
- 修改：`modules/monitor/schema/monitor.sql`
- 修改：`modules/monitor/schema/schema_test.go`

- [ ] **步骤 1：先写系统所有权测试**

系统同步必须完整覆盖启用状态，不能保留用户 override：

```go
func TestSystemCheckUpsertReplacesEnabledState(t *testing.T) {
	repo := openCheckRepository(t)
	require.NoError(t, repo.UpsertSystemCheck(t.Context(), domain.Check{CheckID: "sysdeploy:control:storage", Enabled: true}))
	require.NoError(t, repo.UpsertSystemCheck(t.Context(), domain.Check{CheckID: "sysdeploy:control:storage", Enabled: false}))
	got, err := repo.Get(t.Context(), "", "sysdeploy:control:storage")
	require.NoError(t, err)
	require.False(t, got.Enabled)
}
```

- [ ] **步骤 2：删除 manual 来源和覆盖逻辑**

删除 `CheckSourceManual`。Market Canary 使用 `CheckSourceObservability`。保留 SysDeploy、Market Canary 和业务新鲜度内部所需的 Create/Upsert/Delete，但不再有公共 RPC。

- [ ] **步骤 3：删除指标规则 Store 和运行时**

`Stores`最终为：

```go
type Stores struct {
	Messages *MetricMessageStore
}

func NewStores(db *gorm.DB) *Stores {
	return &Stores{Messages: NewMetricMessageStore(db)}
}
```

- [ ] **步骤 4：删除指标规则 Timer 和清理任务**

`registerMonitorScheduleTimers`只注册系统检查调度。删除 `trpc.moox.monitor.metric_rule.timer`；数据清理只保留检查结果、告警事件和指标去重记录清理。

- [ ] **步骤 5：删除四张指标规则表**

删除：

```text
t_monitor_metric_rules
t_monitor_metric_rule_states
t_monitor_metric_rule_evaluations
t_monitor_metric_rule_channels
```

保留服务、序列、最新值和 ingest-message 表，因为内部健康视图和 Doctor 仍依赖它们。

- [ ] **步骤 6：运行 Monitor 完整单测**

```bash
cd modules/monitor
go test ./... -count=1
```

预期结果：通过；生产代码不再引用 `MetricRule`、`WebhookChannel`、`CheckSourceManual` 或 `metric_rule.timer`。

- [ ] **步骤 7：提交删除逻辑**

```bash
git add -A modules/monitor
git commit -m "refactor: remove custom monitoring configuration"
```

---

### 任务 8：停止发送和存储无业务价值的技术指标

**涉及文件：**

- 修改：`packages/report/config.go`
- 修改：`packages/report/config_test.go`
- 修改：`packages/report/handler_test.go`
- 新增：`modules/monitor/internal/metrics/health_filter.go`
- 新增：`modules/monitor/internal/metrics/health_filter_test.go`
- 修改：`modules/monitor/internal/bootstrap/metrics_runtime.go`
- 修改：对应 ingest route 测试
- 修改：`modules/monitor/internal/observability/overview_test.go`
- 修改：`modules/monitor/internal/doctor/context_test.go`

- [ ] **步骤 1：先写 Reporter 过滤失败测试**

同一个 Registry 注册以下指标：

```text
go_goroutines
process_cpu_seconds_total
trpc_client_requests_total
http_requests_total
moox_collector_dataset_output_watermark_timestamp_seconds
```

构建快照后只允许最后一个指标出现。

- [ ] **步骤 2：收紧 Reporter 默认规则**

```go
IncludeRegex: `^moox_.*$`,
ExcludeRegex: `^$`,
```

删除 `MOOX_METRICS_INCLUDE_REGEX` 和 `MOOX_METRICS_EXCLUDE_REGEX` 的读取，避免部署用户重新放开全部 Prometheus 指标。大小、身份、EventBus 和凭据配置继续保留。

- [ ] **步骤 3：增加 Monitor 端业务指标白名单**

动态 Dataset 指标保留以下后缀：

```go
var healthSuffixes = []string{
	"_dataset_enabled",
	"_dataset_expected_interval_seconds",
	"_dataset_last_run_timestamp_seconds",
	"_dataset_last_success_timestamp_seconds",
	"_dataset_input_watermark_timestamp_seconds",
	"_dataset_output_watermark_timestamp_seconds",
	"_dataset_inventory_last_success_timestamp_seconds",
	"_runs_total",
	"_last_success_timestamp_seconds",
	"_last_error_timestamp_seconds",
	"_business_watermark_timestamp_seconds",
	"_input_watermark_timestamp_seconds",
	"_metrics_errors_total",
	"_metrics_last_error_timestamp_seconds",
}
```

另以精确名称保留 Storage View 水位、Trade 余额四项和 Collector SCF Timer 协调/容量指标。不能简单允许全部 `moox_*`。

- [ ] **步骤 4：Storage 和 SQLite 写入前共同过滤**

`ParseSnapshot`之后立即执行：

```go
samples = monmetrics.FilterHealthSamples(samples)
```

即使样本全部被过滤，也必须提交 `MetricService.LastSeenAt` 和 message dedupe，避免服务被误判离线。

- [ ] **步骤 5：覆盖所有必要指标**

表驱动测试必须覆盖：

- Dataset/K 线和 Factor 新鲜度；
- Storage View 输出水位；
- Trade 余额同步；
- Collector SCF Timer 协调和容量；
- Doctor 使用的七类标准模块指标。

- [ ] **步骤 6：运行 Reporter 和 Monitor 回归测试**

```bash
go test ./packages/report -count=1
cd modules/monitor
go test ./internal/metrics ./internal/bootstrap ./internal/observability ./internal/doctor -count=1
```

预期结果：服务在线事实继续更新，不产生技术指标序列。

- [ ] **步骤 7：提交指标精简**

```bash
git add packages/report modules/monitor/internal/metrics modules/monitor/internal/bootstrap/metrics_runtime.go modules/monitor/internal/observability modules/monitor/internal/doctor
git commit -m "refactor: retain only business health metrics"
```

---

### 任务 9：合并前端健康页面并删除旧控制台

**涉及文件：**

- 新增：`web/src/api/health-monitor/index.ts`
- 新增：`web/src/api/health-monitor/types.ts`
- 新增：`web/src/views/ops/health-monitor/index.vue`
- 新增：`web/src/views/ops/health-monitor/health-display.ts`
- 新增：对应测试
- 新增：`web/scripts/check-health-monitor.mjs`
- 修改：`web/src/views/ops/service-management/index.vue`
- 修改：`web/src/views/ops/service-management/gateway-nodes.test.ts`
- 修改：`web/scripts/check-service-management-contract.mjs`
- 修改：`web/package.json`
- 修改：`web/src/lang/modules/zhCN.ts`
- 删除：文件总览中列出的旧页面、旧 API、旧类型和旧契约

- [ ] **步骤 1：先写 API 和页面失败测试**

API 只允许：

```ts
expect(callControl).toHaveBeenCalledWith("moox_monitor", "GetHealthOverview", {});
expect(callControl).toHaveBeenCalledWith("moox_monitor", "GetNotificationChannel", {});
expect(callControl).toHaveBeenCalledWith("moox_monitor", "UpdateNotificationChannel", {
  channel_type: "feishu",
  webhook_url: newURL
});
```

页面契约要求`当前告警`、`业务健康`、`核心服务`、`消息通知设置`，并禁止原始 JSON、阈值、周期和自定义操作。

- [ ] **步骤 2：定义页面专用 TypeScript 类型**

```ts
export interface HealthItem {
  key?: string;
  category?: string;
  name?: string;
  remark?: string;
  status?: string | number;
  conclusion?: string;
  last_checked_at?: string;
  instances?: HealthInstance[];
}

export interface NotificationChannelSetting {
  channel_type?: "wecom" | "feishu";
  configured?: boolean;
  masked_url?: string;
  updated_at?: string;
}
```

新类型中不出现探测目标、HTTP Method、Headers、Body、阈值表达式、指标名、序列 ID 或 labels JSON。

- [ ] **步骤 3：实现一次请求的健康页面**

页面从上到下使用三个无嵌套卡片的区域：

1. 当前仍在 firing 的告警；
2. 三个业务健康项；
3. 核心服务健康项。

每行只展示状态、中文名称、备注、结论和最后检查时间。详情抽屉使用结构化实例字段，不允许 `JSON.stringify` 服务端对象。

- [ ] **步骤 4：实现全局消息通知设置**

页面右上角保留一个设置图标按钮。弹窗包含：

- 通道类型单选或下拉：企业微信、飞书；
- Webhook URL 输入框；
- 当前掩码状态；
- 保存和取消；
- 清空 URL 时的二次确认。

页面不能读取完整旧 URL。打开弹窗并取消不能改动配置。

- [ ] **步骤 5：实现刷新和响应式布局**

每 30 秒刷新一次，保留手动刷新图标，使用现有 latest-request guard 丢弃过期响应。通知弹窗打开期间暂停自动刷新。小于 640 px 时切换为紧凑纵向行，长中文结论必须换行且不能覆盖状态和操作。

- [ ] **步骤 6：将服务管理页签改为三个**

```ts
type ServiceManagementTab = "health" | "nodes" | "instances";

const tabs = [
  { key: "health", label: "健康监控" },
  { key: "nodes", label: "网关节点" },
  { key: "instances", label: "服务实例" }
] as const;
```

其他 `tab` 值全部归一到 `health`。

- [ ] **步骤 7：删除旧前端能力**

删除可用性监控、应用指标、原始指标图表、指标规则编辑器、Webhook 管理和对应 API。VChart 依赖不能一起删除，因为主机和资源页面仍在使用。

- [ ] **步骤 8：运行前端测试和生产构建**

```bash
cd web
pnpm test
pnpm check:service-management
pnpm check:health-monitor
pnpm lint:eslint:check
pnpm build:prod
```

预期结果：全部通过；TypeScript 编译成功；静态契约找不到退役能力。

- [ ] **步骤 9：提交合并页面**

```bash
git add -A web
git commit -m "feat: consolidate health monitoring UI"
```

---

### 任务 10：更新初始化配置、部署脚本和紧急通知

**涉及文件：**

- 修改：`moox.toml.example`
- 修改：其他 moox.toml 模板和生成器
- 修改：`modules/cli/internal/setup/config/config.go`
- 修改：`modules/cli/internal/setup/config/config_test.go`
- 修改：`modules/cli/internal/setup/deploy/deploy.go`
- 修改：`modules/cli/internal/setup/deploy/deploy_test.go`
- 修改：`scripts/deploy/deploy-moox.sh`
- 修改：相关 Shell 契约测试
- 修改：`modules/admin/internal/bootstrap/certificate_watch.go`
- 修改：`modules/admin/internal/bootstrap/certificate_watch_test.go`

- [ ] **步骤 1：先写通用初始化配置失败测试**

覆盖：

- 默认类型为 `wecom`；
- `wecom` 和企业微信 URL 合法；
- `feishu` 和飞书 URL 合法；
- 未知类型失败；
- HTTP URL 失败；
- 类型与 URL 不匹配失败；
- 空 URL 合法且不关闭监控。

- [ ] **步骤 2：替换 moox.toml 配置**

```toml
[notification]
channel_type = "wecom"
webhook_url = ""
```

删除 `[monitoring].wecom_webhook` 和对应 Go 字段。

- [ ] **步骤 3：替换部署环境变量**

部署生成的 `secrets/notification.env`只包含：

```text
MOOX_NOTIFICATION_CHANNEL_TYPE=wecom
MOOX_NOTIFICATION_WEBHOOK_URL=...
```

文件权限必须为 `0600`。脚本不得把完整 URL 输出到 stdout、部署日志或错误信息。

- [ ] **步骤 4：Admin 证书检查使用通用发送工厂**

Admin 继续从部署环境读取类型和 URL，但通过 `notification.NewSender`创建发送器。该路径用于 Monitor 故障时的紧急通知，不读取 Monitor SQLite，也不承诺跟随页面动态变更。

- [ ] **步骤 5：更新 CLI 和部署契约**

契约必须确认：

- 旧环境变量不再生成；
- 新文件权限为 `0600`；
- Monitor 和 Admin 都收到通用通知配置；
- 空 URL 时两个服务均能启动；
- 脚本输出不包含 Webhook 密钥。

- [ ] **步骤 6：运行 CLI、Admin 和脚本测试**

```bash
cd modules/cli
go test ./internal/setup/... -count=1
cd ../admin
go test ./internal/bootstrap -run 'Certificate|Notification' -count=1
cd ../..
make test-script-contracts
```

预期结果：全部通过，旧配置键和旧环境变量无生产匹配。

- [ ] **步骤 7：提交初始化和部署改造**

```bash
git add examples modules/cli modules/admin scripts skills/moox/references/custom-setup.md
git commit -m "refactor: configure generic notification channel"
```

---

### 任务 11：更新文档并执行绿色项目数据重置

**涉及文件：**

- 修改：`modules/monitor/README.md`
- 修改：`docs/监控配置.md`
- 修改：`docs/运维/MooX指标监控.md`
- 修改：`docs/架构总览.md`
- 修改：`skills/moox/references/custom-setup.md`
- 修改：相关部署/重置脚本和契约测试

- [ ] **步骤 1：重写运维说明**

文档明确说明：

- 健康项和告警规则由 MooX 自动生成；
- 用户不能新增探测或指标规则；
- 全局通知通道支持企业微信或飞书；
- 当前一次只允许一个 `global` 通道；
- URL 为空只停止外部通知；
- 初始化配置仅首次生效；
- 页面修改只对 Monitor 管理的告警即时生效；
- Admin 紧急通知继续使用部署配置；
- 原始 Go、进程和请求指标不再发送或存储。

- [ ] **步骤 2：制定正式环境绿色重置步骤**

首次发布最终 Schema 时执行：

1. 停止 Monitor；
2. 备份 Monitor SQLite，作为回滚证据；
3. 只删除 Monitor SQLite，不删除 Storage、Collector、Factor、Trade 或 EventBus 数据；
4. 启动 Monitor，由 SysDeploy、业务新鲜度、Market Canary 和 HostAgent 重新生成系统事实；
5. 在健康页面配置全局通知通道；
6. 使用现有有界维护命令清理旧 `dataset_mooxsys_service_metrics` 目录和历史；
7. 验证新写入只包含业务健康指标。

脚本必须打印将要处理的目录，并拒绝操作配置的 Monitor 数据目录之外的路径。

- [ ] **步骤 3：运行文档和脚本契约**

```bash
make test-docs-architecture
make test-script-contracts
git diff --check
```

预期结果：全部通过，不再出现旧用户操作说明。

- [ ] **步骤 4：提交文档和重置契约**

```bash
git add modules/monitor/README.md docs skills scripts
git commit -m "docs: document health and notification model"
```

---

### 任务 12：完整验证、视觉验收和正式环境验证

**涉及文件：**

- 默认不新增生产文件；仅修复验证过程中发现的回归。

- [ ] **步骤 1：运行共享通知包和 Monitor race 测试**

```bash
cd packages/notification
go test -race ./... -count=1
cd ../../modules/monitor
go test -race ./... -count=1
```

预期结果：全通过，不存在通道动态切换、告警状态或定时任务数据竞争。

- [ ] **步骤 2：运行多模块工作区验证**

```bash
cd ../..
./scripts/test/contract/test-go-workspace.sh
make test-web
make verify-pr
```

MooX 是多模块仓库，不能用仓库根目录 `go test ./...` 代替工作区验证。

- [ ] **步骤 3：执行退役能力搜索**

```bash
rg -n "CreateCheck|UpdateCheck|DeleteCheck|RunCheckOnce|CreateWebhookChannel|CreateAlertRule|MetricRule|metric_rule.timer|CheckSourceManual" modules/monitor web/src web/scripts
rg -n "packages/msgbox|MOOX_MSGBOX_WECOM_WEBHOOK|monitoring\.wecom_webhook" --glob '!docs/superpowers/plans/**'
rg -n "新增探测|编辑探测|新增告警规则|可用性监控|应用指标|原始指标名|序列 ID" web/src web/scripts
```

预期结果：生产代码无匹配；测试只能在明确的禁止能力断言中提及退役符号。

- [ ] **步骤 4：执行桌面和移动端视觉验收**

启动 Web 开发服务，打开 `/ops/services?tab=health`，使用 Playwright 截取：

```text
1440x900
1024x768
390x844
```

验证：

- 长中文异常原因正常换行；
- 状态、结论和设置按钮不重叠；
- 页面没有卡片套卡片；
- 通知弹窗适配手机屏幕；
- 页面不显示完整 Webhook URL；
- 空状态、加载状态、部分失败、正常、警告和告警状态容易区分；
- 详情抽屉只显示结构化中文字段，不显示 JSON。

- [ ] **步骤 5：验证企业微信和飞书发送**

分别使用受控测试机器人：

1. 选择企业微信并保存 URL；
2. 触发一次受控系统检查失败，确认 firing 事件和企业微信消息；
3. 恢复检查，确认 resolved 事件和恢复消息；
4. 切换为飞书，无需重启 Monitor；
5. 再次触发和恢复，确认消息只发送到飞书；
6. 清空 URL，确认事件继续生成但不发送消息。

- [ ] **步骤 6：验证正式环境健康链路**

确认：

- Gateway 可以调用 `GetHealthOverview`；
- 页面只展示启用中的系统项；
- 通知通道在 Monitor 重启后保持用户配置；
- Reporter 服务在线状态正常；
- K 线新鲜度、Factor 时序差、余额同步和 SCF Timer 容量正常；
- 主机指标和 Doctor Context 正常；
- 最新指标目录不存在 `go_*`、`process_*`、tRPC 或 HTTP 请求序列；
- 恢复后至少观察两个完整检查周期。

- [ ] **步骤 7：执行独立代码审查**

按照仓库要求使用 `codeCR` subAgent，重点审查：

- 通知 URL 泄露；
- 企业微信/飞书响应处理；
- SSRF 和重定向；
- 全局单行约束；
- 空 URL 行为；
- 告警事件与发送失败的事务顺序；
- 动态切换通道的并发风险；
- 删除 RPC 后的网关路由；
- 指标白名单遗漏；
- 前端定时刷新和弹窗状态竞争。

接受的意见修复后，重新运行受影响测试和 `git diff --check`。

- [ ] **步骤 8：记录最终证据**

记录：

- 测试提交 SHA；
- 完整命令和结果；
- 正式环境发布时间；
- Monitor 进程启动时间；
- Gateway RPC 返回；
- 三种视口截图路径；
- 企业微信和飞书受控发送结果；
- 清空 URL 后只记录不发送的证据；
- 技术指标负向搜索结果。

本地测试、已部署二进制身份和正式环境验收必须分开记录，不能相互替代。

---

## 六、自检清单

- [ ] 全文已经使用中文描述，只有代码符号、文件路径和命令保留英文。
- [ ] `packages/msgbox`明确更名为`packages/notification`。
- [ ] 企业微信和飞书均有独立发送器、工厂分发和测试。
- [ ] 数据表统一为`t_monitor_notification_channels`。
- [ ] 数据库只允许`channel_id=global`一行。
- [ ] `channel_type`只允许`wecom`或`feishu`。
- [ ] URL 为空不会禁用监控、规则、状态或事件。
- [ ] Monitor 管理的三条通知路径每次发送都读取最新通道。
- [ ] Admin 紧急通知的部署配置边界已明确。
- [ ] API 和页面都不会回显完整 Webhook 密钥。
- [ ] 用户自定义探测、原始指标和自定义告警规则的前后端能力全部删除。
- [ ] 健康视图由服务端聚合、有界、中文化且不含原始 JSON。
- [ ] 当前告警只来自 firing 状态，不受历史失败结果影响。
- [ ] Reporter 和 Monitor 两层指标过滤都存在。
- [ ] Dataset、Factor、余额、SCF 容量和 Doctor 所需指标均有白名单测试。
- [ ] 正式环境重置只影响 Monitor 控制面和旧服务指标数据。
- [ ] 最终包含 race 测试、工作区验证、视觉验收、企业微信/飞书实发和独立 `codeCR`。
