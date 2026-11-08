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
	// Sample 是“发一条测试”用的示例 payload（JSON 对象）。
	Sample map[string]any
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

var entries = []Entry{
	{
		Event: events.DatasetPeriodCompleted, Title: "数据集一期完成", Description: "数据集的某一个周期数据已经写完整",
		Fields:        []Field{spaceField, {Key: FieldSubject, Title: "数据集"}},
		DeliverPolicy: nats.DeliverNewPolicy,
		Sample:        map[string]any{"dataset_id": "示例数据集", "frequency": "5m", "period_time": 1786032000, "status": "complete", "source": "producer"},
	},
	{
		Event: events.ViewDataReady, Title: "View 数据就绪", Description: "View 已经收齐某一个周期的数据，可以读取",
		Fields:        []Field{spaceField, {Key: FieldSubject, Title: "View"}},
		DeliverPolicy: nats.DeliverNewPolicy,
		Sample:        map[string]any{"view_id": "示例 View", "frequency": "5m", "status": "complete"},
	},
	{
		Event: events.CollectorKlineBackfilled, Title: "K 线补写", Description: "已经结束的周期里补写了 K 线",
		Fields:        []Field{spaceField, {Key: FieldSubject, Title: "数据集"}},
		DeliverPolicy: nats.DeliverNewPolicy,
		Sample:        map[string]any{"dataset_id": "示例数据集", "frequency": "5m", "subject_ids": []string{"BTCUSDT"}, "origin": "auto"},
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
