package observability

import (
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

// Storage 上报的关注项（moox_attention_active）：storage-primary 与 storage-view 各报自己的种类，阈值在各自的判断处。
const (
	// AttentionDeadlineRejected 是 storage-primary 最近 10 分钟内有数据集因“周期建立前截止已过”整期提交被拒。
	AttentionDeadlineRejected = "deadline_rejected"
	// AttentionViewFollowLagging 是 storage-view 有 View 距上次追上变更日志头部超过 2 分钟。
	AttentionViewFollowLagging = "view_follow_lagging"
)

// PrimaryAttentionKinds 是 storage-primary 声明的关注项。
var PrimaryAttentionKinds = []report.AttentionKind{
	{Kind: AttentionDeadlineRejected, Name: "存储截止拒绝", Severity: report.AttentionCritical},
}

// ViewAttentionKinds 是 storage-view 声明的关注项。
var ViewAttentionKinds = []report.AttentionKind{
	{Kind: AttentionViewFollowLagging, Name: "View 跟随数据变更落后", Severity: report.AttentionCritical},
}

// NewPrimaryAttention 注册 storage-primary 的关注项指标。
func NewPrimaryAttention(registerer prometheus.Registerer) (*report.Attention, error) {
	return report.NewAttention(registerer, PrimaryAttentionKinds)
}

// NewViewAttention 注册 storage-view 的关注项指标。
func NewViewAttention(registerer prometheus.Registerer) (*report.Attention, error) {
	return report.NewAttention(registerer, ViewAttentionKinds)
}

// viewAttention 持有 storage-view 的关注项上报器；只有 View 角色会设置，storage-primary 进程里为空。
type viewAttention struct {
	attention atomic.Pointer[report.Attention]
}

// SetAttention 设置 storage-view 的关注项上报器（View 角色启动时调用一次）。
func (m *ViewMetrics) SetAttention(attention *report.Attention) {
	if m == nil {
		return
	}
	m.attention.attention.Store(attention)
}

// SetAttentionSource 登记（或在 compute 为 nil 时撤掉）一个采集时现算的关注项来源；没设上报器时什么都不做。
func (m *ViewMetrics) SetAttentionSource(source string, compute func(now time.Time) map[string][]string) {
	if m == nil {
		return
	}
	attention := m.attention.attention.Load()
	if attention == nil {
		return
	}
	if compute == nil {
		compute = func(time.Time) map[string][]string { return nil }
	}
	attention.AddSource(source, compute)
}
