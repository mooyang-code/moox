package bootstrap

import "github.com/mooyang-code/moox/packages/report"

// factor-mgr 上报的关注项（moox_attention_active），阈值在各自的判断处。
const (
	// attentionWatermarkStalled 是有因子任务的连续水位阻塞超过 30 分钟。
	attentionWatermarkStalled = "watermark_stalled"
)

// attentionKinds 是 factor-mgr 声明的全部关注项。
var attentionKinds = []report.AttentionKind{
	{Kind: attentionWatermarkStalled, Name: "因子连续水位停滞", Severity: report.AttentionCritical},
}
