package events

import (
	"slices"
	"time"
)

// 本文件是 EventBus 拓扑的唯一声明：有哪些 Stream、每个角色（EventBus 账号）发布哪些事件、用哪些持久消费者消费。
// EventBus 按 Streams() 建 Stream，Admin 按 Roles 生成账号与权限，各模块按这里的消费者声明取 durable 名，
// 三处不再各记一份。

// Stream 名。
const (
	StreamStorage       = "MOOX_STORAGE"
	StreamObservability = "MOOX_OBSERVABILITY"
	StreamTrade         = "MOOX_TRADE"
	StreamCollector     = "MOOX_COLLECTOR"
)

// StreamSpec 是一个 Stream 的参数。存储一律是文件、满了丢最旧的、去重窗口 2 分钟，这几项不再逐个声明。
type StreamSpec struct {
	Name string
	// WorkQueue 为真时消息被确认后即删除（只能有一个消费者）；否则按 MaxAge、MaxBytes 保留。
	WorkQueue   bool
	MaxAge      time.Duration
	MaxBytes    int64
	Description string
	// Subjects 由登记在这个 Stream 下的事件推出（每个事件一个 moox.<事件名>.v<版本>.> 主题族）。
	Subjects []string
}

// StreamDuplicates 是所有 Stream 的去重窗口。
const StreamDuplicates = 2 * time.Minute

var streamSpecs = []StreamSpec{
	{
		Name: StreamStorage, MaxAge: 72 * time.Hour, MaxBytes: 2 << 30,
		// 只有周期完成与 View 就绪两种事件：每条带标的全集，几 KB 到十几 KB（发布时压缩），
		// 按每天几万条估，72 小时在几百 MB 量级；上限 2 GiB 留足余量。
		Description: "Storage 的周期完成与 View 就绪事件",
	},
	{Name: StreamObservability, MaxAge: 24 * time.Hour, MaxBytes: 128 << 20, Description: "各服务的指标快照与重要日志"},
	// 自动交易只消费按会话限定的目标权重；工作队列保证一条目标只被 Trade 处理一次。
	{Name: StreamTrade, WorkQueue: true, MaxAge: 168 * time.Hour, MaxBytes: 128 << 20, Description: "策略发给 Trade 的目标权重"},
	// Collector 发布的元数据事件（不带行情数据）：K 线于周期结束后被补写、标的即将下架。
	{Name: StreamCollector, MaxAge: 72 * time.Hour, MaxBytes: 128 << 20, Description: "Collector 的 K 线补写与标的下架事件"},
}

// Streams 返回全部 Stream 声明，主题已按事件推出。
func Streams() []StreamSpec {
	out := make([]StreamSpec, 0, len(streamSpecs))
	for _, spec := range streamSpecs {
		spec.Subjects = nil
		for _, event := range builtInEvents {
			if event.Stream() == spec.Name {
				spec.Subjects = append(spec.Subjects, eventFamily(event))
			}
		}
		slices.Sort(spec.Subjects)
		out = append(out, spec)
	}
	return out
}

// DurableConsumer 是一个持久消费者的声明：名字、所在 Stream 与消费的事件。
type DurableConsumer struct {
	name   string
	events []Event
	// deletable 为真时消费者所属角色可以删掉它（配置冲突时删了重建）。
	deletable bool
}

func declareConsumer(name string, events ...Event) DurableConsumer {
	return DurableConsumer{name: name, events: events}
}

// Name 返回消费者名。
func (c DurableConsumer) Name() string {
	return c.name
}

func (c DurableConsumer) Deletable() bool { return c.deletable }
func (c DurableConsumer) Events() []Event { return append([]Event(nil), c.events...) }

// Stream 返回消费者所在的 Stream（声明的事件必须同属一个 Stream）。
func (c DurableConsumer) Stream() string {
	if len(c.events) == 0 {
		return ""
	}
	return c.events[0].Stream()
}

// FilterSubjects 返回消费者过滤的主题族。
func (c DurableConsumer) FilterSubjects() []string {
	out := make([]string, 0, len(c.events))
	for _, event := range c.events {
		out = append(out, eventFamily(event))
	}
	return out
}

