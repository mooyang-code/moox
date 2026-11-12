// Package attention 上报 Collector 需要人看的情况：指标 moox_collector_attention_active{kind,detail}。
// 下架检测、事件发件箱等多个来源各自报告自己的那部分，这里合并后一起上报。
package attention

import (
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// 需要人看的情况。Monitor 按 kind 生成检查，detail 是来源和原因。
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

var kinds = []string{KindSourceFailed, KindReadonlyKeyMissing, KindOutboxStuck, KindConsumerFailed, KindEastMoneyCooling}

// maxDetail 是标签的字节上限：Monitor 超过 512 字节会拒收整份上报。
const maxDetail = 480

// Attention 合并各来源的关注项并上报：每种关注项一个值，1 表示当前有需要处理的情况，detail 里列出来源和原因。
type Attention struct {
	gauge *prometheus.GaugeVec

	mu      sync.Mutex
	sources map[string]map[string][]string
}

// New 注册关注项指标。
func New(registerer prometheus.Registerer) *Attention {
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_attention_active", Help: "Collector conditions that need a person, with source and reason in the detail label."}, []string{"kind", "detail"})
	if registerer != nil {
		if err := registerer.Register(gauge); err != nil {
			var already prometheus.AlreadyRegisteredError
			if errors.As(err, &already) {
				gauge = already.ExistingCollector.(*prometheus.GaugeVec)
			}
		}
	}
	return &Attention{gauge: gauge, sources: map[string]map[string][]string{}}
}

// Gauge 返回底层指标（测试读取用）。
func (a *Attention) Gauge() *prometheus.GaugeVec { return a.gauge }

// Set 用本轮结果替换某个来源报告的全部关注项（items 按 kind 列出每条详情），再重新上报合并后的结果；
// 恢复后值回到 0，Monitor 才看得到恢复。
func (a *Attention) Set(source string, items map[string][]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sources[source] = items
	merged := map[string][]string{}
	for _, items := range a.sources {
		for kind, details := range items {
			merged[kind] = append(merged[kind], details...)
		}
	}
	a.gauge.Reset()
	for _, kind := range kinds {
		details := append([]string(nil), merged[kind]...)
		if len(details) == 0 {
			a.gauge.WithLabelValues(kind, "").Set(0)
			continue
		}
		sort.Strings(details)
		detail := strings.Join(details, "；")
		if len(detail) > maxDetail {
			detail = strings.ToValidUTF8(detail[:maxDetail], "") + "…"
		}
		a.gauge.WithLabelValues(kind, detail).Set(1)
	}
}
