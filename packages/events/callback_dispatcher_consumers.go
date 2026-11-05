package events

import (
	"fmt"
	"strings"
)

// CallbackDispatcherEvents 是事件中心（callback-dispatcher）消费的事件，每个事件一个固定的 durable。
// 放在 events 包里，让事件中心与 EventBus 凭据生成不会各记一份而悄悄漂移；
// 不开放 MOOX_TRADE 的事件：它是工作队列，再挂消费者会和 Trade 抢消息。
var CallbackDispatcherEvents = []Event{DatasetPeriodCompleted, ViewDataReady, CollectorKlineBackfilled, InstrumentDelistScheduled}

// CallbackDispatcherConsumerName 返回事件中心消费某个事件的 durable 名：
// NATS 的 durable 名不能含点号，所以取去掉“event.”前缀的事件名，点换成下划线，前面加
// callback_dispatcher_，后面加 _v<版本>，例如 callback_dispatcher_collector_instrument_delist_scheduled_v1。
func CallbackDispatcherConsumerName(event Event) string {
	name := strings.ReplaceAll(strings.TrimPrefix(event.Name(), "event."), ".", "_")
	return fmt.Sprintf("callback_dispatcher_%s_v%d", name, event.Version())
}
