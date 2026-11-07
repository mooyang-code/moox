package delist

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/attention"
	"github.com/mooyang-code/moox/packages/events"
	"trpc.group/trpc-go/trpc-go/log"
)

// SourceFailureThreshold 是来源连续失败多少次才算关注项。
const SourceFailureThreshold = 3

// readonlyKeyMissingDetail 是缺币安只读 Key 时关注项 detail 里来源名之后的说明。读不到分三种情况（被禁用、没有、在管理台删除过），
// 关注项分不出是哪一种，一次写全，与管理台监控页的处理说明一致。detail 有字节上限（attention 包的 maxDetail），措辞要短。
const readonlyKeyMissingDetail = "：没有读到只读 Key（exchange-binance-production-readonly）。被禁用：到秘钥管理页启用；" +
	"没有：moox-cli config edit 加一把币安正式环境的只读 Key（名称 readonly），再 setup apply；" +
	"在管理台删除过：setup apply 不会重建，目前补不回来，见采集文档的已知限制"

// Detector 定时轮询各个来源，把新发现的下架通知登记（并发布）。
type Detector struct {
	Sources   []Source
	Registrar *Registrar
	Attention *attention.Attention
	// Interval 是两轮之间的间隔，默认 10 分钟。
	Interval time.Duration
	// FetchTimeout 是单个来源一次请求的时限，默认 30 秒。
	FetchTimeout time.Duration
	Now          func() time.Time

	mu       sync.Mutex
	failures map[string]int
	lastErr  map[string]string
	keyGone  map[string]bool
	// rejected 是各来源这一轮被校验拒绝的条目：拒绝等于漏报，所以按关注项报出来，下一轮不再被拒就恢复。
	rejected map[string]string
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
		d.failures, d.lastErr, d.keyGone, d.rejected = map[string]int{}, map[string]string{}, map[string]bool{}, map[string]string{}
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
		rejected := false
		for _, notice := range earliestPerInstrument(notices) {
			created, eventID, err := d.Registrar.Register(ctx, notice)
			if errors.Is(err, ErrInvalidNotice) {
				rejected = true
			}
			if err != nil {
				log.ErrorContextf(ctx, "[Collector] 登记下架通知 %s:%s:%s 失败: %v", notice.Exchange, notice.Market, notice.Symbol, err)
				continue
			}
			if created {
				log.InfoContextf(ctx, "[Collector] 发现标的即将下架 %s:%s:%s 下架时间 %s 来源 %s 事件 %s", notice.Exchange, notice.Market, notice.Symbol, notice.DelistAt.Format(time.RFC3339), name, eventID)
			}
		}
		if rejected {
			// 每个来源一段固定文字：detail 带条数、第一个被拒的条目之类会变的内容，每变一次 Monitor 就多一条新序列；具体条目看日志。
			d.rejected[name] = name + " 有下架条目被校验拒绝、没有发布（可能漏报），详见 Collector 日志"
		} else {
			delete(d.rejected, name)
		}
	}
	d.publishLocked()
}

// earliestPerInstrument 同一来源一轮里，同一（交易所、市场、代码、范围）出现多条时只保留下架时间最早的：
// 币安现货下架计划里同一个代码可能出现在两个下架时间组，不去重的话，每一轮都会在两个时间之间来回改、反复发布。
func earliestPerInstrument(notices []Notice) []Notice {
	type key struct{ exchange, market, symbol, scope string }
	index := map[key]int{}
	var out []Notice
	for _, notice := range notices {
		// 交易所代码在去重之前先统一成大写（Register 也会再统一一次）：同一个币的不同写法是同一个标的，只保留下架时间最早的一条。
		notice.Symbol = strings.ToUpper(strings.TrimSpace(notice.Symbol))
		scope := notice.Scope
		if scope == "" {
			scope = events.DelistScopeAll
		}
		k := key{notice.Exchange, notice.Market, notice.Symbol, scope}
		if i, ok := index[k]; ok {
			if notice.DelistAt.Before(out[i].DelistAt) {
				out[i] = notice
			}
			continue
		}
		index[k] = len(out)
		out = append(out, notice)
	}
	return out
}

func (d *Detector) publishLocked() {
	if d.Attention == nil {
		return
	}
	items := map[string][]string{}
	for name, count := range d.failures {
		if count >= SourceFailureThreshold {
			items[attention.KindSourceFailed] = append(items[attention.KindSourceFailed], fmt.Sprintf("%s 连续 %d 次以上失败：%s", name, SourceFailureThreshold, stable(d.lastErr[name])))
		}
	}
	for _, reason := range d.rejected {
		items[attention.KindSourceFailed] = append(items[attention.KindSourceFailed], reason)
	}
	for name, missing := range d.keyGone {
		if missing {
			items[attention.KindReadonlyKeyMissing] = append(items[attention.KindReadonlyKeyMissing], name+readonlyKeyMissingDetail)
		}
	}
	d.Attention.Set("delist", items)
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
