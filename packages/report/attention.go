package report

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/log"
)

// AttentionMetric 是各模块“需要人看的情况”（关注项）的指标名。上报方身份（服务名、实例）由上报器带，不进标签。
const AttentionMetric = "moox_attention_active"

// 关注项的级别：critical 是故障，warning 只需提醒（功能还在，只少了一部分）。
const (
	AttentionCritical = "critical"
	AttentionWarning  = "warning"
)

// maxAttentionDetail 是 detail 标签的字节上限：Monitor 超过 512 字节会拒收整份上报。
const maxAttentionDetail = 480

// AttentionKind 是生产者声明的一种需要人看的情况：名称是告警标题，级别是 critical（故障）或 warning（只需提醒）。
type AttentionKind struct{ Kind, Name, Severity string }

// Attention 上报指标 moox_attention_active{kind,name,severity,detail}：每个声明过的种类一条，1 表示当前有需要处理的情况，
// detail 列出对象与原因（按字节截到 480）；没有情况时报 0、detail 为空，Monitor 据此恢复。
//
// detail 是标签，内容一变就是一条新序列：只写对象和起始时刻（“自 hh:mm 起”“最后 hh:mm”），不写“约 N 分钟”这类随时间变化的量。
type Attention struct {
	kinds []AttentionKind
	desc  *prometheus.Desc

	mu       sync.Mutex
	sources  map[string]map[string][]string
	computed map[string]func(now time.Time) map[string][]string
	// warned 记下已经打过日志的未声明种类，同一种只打一次。
	warned map[string]bool
}

// NewAttention 声明全部种类并注册指标。同一个注册器上已注册过时返回已有的那份（种类以第一次为准）。
func NewAttention(registerer prometheus.Registerer, kinds []AttentionKind) (*Attention, error) {
	seen := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		if kind.Kind == "" || kind.Name == "" {
			return nil, fmt.Errorf("关注项种类缺少 kind 或名称: %+v", kind)
		}
		if kind.Severity != AttentionCritical && kind.Severity != AttentionWarning {
			return nil, fmt.Errorf("关注项 %s 的级别 %q 无效，只能是 critical 或 warning", kind.Kind, kind.Severity)
		}
		if seen[kind.Kind] {
			return nil, fmt.Errorf("关注项种类 %s 重复声明", kind.Kind)
		}
		seen[kind.Kind] = true
	}
	a := &Attention{
		kinds:    append([]AttentionKind(nil), kinds...),
		desc:     prometheus.NewDesc(AttentionMetric, "Conditions that need a person, with object and reason in the detail label.", []string{"kind", "name", "severity", "detail"}, nil),
		sources:  map[string]map[string][]string{},
		computed: map[string]func(time.Time) map[string][]string{},
		warned:   map[string]bool{},
	}
	if registerer == nil {
		return a, nil
	}
	if err := registerer.Register(a); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			if existing, ok := already.ExistingCollector.(*Attention); ok {
				return existing, nil
			}
		}
		return nil, err
	}
	return a, nil
}

// Set 用本轮结果替换某个来源报告的全部情况（items 按 kind 列出每条详情），各来源合并后上报。适合按库里状态定时重算的生产者。
func (a *Attention) Set(source string, items map[string][]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sources[source] = items
}

// AddSource 登记一个在采集时现算的来源：上报器每次采集指标都会调用它。适合“超过多久”“最近多久内”这类随时间变化的判断，
// 不必等下一次事件才把值翻成 1 或回到 0。compute 在采集时调用，不能阻塞。
func (a *Attention) AddSource(source string, compute func(now time.Time) map[string][]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.computed[source] = compute
}

// Active 返回当前有情况的种类及其 detail（测试与本地排查用）。
func (a *Attention) Active() map[string]string {
	out := map[string]string{}
	for kind, detail := range a.current(time.Now()) {
		if detail != "" {
			out[kind] = detail
		}
	}
	return out
}

// Describe 实现 prometheus.Collector。
func (a *Attention) Describe(ch chan<- *prometheus.Desc) { ch <- a.desc }

// Collect 实现 prometheus.Collector：每个声明过的种类都报一条，没有情况时为 0。
func (a *Attention) Collect(ch chan<- prometheus.Metric) {
	details := a.current(time.Now())
	for _, kind := range a.kinds {
		value := 0.0
		if details[kind.Kind] != "" {
			value = 1
		}
		ch <- prometheus.MustNewConstMetric(a.desc, prometheus.GaugeValue, value, kind.Kind, kind.Name, kind.Severity, details[kind.Kind])
	}
}

// current 合并各来源，返回每个声明过的种类的 detail（没有情况为空）。
func (a *Attention) current(now time.Time) map[string]string {
	a.mu.Lock()
	computed := make([]func(time.Time) map[string][]string, 0, len(a.computed))
	for _, compute := range a.computed {
		computed = append(computed, compute)
	}
	merged := map[string][]string{}
	for _, items := range a.sources {
		for kind, list := range items {
			merged[kind] = append(merged[kind], list...)
		}
	}
	a.mu.Unlock()
	// 现算来源在锁外调用：它们可能要拿自己的锁。
	for _, compute := range computed {
		for kind, list := range compute(now) {
			merged[kind] = append(merged[kind], list...)
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	declared := make(map[string]bool, len(a.kinds))
	for _, kind := range a.kinds {
		declared[kind.Kind] = true
	}
	out := make(map[string]string, len(a.kinds))
	for kind, list := range merged {
		if !declared[kind] {
			if len(list) > 0 && !a.warned[kind] {
				a.warned[kind] = true
				log.Errorf("关注项种类 %q 没有声明，已丢弃：请在 NewAttention 的种类列表里补上", kind)
			}
			continue
		}
		out[kind] = joinAttentionDetails(list)
	}
	return out
}

// joinAttentionDetails 排序、去重后用“；”连接，按字节截到上限并保证是合法 UTF-8。
func joinAttentionDetails(list []string) string {
	if len(list) == 0 {
		return ""
	}
	details := append([]string(nil), list...)
	sort.Strings(details)
	unique := details[:0]
	for i, detail := range details {
		if detail == "" || (i > 0 && detail == details[i-1]) {
			continue
		}
		unique = append(unique, detail)
	}
	detail := strings.Join(unique, "；")
	if detail == "" {
		// 有情况但没给详情：detail 不能为空，否则会被当成 0。
		detail = "（无详情）"
	}
	if len(detail) > maxAttentionDetail {
		detail = strings.ToValidUTF8(detail[:maxAttentionDetail], "") + "…"
	}
	return detail
}
