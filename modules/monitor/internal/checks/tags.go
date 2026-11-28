package checks

import (
	"context"
	"strings"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
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

// SubjectTagLister 读取 Storage 的标签定义与成员统计；spaceID 为空表示所有空间。
type SubjectTagLister interface {
	ListTags(ctx context.Context, spaceID string) ([]*storagepb.Tag, error)
}

// SubjectTagCheckID 返回标的标签检查的标识。
func SubjectTagCheckID(spaceID, tagID string) string {
	return SubjectTagPrefix + strings.TrimSpace(spaceID) + ":" + strings.TrimSpace(tagID)
}

// checkTag 评估单个标签的健康状况：成员数来自 Storage，运行结果来自 Collector，
// 新鲜度按标签属性里的 cron 与时区推导；没有运行记录的标签视为过期。
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

// SubjectTagFacts 巡检所有空间的内置标签：与行情探针配在哪些空间无关，A 股、港股的内置标签同样要看。
// 非内置标签（经接口、管理台新建的）不巡检。Collector 的标签同步状态读不到时 available 为 false（标签检查保持原状态）：
// Collector 暂时不可达（启动顺序、重启）时不能让每次重启推一对“恢复、重新触发”。
func SubjectTagFacts(ctx context.Context, tags SubjectTagLister, tagRuns TagSyncStatusSource, now time.Time) ([]Fact, bool, error) {
	items, err := tags.ListTags(ctx, "")
	if err != nil {
		return nil, false, err
	}
	statuses, err := tagRuns.ListTagSyncStatus(ctx, "")
	if err != nil {
		log.WarnContextf(ctx, "跳过标签检查，无法读取 Collector 标签同步状态: %v", err)
		return nil, false, nil
	}
	runsByTag := make(map[string]*collectorpb.TagSyncStatus, len(statuses))
	for _, status := range statuses {
		runsByTag[SubjectTagCheckID(status.GetSpaceId(), status.GetTagId())] = status
	}
	var facts []Fact
	for _, tag := range items {
		if tag == nil || !tag.GetBuiltin() {
			continue
		}
		spaceID := tag.GetSpaceId()
		checkID := SubjectTagCheckID(spaceID, tag.GetTagId())
		reasons := checkTag(tag, runsByTag[checkID], now)
		facts = append(facts, Fact{
			SpaceID: spaceID, CheckID: checkID, Name: subjectTagCheckName(tag),
			Status: PassStatus(len(reasons) == 0), Reason: tagReasonText(reasons),
		})
	}
	return facts, true, nil
}

func subjectTagCheckName(tag *storagepb.Tag) string {
	return "采集对象标签 · " + tag.GetTagName()
}

// tagReasonText 把标签问题码转成人可读的中文原因。
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
