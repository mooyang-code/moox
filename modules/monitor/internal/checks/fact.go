// Package checks 是 Monitor 的检查与告警推进：每轮按事实推进检查表里的行（连续计数、告警状态），
// 不再产出的检查当轮删除，并在事务提交后推送通知。规则、级别都由检查 ID 推出，不入库。
package checks

import (
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

// Fact 是一项检查本轮按指标算出的事实。
type Fact struct {
	SpaceID, CheckID, Name string
	// Severity 是 critical 或 warning；为空时按检查 ID 推出（Severity）。关注项的级别由生产者在指标标签里给出。
	Severity string
	// Status 是 ok、degraded 或 down；degraded 按通过记录（不触发也不延续告警），页面显示“需关注”。
	Status   string
	Reason   string
	RawError string
	// Diagnostic 是机器诊断（K 线评估的诊断串、行情探针阈值明细、主机 CPU/内存检查的进程排行），
	// 只在写触发、提醒事件时带进 payload，不入检查表。
	Diagnostic string
}

// Key 返回事实对应的检查身份。
func (f Fact) Key() store.CheckKey { return store.CheckKey{SpaceID: f.SpaceID, CheckID: f.CheckID} }

// Passed 报告这条事实是否按通过记录。
func (f Fact) Passed() bool { return f.Status != domain.CheckStatusDown }

// Domain 按检查 ID 划分谁负责产出、谁负责删除：一项检查只归一个域，域之间互不删除对方的检查。
type Domain int

const (
	// DomainHost 是主机类检查（host:<主机>:<种类>）。
	DomainHost Domain = iota
	// DomainCanary 是行情探针与内置标签巡检（market_canary:、subject_tag:）。
	DomainCanary
	// DomainBusiness 是其余全部：组件、模块健康、数据集、K 线、关注项与 Monitor 自己的检查。
	DomainBusiness
)

// 探针域的检查 ID 前缀。
const (
	MarketCanaryPrefix = "market_canary:"
	SubjectTagPrefix   = "subject_tag:"
)

// DomainOf 返回检查所属的域。
func DomainOf(checkID string) Domain {
	if _, _, ok := hostmetrics.ParseCheckID(checkID); ok {
		return DomainHost
	}
	if strings.HasPrefix(checkID, MarketCanaryPrefix) || strings.HasPrefix(checkID, SubjectTagPrefix) {
		return DomainCanary
	}
	return DomainBusiness
}

// Round 是一个评估域一轮的输入。
type Round struct {
	Domain Domain
	Facts  []Fact
	// Frozen 是本轮保持原状态的检查：本轮没有事实的冻结行既不推进计数，也不删除。
	// 冻结只作用于没有事实的行：同一项检查本轮有事实时照常推进（例如 K 线前缀整体冻结时，
	// kline_freshness:task_result_inventory 自己仍产出一条失败事实）。
	Frozen map[store.CheckKey]bool
	// FrozenPrefixes 是整类冻结的检查 ID 前缀，例如 K 线本轮没有评估时的 "kline_freshness:"。
	FrozenPrefixes []string
	// FrozenAll 表示本域没有事实的行全部冻结（例如观测输入暂停时的业务域：只推进有事实的那几项）。
	FrozenAll bool
	// FreezeWhen 按行判断是否冻结（例如负责产出它的服务本轮停止上报）；为空时不用。
	FreezeWhen func(store.CheckKey) bool
}

// Freeze 把一项检查放进冻结集合。
func (r *Round) Freeze(key store.CheckKey) {
	if r.Frozen == nil {
		r.Frozen = map[store.CheckKey]bool{}
	}
	r.Frozen[key] = true
}

func (r Round) frozen(key store.CheckKey) bool {
	if r.FrozenAll || r.Frozen[key] || (r.FreezeWhen != nil && r.FreezeWhen(key)) {
		return true
	}
	for _, prefix := range r.FrozenPrefixes {
		if strings.HasPrefix(key.CheckID, prefix) {
			return true
		}
	}
	return false
}
