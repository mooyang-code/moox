package delist

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/log"
)

// 需要人看的情况。Monitor 按 kind 生成检查，detail 是来源和原因。
const (
	// AttentionSourceFailed 是某个来源连续失败了 SourceFailureThreshold 次。
	AttentionSourceFailed = "delist_source_failed"
	// AttentionReadonlyKeyMissing 是读不到币安只读 Key，币安现货来源本轮跳过。
	AttentionReadonlyKeyMissing = "delist_readonly_key_missing"
	// SourceFailureThreshold 是来源连续失败多少次才算关注项。
	SourceFailureThreshold = 3
)

var attentionKinds = []string{AttentionSourceFailed, AttentionReadonlyKeyMissing}

// maxAttentionDetail 是标签的字节上限：Monitor 超过 512 字节会拒收整份上报。
const maxAttentionDetail = 480

// Attention 上报 moox_collector_attention_active：每种关注项一个值，1 表示当前有需要处理的情况，detail 里列出来源和原因。
type Attention struct {
	gauge *prometheus.GaugeVec
}

// NewAttention 注册关注项指标。
func NewAttention(registerer prometheus.Registerer) *Attention {
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_attention_active", Help: "Collector conditions that need a person, with source and reason in the detail label."}, []string{"kind", "detail"})
	if registerer != nil {
		if err := registerer.Register(gauge); err != nil {
			var already prometheus.AlreadyRegisteredError
			if errors.As(err, &already) {
				gauge = already.ExistingCollector.(*prometheus.GaugeVec)
			}
		}
	}
	return &Attention{gauge: gauge}
}

// Publish 用本轮结果替换上报值，items 按 kind 列出每条详情；恢复后值回到 0，Monitor 才看得到恢复。
func (a *Attention) Publish(items map[string][]string) {
	a.gauge.Reset()
	for _, kind := range attentionKinds {
		details := append([]string(nil), items[kind]...)
		if len(details) == 0 {
			a.gauge.WithLabelValues(kind, "").Set(0)
			continue
		}
		sort.Strings(details)
		detail := strings.Join(details, "；")
		if len(detail) > maxAttentionDetail {
			detail = strings.ToValidUTF8(detail[:maxAttentionDetail], "") + "…"
		}
		a.gauge.WithLabelValues(kind, detail).Set(1)
	}
}

// Detector 定时轮询各个来源，把新发现的下架通知登记（并发布）。
type Detector struct {
	Sources   []Source
	Registrar *Registrar
	Attention *Attention
	// Interval 是两轮之间的间隔，默认 10 分钟。
	Interval time.Duration
	// FetchTimeout 是单个来源一次请求的时限，默认 30 秒。
	FetchTimeout time.Duration
	Now          func() time.Time

	mu       sync.Mutex
	failures map[string]int
	lastErr  map[string]string
	keyGone  map[string]bool
}

// Run 一直轮询到 ctx 结束。第一轮在启动后马上进行。
func (d *Detector) Run(ctx context.Context) error {
	interval := d.Interval
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		d.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunOnce 轮询一遍所有来源。一个来源失败不影响别的来源。
func (d *Detector) RunOnce(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failures == nil {
		d.failures, d.lastErr, d.keyGone = map[string]int{}, map[string]string{}, map[string]bool{}
	}
	timeout := d.FetchTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	for _, source := range d.Sources {
		if ctx.Err() != nil {
			return
		}
		fetchCtx, cancel := context.WithTimeout(ctx, timeout)
		notices, err := source.Fetch(fetchCtx)
		cancel()
		name := source.Name()
		switch {
		case errors.Is(err, ErrReadonlyKeyMissing):
			// 缺 Key 不算来源故障：这一轮跳过，记一条关注项，下一轮继续尝试，补上 Key 之后自动恢复。
			d.keyGone[name] = true
			delete(d.failures, name)
			log.WarnContextf(ctx, "[Collector] 下架检测来源 %s 缺少只读 Key，本轮跳过", name)
			continue
		case err != nil:
			d.keyGone[name] = false
			d.failures[name]++
			d.lastErr[name] = err.Error()
			log.WarnContextf(ctx, "[Collector] 下架检测来源 %s 失败（连续 %d 次）: %v", name, d.failures[name], err)
			continue
		}
		d.keyGone[name] = false
		d.failures[name] = 0
		for _, notice := range notices {
			created, eventID, err := d.Registrar.Register(ctx, notice)
			if err != nil {
				log.ErrorContextf(ctx, "[Collector] 登记下架通知 %s:%s:%s 失败: %v", notice.Exchange, notice.Market, notice.Symbol, err)
				continue
			}
			if created {
				log.InfoContextf(ctx, "[Collector] 发现标的即将下架 %s:%s:%s 下架时间 %s 来源 %s 事件 %s", notice.Exchange, notice.Market, notice.Symbol, notice.DelistAt.Format(time.RFC3339), name, eventID)
			}
		}
	}
	d.publishLocked()
}

func (d *Detector) publishLocked() {
	if d.Attention == nil {
		return
	}
	items := map[string][]string{}
	for name, count := range d.failures {
		if count >= SourceFailureThreshold {
			items[AttentionSourceFailed] = append(items[AttentionSourceFailed], fmt.Sprintf("%s 连续 %d 次失败：%s", name, count, stable(d.lastErr[name])))
		}
	}
	for name, missing := range d.keyGone {
		if missing {
			items[AttentionReadonlyKeyMissing] = append(items[AttentionReadonlyKeyMissing], name+"：没有读到只读 Key（exchange-binance-production-readonly），用 setup apply 写入")
		}
	}
	d.Attention.Publish(items)
}

var urlPattern = regexp.MustCompile(`"?https?://[^\s"]+"?:?`)

// stable 截短错误文本：错误原文可能带地址和时间，每次都不同，会让指标序列不断增长。
func stable(reason string) string {
	reason = urlPattern.ReplaceAllString(reason, "")
	if runes := []rune(reason); len(runes) > 60 {
		return string(runes[:60])
	}
	return reason
}
