// Package eastmoneylimit 把 packages/cnstock 里东方财富限流器的事件接到 Collector 的日志、指标和关注项。
//
// 限流器本身（令牌桶、并发上限、按主机的冷却）在 packages/cnstock，那里不依赖日志与指标库；
// 这里实现它的观察者：每个事件记一条日志，更新 moox_collector_eastmoney_* 指标，进入冷却时上报关注项 eastmoney_cooling_down。
package eastmoneylimit

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/mooyang-code/moox/modules/collector/internal/attention"
	"github.com/mooyang-code/moox/packages/cnstock"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/log"
)

// knownHosts 是指标 host 标签允许的取值；其他主机一律记为 other，标签取值不会无限增长。
var knownHosts = []string{"push2.eastmoney.com", "push2his.eastmoney.com", "push2test.eastmoney.com"}

const otherHost = "other"

// attentionSource 是关注项合并时的来源名。
const attentionSource = "eastmoney_limit"

// Observer 实现 cnstock.LimitObserver：日志、指标与关注项。
type Observer struct {
	requests  *prometheus.CounterVec
	rejected  *prometheus.CounterVec
	cooldowns *prometheus.CounterVec
	wait      *prometheus.HistogramVec
	cooling   *prometheus.GaugeVec
	attention *attention.Attention

	mu      sync.Mutex
	coolant map[string]string // 冷却中的主机 → 关注项详情
}

// New 注册指标并创建观察者；registerer 为空时只记日志。attn 为空时不上报关注项。
func New(registerer prometheus.Registerer, attn *attention.Attention) *Observer {
	o := &Observer{
		requests:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_eastmoney_requests_total", Help: "Requests to eastmoney.com hosts that passed the process-level limiter."}, []string{"host"}),
		rejected:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_eastmoney_rejected_total", Help: "Requests to eastmoney.com hosts refused without being sent because the host is cooling down."}, []string{"host"}),
		cooldowns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_eastmoney_cooldowns_total", Help: "Times an eastmoney.com host entered a cooldown."}, []string{"host"}),
		wait: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "moox_collector_eastmoney_wait_seconds", Help: "Time a request waited in the eastmoney.com limiter (concurrency slot and token bucket).",
			Buckets: []float64{0.001, 0.01, 0.1, 0.5, 1, 2, 5, 10, 30, 60},
		}, []string{"host"}),
		cooling:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_eastmoney_cooling", Help: "Whether an eastmoney.com host is cooling down (1) or normal (0); stays 1 until a probe request succeeds."}, []string{"host"}),
		attention: attn,
		coolant:   map[string]string{},
	}
	if registerer != nil {
		o.requests = registerOrReuse(registerer, o.requests)
		o.rejected = registerOrReuse(registerer, o.rejected)
		o.cooldowns = registerOrReuse(registerer, o.cooldowns)
		o.wait = registerOrReuse(registerer, o.wait)
		o.cooling = registerOrReuse(registerer, o.cooling)
	}
	for _, host := range knownHosts {
		o.cooling.WithLabelValues(host).Set(0)
	}
	return o
}

func registerOrReuse[C prometheus.Collector](registerer prometheus.Registerer, collector C) C {
	if err := registerer.Register(collector); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			if existing, ok := already.ExistingCollector.(C); ok {
				return existing
			}
		}
	}
	return collector
}

func hostLabel(host string) string {
	for _, known := range knownHosts {
		if host == known {
			return host
		}
	}
	return otherHost
}

var reasonText = map[string]string{
	cnstock.ReasonTransportFailure: "连续传输层失败",
	cnstock.ReasonRateLimited:      "HTTP 429",
	cnstock.ReasonForbidden:        "HTTP 403",
}

// Observe 处理限流器的一个事件；并发安全、不阻塞。
func (o *Observer) Observe(event cnstock.LimitEvent) {
	host := hostLabel(event.Host)
	switch event.Kind {
	case cnstock.EventSent:
		o.requests.WithLabelValues(host).Inc()
		o.wait.WithLabelValues(host).Observe(event.Wait.Seconds())
	case cnstock.EventRejected:
		o.rejected.WithLabelValues(host).Inc()
	case cnstock.EventCooldownStarted:
		o.cooldowns.WithLabelValues(host).Inc()
		o.cooling.WithLabelValues(host).Set(1)
		reason := reasonText[event.Reason]
		if reason == "" {
			reason = event.Reason
		}
		log.Warnf("东方财富限流：%s 进入冷却 %s（第 %d 次，原因：%s，连续失败 %d 次）；冷却期间不发请求，结束后放一个探测请求", event.Host, event.Cooldown, event.Level, reason, event.Failures)
		o.setCooling(event.Host, fmt.Sprintf("%s 冷却 %s（第 %d 次，%s）", event.Host, event.Cooldown, event.Level, reason))
	case cnstock.EventProbe:
		log.Infof("东方财富限流：%s 冷却结束，放出一个探测请求（冷却第 %d 次）", event.Host, event.Level)
	case cnstock.EventRecovered:
		o.cooling.WithLabelValues(host).Set(0)
		log.Infof("东方财富限流：%s 探测成功，已恢复", event.Host)
		o.setCooling(event.Host, "")
	}
}

// setCooling 记录主机的冷却状态并重新上报关注项；detail 为空表示恢复。
func (o *Observer) setCooling(host, detail string) {
	if o.attention == nil {
		return
	}
	o.mu.Lock()
	if detail == "" {
		delete(o.coolant, host)
	} else {
		o.coolant[host] = detail
	}
	details := make([]string, 0, len(o.coolant))
	for _, text := range o.coolant {
		details = append(details, text)
	}
	sort.Strings(details)
	o.attention.Set(attentionSource, map[string][]string{attention.KindEastMoneyCooling: details})
	o.mu.Unlock()
}
