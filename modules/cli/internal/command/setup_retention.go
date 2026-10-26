package command

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/storagepolicy"
)

// 保留期与因子回看根数是关联的配置：因子引擎启动、预载、补算时要从 Storage 读回「最大回看根数」那么多历史，
// 数据集的保留期（moox.toml 的 [storage_retention]）装不下，历史就读不全，因子要么算不出来、要么窗口里缺根。
// 初始化因子（setup init / setup factors）时在这里核对：保留期必须大于这个集合里启用因子的最大回看根数。
// 7×24 市场（crypto）装不下报错；交易时段市场（股票）按日历估算，只给提醒。

// factorRetentionFindings 返回因子回看根数超过保留期的错误与提醒。
func factorRetentionFindings(policy storagepolicy.Policy, plan setupFactorPlan) (errs, warnings []string) {
	lookbacks := map[string]int{} // factor_id → 回看根数
	for _, def := range plan.Definitions {
		compiled, err := factorexpr.Compile(def.Spec)
		if err != nil {
			// 表达式本身不合法由因子导入时校验并报错，这里不重复。
			continue
		}
		lookbacks[def.FactorID] = compiled.Lookback
	}
	type worst struct {
		factor   string
		lookback int
	}
	byTask := map[string]worst{}
	for _, member := range plan.Members {
		if member.Status != "enabled" {
			continue
		}
		lookback, ok := lookbacks[member.FactorID]
		if !ok {
			continue
		}
		if current, seen := byTask[member.TaskID]; !seen || lookback > current.lookback {
			byTask[member.TaskID] = worst{factor: member.FactorID, lookback: lookback}
		}
	}
	setIDs := make([]string, 0, len(byTask))
	for id := range byTask {
		setIDs = append(setIDs, id)
	}
	sort.Strings(setIDs)
	sets := map[string]setupFactorTask{}
	for _, set := range plan.Sets {
		sets[set.TaskID] = set
	}
	for _, id := range setIDs {
		set, ok := sets[id]
		if !ok {
			continue
		}
		w := byTask[id]
		bars, forever, estimated, err := policy.RetentionBars(set.SpaceID, set.Freq)
		if err != nil || forever || bars > float64(w.lookback) {
			continue
		}
		need := storagepolicy.MinRetentionFor(set.SpaceID, set.Freq, w.lookback+1)
		message := fmt.Sprintf(
			"因子任务 %s（空间 %s，频率 %s）里因子 %s 回看 %d 根，而 [storage_retention] 的保留期只能装下约 %.0f 根：保留期必须大于因子回看根数，否则引擎预载和补算读不到足够的历史。"+
				"请把 storage_retention.spaces.%s.%s（没有空间覆盖时是 storage_retention.defaults.%s）至少调到 %s",
			set.TaskID, set.SpaceID, set.Freq, w.factor, w.lookback, bars, set.SpaceID, set.Freq, set.Freq, need.String())
		if estimated {
			warnings = append(warnings, message+"（交易时段市场按日历估算，仅作提醒）")
		} else {
			errs = append(errs, message)
		}
	}
	return errs, warnings
}

func joinFindings(findings []string) string { return strings.Join(findings, "；") }

// reportFactorWarnings 把配置关联的提醒打到 stderr（同时进入 JSON 摘要的 warnings，Agent 也能看到）。
func reportFactorWarnings(warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintln(os.Stderr, "提醒："+warning)
	}
}
