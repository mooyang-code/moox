package bootstrap

import (
	"sort"
	"strings"

	"github.com/mooyang-code/moox/packages/report"
)

// archive-mgr 上报的关注项（moox_attention_active）。
const (
	// attentionWorkerOffline 是有启用的归档任务、而归档 worker 2 分钟内没有心跳。
	attentionWorkerOffline = "worker_offline"
	// attentionTaskFailing 是有归档任务的健康等级为异常。
	attentionTaskFailing = "task_failing"
	// attentionObjectStoreDown 是 worker 报告对象存储连续不可用超过 5 分钟。
	attentionObjectStoreDown = "object_store_down"
	// attentionTaskEnsureFailed 是自动补齐归档任务失败（读取 Storage 数据集清单失败，或有符合条件的数据集无法建出任务）。
	attentionTaskEnsureFailed = "task_ensure_failed"
)

// attentionKinds 是 archive-mgr 声明的全部关注项。
var attentionKinds = []report.AttentionKind{
	{Kind: attentionWorkerOffline, Name: "归档 worker 离线", Severity: report.AttentionCritical},
	{Kind: attentionTaskFailing, Name: "归档任务异常", Severity: report.AttentionCritical},
	{Kind: attentionObjectStoreDown, Name: "归档对象存储不可用", Severity: report.AttentionCritical},
	{Kind: attentionTaskEnsureFailed, Name: "归档任务自动补齐失败", Severity: report.AttentionCritical},
}

// objectStoreAttention 返回对象存储不可用的 detail：不可用超过 5 分钟才报；没有配置对象存储或 worker 不在线（ok 为假）时不报，
// worker 离线由 worker_offline 说明。
func objectStoreAttention(ok bool, worker string, state int, probeError string) []string {
	if !ok || state != report.ObjectStoreDown {
		return nil
	}
	return []string{worker + " 的对象存储不可用已超过 5 分钟：" + probeError}
}

// taskAttention 列出健康等级为异常的归档任务（空间/数据集）。
func taskAttention(failing []string) []string {
	if len(failing) == 0 {
		return nil
	}
	sorted := append([]string(nil), failing...)
	sort.Strings(sorted)
	// 合成一条：对象按序在前、处理提示在后（多条会被重新排序）。
	return []string{strings.Join(sorted, "；") + "。用 moox-cli archive tasks list 查看原因"}
}

// ensureAttention 返回自动补齐归档任务失败的 detail。
func ensureAttention(issues []string) []string {
	if len(issues) == 0 {
		return nil
	}
	sorted := append([]string(nil), issues...)
	sort.Strings(sorted)
	return []string{strings.Join(sorted, "；")}
}
