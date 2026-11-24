package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"gorm.io/gorm"
)

const (
	// 仅防止死循环；足以容纳 1024 台主机的全部探针及历史记录。
	maxDefaultCheckPages = 400
)

func ensureDefaultCheckAlertRules(ctx context.Context, repositories *store.Repositories) error {
	if repositories == nil {
		return fmt.Errorf("default alerts require repositories")
	}
	checks, err := listDefaultAlertChecks(ctx, repositories)
	if err != nil {
		return err
	}
	return ensureDefaultRules(ctx, repositories, checks)
}

// ensureDefaultRules 给这些检查补齐（或按当前默认值更新）默认告警规则。
func ensureDefaultRules(ctx context.Context, repositories *store.Repositories, checks []domain.Check) error {
	for _, check := range checks {
		failureThreshold, successThreshold := 3, 2
		if check.Kind == domain.CheckKindExternal {
			failureThreshold, successThreshold = externalFailureThreshold(check.IntervalSeconds), 1
		}
		if strings.HasPrefix(check.CheckID, "kline_freshness:") {
			successThreshold = 2
		}
		if failures, ok := hostCheckFailureThreshold(check.CheckID); ok {
			failureThreshold, successThreshold = failures, 1
		}
		rule := &domain.AlertRule{
			SpaceID: check.SpaceID, RuleID: "default:" + check.CheckID, CheckID: check.CheckID,
			FailureThreshold: failureThreshold, SuccessThreshold: successThreshold,
			MinimumReminderIntervalSeconds: 300, SendOnResolved: true, Enabled: true,
			Description: "Default MooX monitoring notification",
		}
		existing, err := repositories.Alerts.GetRule(ctx, check.SpaceID, rule.RuleID)
		switch {
		case err == nil:
			rule.ID = existing.ID
			if err := repositories.Alerts.UpdateRule(ctx, rule); err != nil {
				return err
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			if err := repositories.Alerts.CreateRule(ctx, rule); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// externalAlertGrace 是外部检查从第一次失败到告警的宽限时间。外部检查（运行指标上报、采集计划对账、
// 行情探针、数据集新鲜度等）每个周期评估一次，服务重启时会有一两个周期读不到指标；原来失败一次就告警，
// 一次重启就推送一对“新告警 / 已恢复”（2026-10-14～15 两天 122 次告警，大多 29 秒内自愈）。
// 持续失败满 60 秒才告警，重启造成的短暂中断不再推送，真正的故障最多晚一分钟。
const externalAlertGrace = 60 * time.Second

// externalFailureThreshold 把宽限时间换算成连续失败次数：首次失败之后还要再失败满宽限时间。
func externalFailureThreshold(intervalSeconds int) int {
	if intervalSeconds <= 0 {
		intervalSeconds = 30
	}
	interval := time.Duration(intervalSeconds) * time.Second
	return 1 + int((externalAlertGrace+interval-1)/interval)
}

func listDefaultAlertChecks(ctx context.Context, repositories *store.Repositories) ([]domain.Check, error) {
	all, err := listAllMonitorChecks(ctx, repositories)
	if err != nil {
		return nil, err
	}
	checks := make([]domain.Check, 0, len(all))
	for _, check := range all {
		if !check.Enabled {
			continue
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func listAllMonitorChecks(ctx context.Context, repositories *store.Repositories) ([]domain.Check, error) {
	if repositories == nil {
		return nil, fmt.Errorf("default alerts require repositories")
	}
	checks := make([]domain.Check, 0, 500)
	// 按页读完整个检查集合（含已停用的历史检查），不能因总量上限阻断 Monitor 启动。
	for page := 1; page <= maxDefaultCheckPages; page++ {
		batch, err := repositories.Checks.List(ctx, store.ListChecksOptions{Page: store.Page{Page: page, PageSize: 500}})
		if err != nil {
			return nil, err
		}
		checks = append(checks, batch...)
		if len(batch) < 500 {
			return checks, nil
		}
	}
	return nil, fmt.Errorf("default alert checks exceed %d pages", maxDefaultCheckPages)
}
