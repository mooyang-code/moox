package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmarketfetch "github.com/mooyang-code/moox/modules/monitor/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/msgbox"
	"gorm.io/gorm"
)

const marketFetchCheckPrefix = "market_fetch:"

// evaluateMarketFetchFreshness turns the latest completion snapshot into the
// same CheckResult/alert path used by the other Monitor watchdogs. It is
// deliberately best effort: Collector remains the owner of detailed batch
// and RetryItem state, while Monitor only needs to detect stale/failed work.
func evaluateMarketFetchFreshness(ctx context.Context, runtime *Runtime, hook func(context.Context, domain.Check, domain.CheckResult)) error {
	if runtime == nil || runtime.MarketFetchStore == nil || runtime.Repositories == nil {
		return nil
	}
	now := time.Now().UTC()
	for _, status := range runtime.MarketFetchStore.Snapshots() {
		check := marketFetchCheck(status)
		if err := ensureMarketFetchCheck(ctx, runtime.Repositories, check); err != nil {
			return err
		}
		result := marketFetchResult(check, status, now)
		inserted, err := runtime.Repositories.Results.InsertIfAbsent(ctx, &result)
		if err != nil {
			return err
		}
		if inserted && hook != nil {
			hook(ctx, check, result)
		}
	}
	return nil
}

func marketFetchCheck(status monmarketfetch.Status) domain.Check {
	checkID := marketFetchCheckPrefix + status.DatasetID + ":" + status.Frequency
	return domain.Check{
		SpaceID: status.SpaceID, CheckID: checkID,
		Name:      fmt.Sprintf("行情采集 %s/%s", status.DatasetID, status.Frequency),
		GroupName: "market_fetch", Kind: domain.CheckKindExternal, Enabled: true,
		Source: domain.CheckSourceObservability, IntervalSeconds: 30, TimeoutMS: 2000,
		Description: "SCF 短时行情采集完成度与新鲜度",
	}
}

func marketFetchResult(check domain.Check, status monmarketfetch.Status, now time.Time) domain.CheckResult {
	result := domain.CheckResult{
		ResultID: fmt.Sprintf("%s:%d", check.CheckID, now.Truncate(30*time.Second).Unix()),
		SpaceID:  check.SpaceID, CheckID: check.CheckID, InstanceID: "monitor",
		Success: true, Status: domain.CheckStatusOK, CheckedAt: now.UTC(),
	}
	threshold := marketFetchFreshnessThreshold(status.Frequency)
	if status.Status != "succeeded" {
		result.Success = false
		result.Status = domain.CheckStatusDown
		result.ErrorMessage = fmt.Sprintf("行情采集批次状态异常：%s", firstNonEmpty(status.ErrorSummary, status.Status))
	} else if status.CompletedAt.IsZero() || now.Sub(status.CompletedAt.UTC()) > threshold {
		result.Success = false
		result.Status = domain.CheckStatusDown
		if status.CompletedAt.IsZero() {
			result.ErrorMessage = "尚未收到行情采集完成回执"
		} else {
			result.ErrorMessage = fmt.Sprintf("最近成功批次已超过允许新鲜度：%s，允许 %s", now.Sub(status.CompletedAt.UTC()).Round(time.Second), threshold)
		}
	}
	if status.RetryCount > 0 {
		result.ErrorMessage = strings.TrimSpace(result.ErrorMessage + fmt.Sprintf("；当前批次待重试 %d 项", status.RetryCount))
	}
	body, _ := json.Marshal(map[string]any{
		"dataset_id": status.DatasetID, "frequency": status.Frequency, "batch_id": status.BatchID,
		"schedule_id": status.ScheduleID, "batch_kind": status.BatchKind, "region": status.Region,
		"node_id": status.NodeID, "request_id": status.RequestID,
		"status": status.Status, "success_count": status.SuccessCount, "retry_count": status.RetryCount,
		"completed_at": status.CompletedAt,
	})
	result.BodyExcerpt = string(body)
	return result
}

func ensureMarketFetchCheck(ctx context.Context, repositories *store.Repositories, check domain.Check) error {
	if repositories == nil {
		return fmt.Errorf("monitor repositories are required")
	}
	existing, err := repositories.Checks.Get(ctx, check.SpaceID, check.CheckID)
	switch {
	case err == nil:
		check.ID = existing.ID
		if err := repositories.Checks.Update(ctx, &check); err != nil {
			return err
		}
	case errorsIsNotFound(err):
		if err := repositories.Checks.Create(ctx, &check); err != nil {
			return err
		}
	default:
		return err
	}
	webhookURL := strings.TrimSpace(os.Getenv("MOOX_MSGBOX_WECOM_WEBHOOK"))
	if webhookURL == "" {
		return nil
	}
	if _, err := msgbox.NewWeComSender(webhookURL); err != nil {
		return err
	}
	if err := ensureDefaultWebhook(ctx, repositories, check.SpaceID, webhookURL); err != nil {
		return err
	}
	rule := &domain.AlertRule{SpaceID: check.SpaceID, RuleID: "default:" + check.CheckID, CheckID: check.CheckID, WebhookID: defaultWebhookID, FailureThreshold: 1, SuccessThreshold: 1, MinimumReminderIntervalSeconds: 300, SendOnResolved: true, Enabled: true, Description: "Default MooX market fetch notification"}
	existingRule, err := repositories.Alerts.GetRule(ctx, rule.SpaceID, rule.RuleID)
	switch {
	case err == nil:
		rule.ID = existingRule.ID
		return repositories.Alerts.UpdateRule(ctx, rule)
	case errorsIsNotFound(err):
		return repositories.Alerts.CreateRule(ctx, rule)
	default:
		return err
	}
}

func errorsIsNotFound(err error) bool { return err != nil && errors.Is(err, gorm.ErrRecordNotFound) }

func marketFetchFreshnessThreshold(frequency string) time.Duration {
	frequency = strings.TrimSpace(strings.ToLower(frequency))
	if len(frequency) < 2 {
		return 10 * time.Minute
	}
	value, err := strconv.Atoi(frequency[:len(frequency)-1])
	if err != nil || value <= 0 {
		return 10 * time.Minute
	}
	var interval time.Duration
	switch frequency[len(frequency)-1] {
	case 's':
		interval = time.Duration(value) * time.Second
	case 'm':
		interval = time.Duration(value) * time.Minute
	case 'h':
		interval = time.Duration(value) * time.Hour
	case 'd':
		interval = time.Duration(value) * 24 * time.Hour
	default:
		return 10 * time.Minute
	}
	if interval < time.Minute {
		interval = time.Minute
	}
	return 2 * interval
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "unknown"
}
