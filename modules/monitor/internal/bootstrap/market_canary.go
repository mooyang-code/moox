package bootstrap

import (
	"context"
	"fmt"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/checks"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/robfig/cron"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	tagStaleFactor = 3
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
	if run.GetLastRunAt() <= 0 {
		return true
	}
	lastRun := time.UnixMilli(run.GetLastRunAt()).UTC()
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
	return checks.SubjectTagPrefix + strings.TrimSpace(spaceID) + ":" + strings.TrimSpace(tagID)
}

// buildMonitorMarketCanary 返回探针域每轮的评估：行情探针每个配置项一个事实，内置标签每个一个事实。
// Collector 暂停或恢复追赶时整域跳过（保持原状态）；Storage 标签列表读不到时本轮报错、不推进；
// Collector 的标签同步状态读不到时标签检查整类冻结。探针关闭时返回 nil，由 retireMarketCanaries 在启动时把旧检查删掉。
func buildMonitorMarketCanary(
	cfg *config.Config,
	runtime *Runtime,
	applier *checks.Applier,
	metricsStorage *monmetrics.StorageAdapter,
	tagRuns TagSyncStatusSource,
	halted func(context.Context) bool,
) (func(context.Context) error, error) {
	if cfg == nil || !cfg.MarketCanary.Enabled {
		return nil, nil
	}
	if runtime == nil || applier == nil {
		return nil, fmt.Errorf("monitor market canary requires repositories")
	}
	if runtime.StorageGateway == nil {
		return nil, fmt.Errorf("monitor market canary requires the shared Storage gateway client")
	}
	reader := runtime.StorageGateway
	type canaryCheck struct {
		canary watchdog.MarketCanary
		name   string
	}
	canaries := make([]canaryCheck, 0, len(cfg.MarketCanary.Subjects))
	for _, subject := range cfg.MarketCanary.Subjects {
		canaryConfig := watchdog.MarketCanaryConfig{
			SpaceID: subject.SpaceID, DatasetID: subject.DatasetID, SubjectID: subject.Symbol, Frequency: subject.Frequency,
			SeriesTag: subject.SeriesTag,
			Freshness: cfg.MarketCanary.Freshness, ReturnThreshold: cfg.MarketCanary.ReturnThreshold, MarketID: subject.MarketID,
			SettleDelay: cfg.MarketCanary.SettleDelay, PostCloseDelay: cfg.MarketCanary.PostCloseDelay, CalendarWarningLead: cfg.MarketCanary.CalendarWarningLead,
			ClosedBarCount: cfg.MarketCanary.ClosedBarCount, ClosedBarMinCoverage: cfg.MarketCanary.ClosedBarMinCoverage,
			EligibleKlineProviders: append([]string(nil), subject.EligibleKlineProviders...),
		}
		canaries = append(canaries, canaryCheck{
			canary: watchdog.MarketCanary{Reader: reader, Config: canaryConfig},
			name:   "行情探针 · " + canaryConfig.SubjectID + " " + alerttext.Frequency(canaryConfig.Frequency) + "K线",
		})
	}
	return func(runCtx context.Context) error {
		// Collector 模块暂停或恢复追赶时采集是有意停下的：本轮不跑探针，检查保持原状态。
		if halted != nil && halted(runCtx) {
			return nil
		}
		round := checks.Round{Domain: checks.DomainCanary}
		for _, item := range canaries {
			result := item.canary.Run(runCtx)
			round.Facts = append(round.Facts, checks.Fact{
				SpaceID: result.SpaceID, CheckID: result.CheckID, Name: item.name,
				Status: passStatus(result.Success), Reason: result.Reason, Diagnostic: result.Diagnostic,
			})
		}
		if metricsStorage != nil && tagRuns != nil {
			tagFacts, available, err := subjectTagFacts(runCtx, metricsStorage, tagRuns, time.Now().UTC())
			if err != nil {
				return err
			}
			if !available {
				round.FrozenPrefixes = append(round.FrozenPrefixes, checks.SubjectTagPrefix)
			}
			round.Facts = append(round.Facts, tagFacts...)
		} else {
			round.FrozenPrefixes = append(round.FrozenPrefixes, checks.SubjectTagPrefix)
		}
		return applier.Apply(runCtx, round)
	}, nil
}

// retireMarketCanaries 在行情探针关闭时把探针域的旧检查全部删掉（在告警的推一条恢复）：关闭后不再有采样能让它们恢复。
func retireMarketCanaries(ctx context.Context, cfg *config.Config, applier *checks.Applier) error {
	if applier == nil || (cfg != nil && cfg.MarketCanary.Enabled) {
		return nil
	}
	return applier.Apply(ctx, checks.Round{Domain: checks.DomainCanary})
}

// subjectTagLister 读取 Storage 的标签定义与成员统计；spaceID 为空表示所有空间。
type subjectTagLister interface {
	ListTags(ctx context.Context, spaceID string) ([]*storagepb.Tag, error)
}

// subjectTagFacts 巡检所有空间的内置标签：与行情探针配在哪些空间无关，A 股、港股的内置标签同样要看。
// 非内置标签（经接口、管理台新建的）不巡检。Collector 的标签同步状态读不到时 available 为 false（标签检查保持原状态）：
// Collector 暂时不可达（启动顺序、重启）时不能让每次重启推一对“恢复、重新触发”。
func subjectTagFacts(ctx context.Context, tags subjectTagLister, tagRuns TagSyncStatusSource, now time.Time) ([]checks.Fact, bool, error) {
	items, err := tags.ListTags(ctx, "")
	if err != nil {
		return nil, false, err
	}
	statuses, err := tagRuns.ListTagSyncStatus(ctx, "")
	if err != nil {
		log.WarnContextf(ctx, "monitor skip tag checks: collector tag sync status unavailable: %v", err)
		return nil, false, nil
	}
	runsByTag := make(map[string]*collectorpb.TagSyncStatus, len(statuses))
	for _, status := range statuses {
		runsByTag[subjectTagCheckID(status.GetSpaceId(), status.GetTagId())] = status
	}
	var facts []checks.Fact
	for _, tag := range items {
		if tag == nil || !tag.GetBuiltin() {
			continue
		}
		spaceID := tag.GetSpaceId()
		checkID := subjectTagCheckID(spaceID, tag.GetTagId())
		reasons := checkTag(tag, runsByTag[checkID], now)
		facts = append(facts, checks.Fact{SpaceID: spaceID, CheckID: checkID, Name: subjectTagCheckName(tag), Status: passStatus(len(reasons) == 0), Reason: tagReasonText(reasons)})
	}
	return facts, true, nil
}

func subjectTagCheckName(tag *storagepb.Tag) string {
	return "采集对象标签 · " + tag.GetTagName()
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
