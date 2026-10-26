package bootstrap

import (
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/report"
)

// observeParams 把引擎心跳里的参数状态导出成指标，供 Monitor 告警：
//   - moox_factor_param_carry_alert：成员连续沿用参数的期数超过阈值（默认 10 期）为 1；沿用不自动回退默认值，只告警。
//   - moox_factor_param_offline_freeze_timestamp_seconds：任务最近一次离线冻结（FactorMgr 不可达时引擎本地冻结）的时间。
//
// 成员沿用恢复（期数归零）后引擎不再列出它，这里把已经导出过的序列清零，告警随之解除。
func (o *factorDatasetObserver) observeParams(keys map[string]report.DatasetKey, status domain.EngineStatus) {
	if o.carryAlert == nil || o.offlineFreeze == nil {
		return
	}
	if o.carryExported == nil {
		o.carryExported = map[paramMemberKey]string{}
		o.offlineExported = map[string]string{}
	}
	listed := map[paramMemberKey]bool{}
	offline := map[string]int64{}
	for _, item := range status.ParamHealth {
		key, known := keys[item.TaskID]
		if !known {
			continue
		}
		mk := paramMemberKey{item.TaskID, item.Member}
		listed[mk] = true
		value := 0.0
		if item.AlertAfter > 0 && item.CarriedPeriods > item.AlertAfter {
			value = 1
		}
		o.carryAlert.WithLabelValues(key.SpaceID, item.TaskID, item.Member).Set(value)
		o.carryExported[mk] = key.SpaceID
		if !item.LastOfflineAt.IsZero() && item.LastOfflineAt.Unix() > offline[item.TaskID] {
			offline[item.TaskID] = item.LastOfflineAt.Unix()
		}
		if _, seen := o.offlineExported[item.TaskID]; !seen {
			o.offlineExported[item.TaskID] = key.SpaceID
		}
	}
	for mk, space := range o.carryExported {
		if !listed[mk] {
			o.carryAlert.WithLabelValues(space, mk.set, mk.member).Set(0)
		}
	}
	for taskID, space := range o.offlineExported {
		o.offlineFreeze.WithLabelValues(space, taskID).Set(float64(offline[taskID]))
	}
}
