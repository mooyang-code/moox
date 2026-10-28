package bootstrap

import (
	"context"
	"errors"
	"fmt"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/storageauth"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/robfig/cron"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	tagStaleFactor = 3
	tagCheckPrefix = "subject_tag:"
	// 标签属性里的同步配置键与缺省 cron，与 Collector 的 internal/tagattr 一致。
	tagAttrSyncCron     = "sync.cron"
	tagAttrSyncTimezone = "sync.timezone"
	defaultTagSyncCron  = "0 * * * *"
)

// TagSyncStatusSource 读取 Collector 的标签同步运行状态：标签只是一组标的，同步任务的运行结果在 Collector 的库里。
type TagSyncStatusSource interface {
	ListTagSyncStatus(ctx context.Context, spaceID string) ([]*collectorpb.TagSyncStatus, error)
}

// checkTag evaluates the health of one tag definition. Member counts are
// queried by Storage, run results come from Collector, and freshness is derived
// from the sync cron and timezone in the tag attributes, so a slow or failed
// sync cannot be mistaken for a healthy catalog. A tag without a run record
// counts as stale.
func checkTag(tag *storagepb.Tag, run *collectorpb.TagSyncStatus, now time.Time) []string {
	if tag == nil {
		return []string{"missing"}
	}
	reasons := make([]string, 0, 3)
	if run.GetLastStatus() == "failed" {
		reasons = append(reasons, "failed")
	}
	if tag.GetActiveCount() == 0 {
		reasons = append(reasons, "no_active_members")
	}
	if tagStale(tag, run, now) {
		reasons = append(reasons, "stale")
	}
	if len(reasons) == 0 {
		return nil
	}
	return reasons
}

func tagStale(tag *storagepb.Tag, run *collectorpb.TagSyncStatus, now time.Time) bool {
	lastRun, err := time.Parse(time.RFC3339, run.GetLastRunAt())
	if err != nil {
		return true
	}
	schedule, err := cron.ParseStandard(firstNonEmptyString(tag.GetAttributes()[tagAttrSyncCron], defaultTagSyncCron))
	if err != nil {
		return true
	}
	loc, err := time.LoadLocation(firstNonEmptyString(tag.GetAttributes()[tagAttrSyncTimezone], "UTC"))
	if err != nil {
		return true
	}
	next := schedule.Next(lastRun.In(loc))
	period := schedule.Next(next).Sub(next)
	return now.Sub(lastRun) > time.Duration(tagStaleFactor)*period
}

func subjectTagCheckID(spaceID, tagID string) string {
	return tagCheckPrefix + strings.TrimSpace(spaceID) + ":" + strings.TrimSpace(tagID)
}

// marketCanaryCheckKey 是行情探针检查的完整身份：MarketCanaryCheckID 不含空间。
type marketCanaryCheckKey struct{ spaceID, checkID string }

// retireUnconfiguredMarketCanaries 退役不在当前配置中的探针检查（按 空间+CheckID 匹配）。
func retireUnconfiguredMarketCanaries(ctx context.Context, repositories *store.Repositories, configured map[marketCanaryCheckKey]struct{}) error {
	// Canary identity includes the symbol. When configuration moves from an
	// inactive symbol to an active one, leave no enabled check behind for the
	// old identity; otherwise its last failed result remains visible forever in
	// the business overview and can continue to drive a stale alert.
	checks, err := repositories.Checks.List(ctx, store.ListChecksOptions{
		Source: domain.CheckSourceObservability,
		Page:   store.Page{PageSize: 500},
	})
	if err != nil {
		return err
	}
	for index := range checks {
		check := &checks[index]
		if !strings.HasPrefix(check.CheckID, "market_canary:") {
			continue
		}
		if _, keep := configured[marketCanaryCheckKey{check.SpaceID, check.CheckID}]; keep {
			continue
		}
		rules, err := repositories.Alerts.ListRulesForCheck(ctx, check.SpaceID, check.CheckID)
		if err != nil {
			return err
		}
		// Remove obsolete rules instead of merely disabling them. DeleteRule also
		// removes a previously firing AlertState, so a retired symbol cannot keep
		// contributing a stale alert to the availability overview.
		for index := range rules {
			if err := repositories.Alerts.DeleteRule(ctx, rules[index].SpaceID, rules[index].RuleID); err != nil {
				return err
			}
		}
		check.Enabled = false
		if err := repositories.Checks.Update(ctx, check); err != nil {
			return err
		}
	}
	return nil
}

