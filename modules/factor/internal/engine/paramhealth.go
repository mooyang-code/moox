package engine

import (
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

// paramHealth 汇总各成员的参数状态随心跳报给管理端：连续沿用的期数（超过阈值监控告警）与最近一次离线冻结。
// 只列出需要关注的成员（沿用过或离线冻结过），不让心跳随成员总数增长。
func (r *Runtime) paramHealth() ([]domain.MemberParamHealth, int) {
	if r.runner == nil {
		return nil, 0
	}
	var out []domain.MemberParamHealth
	for _, set := range r.catalog.Sets() {
		for _, member := range set.Members {
			carried := r.runner.carriedPeriods(set.Set.TaskID, member.Alias)
			offlineAt := r.lastOffline(set.Set.TaskID, member.Alias)
			if carried == 0 && offlineAt.IsZero() {
				continue
			}
			out = append(out, domain.MemberParamHealth{TaskID: set.Set.TaskID, Member: member.Alias, CarriedPeriods: carried,
				AlertAfter: member.CarryAlertAfter(), LastOfflineAt: offlineAt})
		}
	}
	pending := 0
	if r.params != nil {
		pending = r.params.OfflinePending()
	}
	return out, pending
}

func (r *Runtime) lastOffline(taskID, member string) (at time.Time) {
	if r.params == nil {
		return at
	}
	return r.params.LastOffline(taskID, member)
}
