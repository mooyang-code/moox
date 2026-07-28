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
	"github.com/mooyang-code/moox/packages/msgbox"
	"gorm.io/gorm"
)

const (
	defaultWebhookID = "default-wecom"
	maxDefaultChecks = 1000
)

func ensureDefaultCheckAlertRules(ctx context.Context, repositories *store.Repositories) error {
	webhookURL := strings.TrimSpace(os.Getenv("MOOX_MSGBOX_WECOM_WEBHOOK"))
	if webhookURL == "" {
		return nil
	}
	if repositories == nil {
		return fmt.Errorf("default alerts require repositories")
	}
	if _, err := msgbox.NewWeComSender(webhookURL); err != nil {
		return fmt.Errorf("default WeCom webhook: %w", err)
	}
	checks := make([]domain.Check, 0, 500)
	for page := 1; len(checks) < maxDefaultChecks; page++ {
		batch, err := repositories.Checks.List(ctx, store.ListChecksOptions{Page: store.Page{Page: page, PageSize: 500}})
		if err != nil {
			return err
		}
		checks = append(checks, batch...)
		if len(batch) < 500 {
			break
		}
	}
	if len(checks) >= maxDefaultChecks {
		total, err := repositories.Checks.Count(ctx, store.ListChecksOptions{})
		if err != nil {
			return err
		}
		if total > maxDefaultChecks {
			return fmt.Errorf("default alert checks exceed limit %d", maxDefaultChecks)
		}
	}
	webhookSpaces := make(map[string]struct{})
	for _, check := range checks {
		if check.Enabled {
			webhookSpaces[check.SpaceID] = struct{}{}
		}
	}
	for spaceID := range webhookSpaces {
		if err := ensureDefaultWebhook(ctx, repositories, spaceID, webhookURL); err != nil {
			return err
		}
	}
	for _, check := range checks {
		if !check.Enabled {
			continue
		}
		failureThreshold, successThreshold := 3, 2
		if check.Kind == domain.CheckKindExternal {
			failureThreshold, successThreshold = 1, 1
		}
		rule := &domain.AlertRule{
			SpaceID: check.SpaceID, RuleID: "default:" + check.CheckID, CheckID: check.CheckID,
			WebhookID: defaultWebhookID, FailureThreshold: failureThreshold, SuccessThreshold: successThreshold,
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

func ensureDefaultHostAlertRules(ctx context.Context, repositories *store.Repositories, registry *hostmetrics.Registry) error {
	webhookURL := strings.TrimSpace(os.Getenv("MOOX_MSGBOX_WECOM_WEBHOOK"))
	if webhookURL == "" {
		return nil
	}
	if repositories == nil || registry == nil {
		return fmt.Errorf("default host alerts require repositories and registry")
	}
	if err := ensureDefaultWebhook(ctx, repositories, hostmetrics.SpaceID, webhookURL); err != nil {
		return err
	}
	agents, err := registry.List(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	definitions := map[string]string{
		hostmetrics.HostMetricCPU:             `{"threshold":90,"recovery_threshold":80}`,
		hostmetrics.HostMetricMemory:          `{"threshold":90,"recovery_threshold":80}`,
		hostmetrics.HostMetricFilesystemUsage: `{"threshold":90,"recovery_threshold":85}`,
		hostmetrics.HostMetricDiskUtilization: `{"threshold":90,"recovery_threshold":85}`,
		hostmetrics.HostMetricNetworkErrors:   `{"threshold":1,"recovery_threshold":0.1}`,
	}
	for _, agent := range agents {
		for metric, definition := range definitions {
			checkID := hostmetrics.HostRuleKey(agent.AgentID, metric)
			rule := &domain.AlertRule{
				SpaceID: hostmetrics.SpaceID, RuleID: "default:" + checkID, CheckID: checkID,
				WebhookID: defaultWebhookID, FailureThreshold: 5, SuccessThreshold: 1,
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

func ensureDefaultWebhook(ctx context.Context, repositories *store.Repositories, spaceID, webhookURL string) error {
	if _, err := msgbox.NewWeComSender(webhookURL); err != nil {
		return fmt.Errorf("default WeCom webhook: %w", err)
	}
	webhook := &domain.WebhookChannel{
		SpaceID: spaceID, WebhookID: defaultWebhookID, Name: "MooX default WeCom",
		URL: webhookURL, Method: "POST", Headers: "{}", Enabled: true,
	}
	existing, err := repositories.Alerts.GetWebhook(ctx, spaceID, defaultWebhookID)
	switch {
	case err == nil:
		webhook.ID = existing.ID
		return repositories.Alerts.UpdateWebhook(ctx, webhook)
	case errors.Is(err, gorm.ErrRecordNotFound):
		return repositories.Alerts.CreateWebhook(ctx, webhook)
	default:
		return err
	}
}