// 各模块的持久消费者。
var (
	// MonitorObservabilityConsumer 是 Monitor 收各服务指标与重要日志的消费者；过滤条件变了 Monitor 会删掉重建。
	// 主机快照不走 EventBus：HostAgent 随拉取主机策略那次 RPC 报给 Monitor。
	MonitorObservabilityConsumer = DurableConsumer{
		name:      "monitor_observability_ingest_v1",
		events:    []Event{ObservabilityLogReported, ObservabilityMetricsSnapshotReported},
		deletable: true,
	}
	FactorPeriodConsumer      = declareConsumer("factor_collector_period_v1", DatasetPeriodCompleted)
	FactorBackfilledConsumer  = declareConsumer("factor_kline_backfilled_v1", CollectorKlineBackfilled)
	StrategyViewReadyConsumer = declareConsumer("strategy_view_data_ready_v1", ViewDataReady)
	TradeTargetConsumer       = declareConsumer("trade_target_weight_v1", LogicalAccountTargetWeightRequested)
	// TradeDelistConsumer 是 Trade 收 Collector 下架事件的消费者（在 MOOX_COLLECTOR 上）。
	TradeDelistConsumer = declareConsumer("trade_instrument_delist_v1", InstrumentDelistScheduled)
	// Collector 的 K 线合成跨空间消费源周期完成与补采完成事件，领域层按信封里的 space_id 匹配任务。
	CollectorResamplePeriodConsumer     = declareConsumer("collector_resample_period_v1", DatasetPeriodCompleted)
	CollectorResampleBackfilledConsumer = declareConsumer("collector_resample_backfilled_v1", CollectorKlineBackfilled)
)

// Role 是一个 EventBus 账号。
type Role struct {
	// Username 是 NATS 用户名。
	Username string
	// File 是凭据文件名（不带 .yaml）；服务目录里写作 eventbus:<File>。
	File string
	// SecretKey 是令牌在 Admin 秘钥表里的 key_id。
	SecretKey string
	// Admin 为真时拥有全部 JetStream 接口，只给 EventBus 自己建 Stream 用。
	Admin bool
	// Publishes 是可以发布的事件。
	Publishes []Event
	// Consumers 是可以创建与消费的持久消费者。
	Consumers []DurableConsumer
}

// Roles 是全部 EventBus 账号。
var Roles = []Role{
	{Username: "eventbus-internal-admin", File: "internal-admin", SecretKey: "eventbus_internal_admin", Admin: true},
	// 各服务的指标与重要日志共用一个账号（除 host-agent 与第三方程序外每个组件都隐含它）。
	{Username: "metrics-publisher", File: "metrics-publisher", SecretKey: "eventbus_metrics_publisher", Publishes: []Event{ObservabilityMetricsSnapshotReported, ObservabilityLogReported}},
	{Username: "monitor-observability-consumer", File: "monitor-observability", SecretKey: "eventbus_monitor_observability_consumer", Consumers: []DurableConsumer{MonitorObservabilityConsumer}},
	{Username: "storage-eventbus", File: "storage-eventbus", SecretKey: "eventbus_storage", Publishes: []Event{DatasetPeriodCompleted, ViewDataReady}},
	{Username: "factor-eventbus", File: "factor-eventbus", SecretKey: "eventbus_factor", Consumers: []DurableConsumer{FactorPeriodConsumer, FactorBackfilledConsumer}},
	{Username: "strategy-eventbus", File: "strategy-eventbus", SecretKey: "eventbus_strategy", Publishes: []Event{LogicalAccountTargetWeightRequested}, Consumers: []DurableConsumer{StrategyViewReadyConsumer}},
	{Username: "trade-eventbus", File: "trade-eventbus", SecretKey: "eventbus_trade", Consumers: []DurableConsumer{TradeTargetConsumer, TradeDelistConsumer}},
	{Username: "collector-eventbus", File: "collector-eventbus", SecretKey: "eventbus_collector", Publishes: []Event{CollectorKlineBackfilled, InstrumentDelistScheduled}, Consumers: []DurableConsumer{CollectorResamplePeriodConsumer, CollectorResampleBackfilledConsumer}},
}

// RoleByFile 按凭据文件名（服务目录里 eventbus:<File> 的 <File>）找账号。
func RoleByFile(file string) (Role, bool) {
	for _, role := range Roles {
		if role.File == file {
			return role, true
		}
	}
	return Role{}, false
}

// RoleByUsername 按 NATS 用户名找账号。
func RoleByUsername(username string) (Role, bool) {
	for _, role := range Roles {
		if role.Username == username {
			return role, true
		}
	}
	return Role{}, false
}
