// Package catalog 是事件中心可以配置规则的事件目录：界面名、可限定的范围，以及怎样从信封里取出范围的值。
// 目录只放在本服务里，不改事件注册表；开放新事件只需要在这里加一项，并同步 events.CallbackDispatcherEvents。
package catalog

import (
	"strings"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/nats-io/nats.go"
)

// 范围字段名。
const (
	FieldSpaceID        = "space_id"
	FieldSubject        = "subject"
	FieldExchange       = "exchange"
	FieldMarket         = "market"
	FieldExchangeSymbol = "exchange_symbol"
)

// Field 是一个可以限定的字段。
type Field struct {
	Key   string
	Title string
	// Options 是建议的取值；为空表示自由填写。
	Options []string
}

// Entry 是目录里的一个事件。
type Entry struct {
	Event       events.Event
	Title       string
	Description string
	Fields      []Field
	// DeliverPolicy 是第一次创建 durable 时的起点：下架事件要能收到 Collector 已经发出的，其余不回放历史。
	DeliverPolicy nats.DeliverPolicy
	// Sample 是“发一条测试”用的示例 payload（JSON 对象），是一条典型事件，写法和真实推送一致（protojson、proto 字段名）：
	// int64 写成字符串、时刻写成 RFC3339，值为空的字段不出现。
	Sample map[string]any
	// PayloadFields 是 payload 全部字段的说明（覆盖 proto 里的每个字段，内部字段注明可以忽略）。
	PayloadFields []Field
}

// Name 返回事件名。
func (e Entry) Name() string { return e.Event.Name() }

// HasField 报告这个事件能否按 key 限定范围。
func (e Entry) HasField(key string) bool {
	for _, field := range e.Fields {
		if field.Key == key {
			return true
		}
	}
	return false
}

// ScopeValue 返回信封里 key 对应的值；取不到时 ok 为 false。
// 交易所、市场、代码从下架事件的 subject（交易所:市场:代码）里拆出来。
func (e Entry) ScopeValue(message *eventpb.EventMessage, key string) (string, bool) {
	switch key {
	case FieldSpaceID:
		return message.GetSpaceId(), true
	case FieldSubject:
		return message.GetSubjectId(), true
	case FieldExchange, FieldMarket, FieldExchangeSymbol:
		parts := strings.SplitN(message.GetSubjectId(), ":", 3)
		if len(parts) != 3 {
			return "", false
		}
		return map[string]string{FieldExchange: parts[0], FieldMarket: parts[1], FieldExchangeSymbol: parts[2]}[key], true
	}
	return "", false
}

var spaceField = Field{Key: FieldSpaceID, Title: "空间"}

// samplePosition 是示例里的写入位置（uint64 按 protojson 写成字符串）。
var samplePosition = map[string]any{"node_id": "node-1", "store_id": "store-1", "sequence": "128"}

