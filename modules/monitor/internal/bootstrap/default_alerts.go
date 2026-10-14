package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/notification"
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
	if err := seedNotificationChannel(ctx, repositories); err != nil {
		return err
	}
	checks, err := listDefaultAlertChecks(ctx, repositories)
	if err != nil {
		return err
	}
	for _, check := range checks {
		failureThreshold, successThreshold := 3, 2
		if check.Kind == domain.CheckKindExternal {
			failureThreshold, successThreshold = 1, 1
		}
		if strings.HasPrefix(check.CheckID, "kline_freshness:") {
			failureThreshold, successThreshold = 2, 2
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

// hostCPUFailureThreshold 是 CPU 告警需要连续超过阈值的采样次数。HostAgent 每 15 秒采样一次，12 次约 3 分钟。
const hostCPUFailureThreshold = 12

func ensureDefaultHostAlertRules(ctx context.Context, repositories *store.Repositories, registry *hostmetrics.Registry) error {
	if repositories == nil || registry == nil {
		return fmt.Errorf("default host alerts require repositories and registry")
	}
	if err := seedNotificationChannel(ctx, repositories); err != nil {
		return err
	}
	agents, err := registry.List(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	definitions := map[string]string{
		// CPU 阈值取 80%、连续 12 次采样（约 3 分钟）：2026-10-14 两个 1h 因子集回填，storage-node 吃掉 4 核主机的
		// 330%–380% CPU（整机 82%–95%），读窗口一再超时，却没有一次连续 20 次（5 分钟）都不低于 90%，原来的规则一条告警也没出。
		hostmetrics.HostMetricCPU:    `{"threshold":80,"recovery_threshold":70}`,
		hostmetrics.HostMetricMemory: `{"threshold":90,"recovery_threshold":80}`,
		// Filesystem pressure is the leading cause of Storage/View outages. Start
		// warning before the disk is full and evaluate it quickly enough to leave
		// room for cleanup or an operator intervention.
		hostmetrics.HostMetricFilesystemUsage: `{"threshold":85,"recovery_threshold":80}`,
	}
	// 磁盘繁忙度、网络错误包两类告警已精简掉（个人系统噪声大、价值低）：删除存量规则。
	for _, retired := range []string{"disk_utilization", "network_errors"} {
		for _, agent := range agents {
			checkID := hostmetrics.HostRulePrefix + agent.AgentID + ":" + retired
			if err := repositories.Alerts.DeleteRule(ctx, hostmetrics.SpaceID, "default:"+checkID); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
	}
	for _, agent := range agents {
		for metric, definition := range definitions {
			failureThreshold := 20
			if metric == hostmetrics.HostMetricCPU {
				failureThreshold = hostCPUFailureThreshold
			}
			if metric == hostmetrics.HostMetricFilesystemUsage {
				// HostAgent samples every 15 seconds. Three consecutive samples
				// provide a short, debounced disk alert without paging on one
				// transient statfs result.
				failureThreshold = 3
			}
			checkID := hostmetrics.HostRuleKey(agent.AgentID, metric)
			rule := &domain.AlertRule{
				SpaceID: hostmetrics.SpaceID, RuleID: "default:" + checkID, CheckID: checkID,
				FailureThreshold: failureThreshold, SuccessThreshold: 1,
				MinimumReminderIntervalSeconds: 300, SendOnResolved: true, Enabled: true,
				Description: definition,
			}
			existing, err := repositories.Alerts.GetRule(ctx, rule.SpaceID, rule.RuleID)
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
	}
	return nil
}

func seedNotificationChannel(ctx context.Context, repositories *store.Repositories) error {
	if repositories == nil || repositories.Notifications == nil {
		return fmt.Errorf("notification repository is unavailable")
	}
	typ := strings.TrimSpace(os.Getenv("MOOX_NOTIFICATION_CHANNEL_TYPE"))
	if typ == "" {
		typ = string(notification.ChannelTypeWeCom)
	}
	url := strings.TrimSpace(os.Getenv("MOOX_NOTIFICATION_WEBHOOK_URL"))
	if _, err := notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(typ), WebhookURL: url}); err != nil {
		return fmt.Errorf("notification channel: %w", err)
	}
	return repositories.Notifications.SeedIfAbsent(ctx, domain.NotificationChannel{ChannelID: domain.GlobalNotificationChannelID, ChannelType: typ, WebhookURL: url})
}
