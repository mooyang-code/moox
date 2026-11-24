// Package attention 声明 Collector 需要人看的情况（关注项）：指标 moox_attention_active，由 report.Attention 上报。
// 下架检测、事件发件箱等多个来源各自报告自己的那部分，report.Attention 合并后一起上报。
package attention

import (
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

// 需要人看的情况。Monitor 按 kind 生成检查 attention:collector:<kind>，名称与级别在 Kinds 里，detail 是来源和原因。
const (
	// KindSourceFailed 是某个下架来源连续失败了几次。
	KindSourceFailed = "delist_source_failed"
	// KindReadonlyKeyMissing 是读不到币安只读 Key，币安现货来源本轮跳过。
	KindReadonlyKeyMissing = "delist_readonly_key_missing"
	// KindOutboxStuck 是事件发件箱里的事件积压过久没发布出去，或有无效事件被搁置。
	KindOutboxStuck = "event_outbox_stuck"
	// KindConsumerFailed 是连上 EventBus 之后的初始化（例如 K 线合成的消费者）连续失败，一直没有成功。
	KindConsumerFailed = "eventbus_consumer_failed"
	// KindEastMoneyCooling 是标的列表同步用到的东方财富主机（push2 与 push2test）都被限流、Collector 已主动冷却，同步暂缓；
	// push2 单独冷却时回退 push2test，同步照常，不上报。任一主机探测成功即恢复；冷却结束后若一直没有新请求，值会保持 1，直到下一次探测成功。
	KindEastMoneyCooling = "eastmoney_cooling_down"
)

// Kinds 是 Collector 声明的全部关注项。warning 只需提醒、不算故障：缺币安只读 Key 时只是现货下架改为手工登记；
// 东方财富冷却时只是标的列表同步暂缓（云函数采集不受影响），冷却结束探测成功后自动恢复。
var Kinds = []report.AttentionKind{
	{Kind: KindSourceFailed, Name: "下架检测来源连续失败", Severity: report.AttentionCritical},
	{Kind: KindReadonlyKeyMissing, Name: "下架检测缺少币安只读 Key", Severity: report.AttentionWarning},
	{Kind: KindOutboxStuck, Name: "Collector 事件发件箱积压", Severity: report.AttentionCritical},
	{Kind: KindConsumerFailed, Name: "Collector EventBus 消费者启动失败", Severity: report.AttentionCritical},
	{Kind: KindEastMoneyCooling, Name: "东方财富请求被限流，Collector 已暂停访问", Severity: report.AttentionWarning},
}

// Attention 是 Collector 的关注项上报器。
type Attention = report.Attention

// New 注册关注项指标；同一个注册器上已注册过时返回已有的那份。
func New(registerer prometheus.Registerer) *Attention {
	attention, err := report.NewAttention(registerer, Kinds)
	if err != nil {
		// 只有声明本身写错才会失败，属于编程错误。
		panic(err)
	}
	return attention
}

// Value 返回某个种类、某条 detail 当前的值（测试用）：正在上报这条 detail 时为 1，否则为 0。
func Value(a *Attention, kind, detail string) float64 {
	if detail != "" && a.Active()[kind] == detail {
		return 1
	}
	return 0
}
