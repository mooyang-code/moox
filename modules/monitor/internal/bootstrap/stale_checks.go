package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// 失效监控配置的清理：Monitor 自动生成的业务检查（来源 observability、分组 business）对应的对象删除之后，
// 检查会一直以“已停用或移除，不再检查”的通过结果存在，每个评估周期都被评估一次，告警规则、状态与结果行也跟着一直留着；
// 已停用的检查行同样没有人删。每删一个策略实例、数据集或主机就多留几条，只增不减。
// 这里在数据清理定时器里按保守的条件清掉它们：
//   - 一直处于“不再检查”超过宽限期（默认 1 小时，避免重启后暂时没数据的检查被误删），并且没有未恢复的告警；
//   - 已停用超过保留期（默认 7 天）。
//
// 主机组件检查（来源 placement）、控制台页面检查等不是自动生成的业务检查，不碰。对象再次出现时检查会按原有逻辑重建。
const (
	defaultRetiredCheckGrace      = time.Hour
	defaultDisabledCheckRetention = 7 * 24 * time.Hour
	defaultStaleCheckBatch        = 100
)

// staleCheckPolicy 是清理参数；零值取默认。
type staleCheckPolicy struct {
	RetiredGrace      time.Duration
	DisabledRetention time.Duration
	// MaxPerRun 是每轮最多清理的检查数，避免一次清太多。
	MaxPerRun int
}

func (p staleCheckPolicy) withDefaults() staleCheckPolicy {
	if p.RetiredGrace <= 0 {
		p.RetiredGrace = defaultRetiredCheckGrace
	}
	if p.DisabledRetention <= 0 {
		p.DisabledRetention = defaultDisabledCheckRetention
	}
	if p.MaxPerRun <= 0 {
		p.MaxPerRun = defaultStaleCheckBatch
	}
	return p
}

// pruneStaleChecks 清理失效的业务检查及其告警规则、状态与结果，返回清理的检查 ID。
func pruneStaleChecks(ctx context.Context, repositories *store.Repositories, now time.Time, policy staleCheckPolicy) ([]string, error) {
	if repositories == nil {
		return nil, fmt.Errorf("prune stale checks requires repositories")
	}
	policy = policy.withDefaults()
	checks, err := listAllMonitorChecks(ctx, repositories)
	if err != nil {
		return nil, err
	}
	var pruned []string
	for index := range checks {
		if len(pruned) >= policy.MaxPerRun || ctx.Err() != nil {
			break
		}
		check := &checks[index]
		if check.Source != domain.CheckSourceObservability || check.GroupName != "business" {
			continue
		}
		stale, err := checkIsStale(ctx, repositories, check, now, policy)
		if err != nil {
			return pruned, err
		}
		if !stale {
			continue
		}
		rules, err := repositories.Alerts.ListRulesForCheck(ctx, check.SpaceID, check.CheckID)
		if err != nil {
			return pruned, err
		}
		firing, err := hasUnresolvedAlert(ctx, repositories, check, rules)
		if err != nil {
			return pruned, err
		}
		if firing {
			continue
		}
		for ruleIndex := range rules {
			if err := repositories.Alerts.DeleteRule(ctx, rules[ruleIndex].SpaceID, rules[ruleIndex].RuleID); err != nil {
				return pruned, err
			}
		}
		if err := repositories.Checks.Delete(ctx, check.SpaceID, check.CheckID); err != nil {
			return pruned, err
		}
		pruned = append(pruned, check.CheckID)
	}
	if len(pruned) > 0 {
		log.Infof("monitor_stale_checks_pruned count=%d checks=%v", len(pruned), pruned)
	}
	return pruned, nil
}

// checkIsStale 判断检查是否已经失效到可以清理：已停用超过保留期；或启用着但最近的结果一直是“不再检查”且已持续超过宽限期。
func checkIsStale(ctx context.Context, repositories *store.Repositories, check *domain.Check, now time.Time, policy staleCheckPolicy) (bool, error) {
	if !check.Enabled {
		return now.Sub(check.UpdatedAt) >= policy.DisabledRetention, nil
	}
	recent, err := repositories.Results.Recent(ctx, check.SpaceID, check.CheckID, 1)
	if err != nil || len(recent) == 0 {
		// 没有任何结果的检查是刚创建的，等它有了结果再判断。
		return false, err
	}
	if recent[0].ErrorMessage != domain.CheckReasonRetired {
		return false, nil
	}
	// 从最后一次真正有效的结果起算；结果已被保留期清掉时退而取最早的一条，再没有就取检查的更新时间。
	since := check.UpdatedAt
	if last, err := repositories.Results.LatestWithoutMessage(ctx, check.SpaceID, check.CheckID, domain.CheckReasonRetired); err != nil {
		return false, err
	} else if last != nil {
		since = last.CheckedAt
	} else if first, err := repositories.Results.Earliest(ctx, check.SpaceID, check.CheckID); err != nil {
		return false, err
	} else if first != nil {
		since = first.CheckedAt
	}
	return now.Sub(since) >= policy.RetiredGrace, nil
}

// hasUnresolvedAlert 报告检查的告警状态里是否还有没恢复的：还在告警（或恢复阈值没满足）的检查先不清。
func hasUnresolvedAlert(ctx context.Context, repositories *store.Repositories, check *domain.Check, rules []domain.AlertRule) (bool, error) {
	for index := range rules {
		state, err := repositories.Alerts.GetState(ctx, rules[index].SpaceID, rules[index].RuleID, check.CheckID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue // 规则还没有产生过状态
		}
		if err != nil {
			return false, err
		}
		if state != nil && state.Status != domain.AlertStatusOK && state.Status != domain.AlertStatusResolved {
			return true, nil
		}
	}
	return false, nil
}