func buildMonitorMarketCanary(
	ctx context.Context,
	cfg *config.Config,
	runtime *Runtime,
	metricsStorage *monmetrics.StorageAdapter,
	tagRuns TagSyncStatusSource,
	hook func(context.Context, domain.Check, domain.CheckResult),
) (func(context.Context) error, func(context.Context) error, error) {
	if cfg == nil || !cfg.MarketCanary.Enabled {
		return nil, nil, nil
	}
	if runtime == nil || runtime.Repositories == nil {
		return nil, nil, fmt.Errorf("monitor market canary requires repositories")
	}
	if runtime.StorageGateway == nil {
		return nil, nil, fmt.Errorf("monitor market canary requires the shared Storage gateway client")
	}
	reader := runtime.StorageGateway
	canaries := make([]watchdog.MarketCanary, 0, len(cfg.MarketCanary.Subjects))
	tagSpaces := make([]string, 0, len(cfg.MarketCanary.Subjects))
	seenTagSpaces := make(map[string]struct{})
	// 检查身份是 (空间, CheckID)：MarketCanaryCheckID 不含空间，把探针从 A 空间迁到 B 空间时旧空间的检查必须被退役
	configuredCheckIDs := make(map[marketCanaryCheckKey]struct{}, len(cfg.MarketCanary.Subjects))
	for _, subject := range cfg.MarketCanary.Subjects {
		if spaceID := strings.TrimSpace(subject.SpaceID); spaceID != "" {
			if _, ok := seenTagSpaces[spaceID]; !ok {
				seenTagSpaces[spaceID] = struct{}{}
				tagSpaces = append(tagSpaces, spaceID)
			}
		}
		canaryConfig := watchdog.MarketCanaryConfig{
			SpaceID: subject.SpaceID, DatasetID: subject.DatasetID, SubjectID: subject.Symbol, Frequency: subject.Frequency,
			SeriesTag: subject.SeriesTag,
			Freshness: cfg.MarketCanary.Freshness, ReturnThreshold: cfg.MarketCanary.ReturnThreshold,
			MarketID: subject.MarketID, CalendarPath: subject.CalendarPath,
			SettleDelay: cfg.MarketCanary.SettleDelay, PostCloseDelay: cfg.MarketCanary.PostCloseDelay, CalendarWarningLead: cfg.MarketCanary.CalendarWarningLead,
			ClosedBarCount: cfg.MarketCanary.ClosedBarCount, ClosedBarMinCoverage: cfg.MarketCanary.ClosedBarMinCoverage,
			EligibleKlineProviders: append([]string(nil), subject.EligibleKlineProviders...),
		}
		check := domain.Check{
			SpaceID: canaryConfig.SpaceID, CheckID: watchdog.MarketCanaryCheckID(canaryConfig),
			Name:      "行情探针 · " + canaryConfig.SubjectID + " " + alerttext.Frequency(canaryConfig.Frequency) + "K线",
			GroupName: "business", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability,
			Enabled: true, IntervalSeconds: 30, TimeoutMS: 20000,
		}
		configuredCheckIDs[marketCanaryCheckKey{check.SpaceID, check.CheckID}] = struct{}{}
		existing, getErr := runtime.Repositories.Checks.Get(ctx, check.SpaceID, check.CheckID)
		switch {
		case getErr == nil:
			check.ID = existing.ID
			if err := runtime.Repositories.Checks.Update(ctx, &check); err != nil {
				return nil, nil, err
			}
		case errors.Is(getErr, gorm.ErrRecordNotFound):
			if err := runtime.Repositories.Checks.Create(ctx, &check); err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, getErr
		}
		canaries = append(canaries, watchdog.MarketCanary{
			Reader: reader, AuthInfo: storageauth.Primary("monitor-market-canary"), Config: canaryConfig,
		})
	}
	if err := retireUnconfiguredMarketCanaries(ctx, runtime.Repositories, configuredCheckIDs); err != nil {
		return nil, nil, err
	}
	run := func(runCtx context.Context) error {
		var errs []error
		for _, canary := range canaries {
			result := canary.Run(runCtx)
			inserted, err := runtime.Repositories.Results.InsertIfAbsent(runCtx, &result)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !inserted {
				continue
			}
			check, err := runtime.Repositories.Checks.Get(runCtx, result.SpaceID, result.CheckID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			check.LastCheckedAt = &result.CheckedAt
			if err := runtime.Repositories.Checks.Update(runCtx, check); err != nil {
				errs = append(errs, err)
				continue
			}
			if hook != nil {
				hook(runCtx, *check, result)
			}
		}
		return errors.Join(errs...)
	}
	probe := func(probeCtx context.Context) error {
		for _, canary := range canaries {
			if err := canary.ProbeStorageAuth(probeCtx); err != nil {
				return fmt.Errorf("%s: %w", watchdog.MarketCanaryTarget(canary.Config), err)
			}
		}
		return nil
	}
	if metricsStorage != nil && tagRuns != nil {
		previousRun := run
		run = func(runCtx context.Context) error {
			err := previousRun(runCtx)
			now := time.Now().UTC()
			for _, spaceID := range tagSpaces {
				tags, tagErr := metricsStorage.ListTags(runCtx, spaceID)
				if tagErr != nil {
					err = errors.Join(err, tagErr)
					continue
				}
				statuses, statusErr := tagRuns.ListTagSyncStatus(runCtx, spaceID)
				if statusErr != nil {
					// Collector 暂时不可达（启动顺序、重启）时不能让定时器失败：首次运行出错会让 Monitor 整个退出。
					// Collector 自身的健康检查会报告它不可用，这里只跳过本轮标签检查。
					log.WarnContextf(runCtx, "monitor skip tag checks for space %s: collector tag sync status unavailable: %v", spaceID, statusErr)
					continue
				}
				runsByTag := make(map[string]*collectorpb.TagSyncStatus, len(statuses))
				for _, status := range statuses {
					runsByTag[status.GetTagId()] = status
				}
				for _, tag := range tags {
					if tag == nil || !tag.GetBuiltin() {
						continue
					}
					checkID := subjectTagCheckID(spaceID, tag.GetTagId())
					check, getErr := ensureSubjectTagCheck(runCtx, runtime.Repositories, spaceID, tag)
					if getErr != nil {
						err = errors.Join(err, getErr)
						continue
					}
					reasons := checkTag(tag, runsByTag[tag.GetTagId()], now)
					result := domain.CheckResult{ResultID: domain.ScopedResultID(spaceID, checkID, now), SpaceID: spaceID, CheckID: checkID, InstanceID: "monitor", Success: len(reasons) == 0, Connected: true, Status: domain.CheckStatusOK, ErrorMessage: tagReasonText(reasons), CheckedAt: now, CreatedAt: now}
					if !result.Success {
						result.Status = domain.CheckStatusDown
					}
					inserted, insertErr := runtime.Repositories.Results.InsertIfAbsent(runCtx, &result)
					if insertErr != nil {
						err = errors.Join(err, insertErr)
						continue
					}
					if inserted {
						check.LastCheckedAt = &now
						if updateErr := runtime.Repositories.Checks.Update(runCtx, check); updateErr != nil {
							err = errors.Join(err, updateErr)
						} else if hook != nil {
							hook(runCtx, *check, result)
						}
					}
				}
			}
			return err
		}
	}
	return run, probe, nil
}

// ensureSubjectTagCheck 创建或修复标签检查：探针曾被关闭而退役（Enabled=false）后重新启用，必须恢复检查，
// 默认告警规则随周期同步重建。
func ensureSubjectTagCheck(ctx context.Context, repositories *store.Repositories, spaceID string, tag *storagepb.Tag) (*domain.Check, error) {
	checkID := subjectTagCheckID(spaceID, tag.GetTagId())
	check, err := repositories.Checks.Get(ctx, spaceID, checkID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		check = &domain.Check{SpaceID: spaceID, CheckID: checkID, Name: "采集对象标签 · " + tag.GetTagName(), GroupName: "business", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true, IntervalSeconds: 30, TimeoutMS: 20000}
		return check, repositories.Checks.Create(ctx, check)
	}
	if err != nil {
		return nil, err
	}
	if check.Name != "Subject tag "+tag.GetTagName() || !check.Enabled {
		check.Name = "Subject tag " + tag.GetTagName()
		check.Enabled = true
		return check, repositories.Checks.Update(ctx, check)
	}
	return check, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// tagReasonText explains a subject tag's problems to people.
func tagReasonText(reasons []string) string {
	text := map[string]string{
		"missing":           "标签不存在",
		"failed":            "最近一次刷新失败",
		"no_active_members": "标签下没有可用的采集对象",
		"stale":             "标签长时间没有刷新",
	}
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if value := text[reason]; value != "" {
			parts = append(parts, value)
		} else {
			parts = append(parts, reason)
		}
	}
	return strings.Join(parts, "；")
}