var entries = []Entry{
	{
		Event: events.DatasetPeriodCompleted, Title: "数据集一期完成", Description: "数据集的某一个周期数据已经写完整",
		Fields:        []Field{spaceField, {Key: FieldSubject, Title: "数据集"}},
		DeliverPolicy: nats.DeliverNewPolicy,
		Sample: map[string]any{
			"dataset_id": "示例数据集", "frequency": "5m", "period_time": "1786032000", "status": "complete", "source": "producer",
			"universe_subject_ids": []string{"BTCUSDT", "ETHUSDT"}, "committed_positions": []map[string]any{samplePosition},
			"cause_event_id": "示例事件", "completed_at": "2026-08-06T00:05:03Z",
		},
		PayloadFields: []Field{
			{Key: "dataset_id", Title: "数据集 ID"},
			{Key: "frequency", Title: "周期，例如 1m、5m、1h"},
			{Key: "period_time", Title: "这一期的开始时刻，Unix 秒（字符串）"},
			{Key: "status", Title: "complete 表示全部标的写完；degraded 表示有标的失败，失败的在 failed_subjects"},
			{Key: "source", Title: "怎样判定完成：ledger 按账本判定，producer 由写入方报告"},
			{Key: "universe_subject_ids", Title: "这一期应有的全部标的"},
			{Key: "failed_subjects", Title: "这一期没写成功的标的（只在 degraded 时出现）"},
			{Key: "universe_series", Title: "按序列（标的、series_tag）的全部成员：只有统一数据集（一个数据集含多个序列）才有"},
			{Key: "failed_series", Title: "按序列的失败名单：只有统一数据集才有"},
			{Key: "completed_at", Title: "判定完成的时刻（UTC）"},
			{Key: "cause_event_id", Title: "触发这次计算的事件 ID（source 为 producer 时）"},
			{Key: "snapshot_ref", Title: "内部字段（source 为 ledger 时的快照引用），可以忽略"},
			{Key: "committed_positions", Title: "内部字段（写入位置），可以忽略"},
			{Key: "detail_type", Title: "写入方自定义的明细类型，一般用不到"},
			{Key: "detail", Title: "写入方自定义的明细内容（base64），一般用不到"},
		},
	},
	{
		Event: events.ViewDataReady, Title: "View 数据就绪", Description: "View 已经收齐某一个周期的数据，可以读取",
		Fields:        []Field{spaceField, {Key: FieldSubject, Title: "View"}},
		DeliverPolicy: nats.DeliverNewPolicy,
		Sample: map[string]any{
			"view_id": "示例 View", "view_config_id": "示例配置", "completion_event_id": "示例事件", "dataset_id": "示例数据集",
			"status": "complete", "frequency": "5m", "period_time": "1786032000", "committed_positions": []map[string]any{samplePosition},
			"ready_at": "2026-08-06T00:05:04Z", "universe_subject_ids": []string{"BTCUSDT", "ETHUSDT"},
		},
		PayloadFields: []Field{
			{Key: "view_id", Title: "View ID"},
			{Key: "dataset_id", Title: "View 读取的数据集 ID"},
			{Key: "frequency", Title: "周期，例如 1m、5m、1h"},
			{Key: "period_time", Title: "这一期的开始时刻，Unix 秒（字符串）"},
			{Key: "status", Title: "complete 表示全部标的就绪；degraded 表示有标的缺失"},
			{Key: "universe_subject_ids", Title: "这一期应有的全部标的"},
			{Key: "ready_at", Title: "View 收齐数据的时刻（UTC）"},
			{Key: "completion_event_id", Title: "对应的一期完成事件的 ID"},
			{Key: "view_config_id", Title: "内部字段，可以忽略"},
			{Key: "visible_scope", Title: "内部字段，可以忽略"},
			{Key: "failed_scope_ref", Title: "内部字段，可以忽略"},
			{Key: "committed_positions", Title: "内部字段（写入位置），可以忽略"},
			{Key: "detail_type", Title: "原样透传一期完成事件里的明细类型，一般用不到"},
			{Key: "detail", Title: "原样透传一期完成事件里的明细内容（base64），一般用不到"},
		},
	},
	{
		Event: events.CollectorKlineBackfilled, Title: "K 线补写", Description: "已经结束的周期里补写了 K 线",
		Fields:        []Field{spaceField, {Key: FieldSubject, Title: "数据集"}},
		DeliverPolicy: nats.DeliverNewPolicy,
		Sample: map[string]any{
			"dataset_id": "示例数据集", "frequency": "5m", "subject_ids": []string{"BTCUSDT"}, "start": "2026-08-06T00:00:00Z",
			"end": "2026-08-06T01:00:00Z", "origin": "auto", "job_id": "示例作业", "sync_request_id": "示例同步点",
			"confirmed_at": "2026-08-06T02:00:00Z",
		},
		PayloadFields: []Field{
			{Key: "dataset_id", Title: "数据集 ID"},
			{Key: "frequency", Title: "周期，例如 1m、5m、1h"},
			{Key: "subject_ids", Title: "补写了的标的"},
			{Key: "start", Title: "补写范围的开始时刻（含，UTC）"},
			{Key: "end", Title: "补写范围的结束时刻（不含，UTC）"},
			{Key: "origin", Title: "补写来源：auto 自动补采，manual 手工补采，force 强制补采，resample 历史合成"},
			{Key: "job_id", Title: "补采或合成作业的 ID"},
			{Key: "confirmed_at", Title: "读回确认写入的时刻（UTC）"},
			{Key: "series_tags", Title: "补写的序列（series_tag）；不出现表示数据集的全部序列"},
			{Key: "sync_request_id", Title: "内部字段，可以忽略"},
		},
	},
	{
		Event: events.InstrumentDelistScheduled, Title: "标的即将下架", Description: "交易所公布了某个标的的下架时间，或者手工登记了下架",
		Fields: []Field{
			{Key: FieldExchange, Title: "交易所", Options: []string{"binance", "okx"}},
			{Key: FieldMarket, Title: "市场", Options: []string{events.DelistMarketSpot, events.DelistMarketSwap}},
			{Key: FieldExchangeSymbol, Title: "交易所代码"},
		},
		// 流只保留 72 小时：第一次上线时能收到 Collector 已经发出的下架；回放也无害，同一事件会被去重。
		DeliverPolicy: nats.DeliverAllPolicy,
		Sample: map[string]any{
			"exchange": "binance", "market": events.DelistMarketSpot, "exchange_symbol": "FOOUSDT",
			"delist_at": "2026-11-11T08:00:00Z", "source": events.DelistSourceManual, "first_seen_at": "2026-11-04T08:00:00Z", "scope": events.DelistScopeAll,
		},
		PayloadFields: []Field{
			{Key: "exchange", Title: "交易所：binance 或 okx"},
			{Key: "market", Title: "市场：SPOT 现货，SWAP 永续"},
			{Key: "exchange_symbol", Title: "交易所的标的代码"},
			{Key: "delist_at", Title: "下架时刻（UTC）"},
			{Key: "source", Title: "怎样发现的：MANUAL 手工登记，其余是交易所公告或接口的自动检测"},
			{Key: "first_seen_at", Title: "第一次发现的时刻（UTC）"},
			{Key: "scope", Title: "影响范围：ALL 实盘和模拟盘，PAPER_ONLY 只影响模拟盘（演练用）"},
		},
	},
}

// Subscribable 返回目录里的全部事件。
func Subscribable() []Entry {
	return append([]Entry(nil), entries...)
}

// Lookup 按事件名查目录。
func Lookup(eventName string) (Entry, bool) {
	for _, entry := range entries {
		if entry.Name() == eventName {
			return entry, true
		}
	}
	return Entry{}, false
}
