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
	maxDefaultChecks = 1000
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
	if repositories == nil {
		return nil, fmt.Errorf("default alerts require repositories")
	}
	checks := make([]domain.Check, 0, 500)
	for page := 1; len(checks) < maxDefaultChecks; page++ {
		batch, err := repositories.Checks.List(ctx, store.ListChecksOptions{Page: store.Page{Page: page, PageSize: 500}})
		if err != nil {
			return nil, err
		}
		checks = append(checks, batch...)
		if len(batch) < 500 {
			break
		}
	}
	if len(checks) >= maxDefaultChecks {
		total, err := repositories.Checks.Count(ctx, store.ListChecksOptions{})
		if err != nil {
			return nil, err
		}
		if total > maxDefaultChecks {
			return nil, fmt.Errorf("default alert checks exceed limit %d", maxDefaultChecks)
		}
	}
	return checks, nil
}

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
		hostmetrics.HostMetricCPU:    `{"threshold":90,"recovery_threshold":80}`,
		hostmetrics.HostMetricMemory: `{"threshold":90,"recovery_threshold":80}`,
		// Filesystem pressure is the leading cause of Storage/View outages. Start
		// warning before the disk is full and evaluate it quickly enough to leave
		// room for cleanup or an operator intervention.
		hostmetrics.HostMetricFilesystemUsage: `{"threshold":85,"recovery_threshold":80}`,
		hostmetrics.HostMetricDiskUtilization: `{"threshold":90,"recovery_threshold":85}`,
		hostmetrics.HostMetricNetworkErrors:   `{"threshold":1,"recovery_threshold":0.1}`,
	}
	for _, agent := range agents {
		for metric, definition := range definitions {
			failureThreshold := 20
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
