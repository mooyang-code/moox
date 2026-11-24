// Package tradingstatus 检测并记录每个标的在每个市场（标签成员）上的交易状态：trading 正常、suspended 停牌、delisted 退市。
//
// 状态写在 Storage 的标签成员上（t_subject_tags.c_trading_status）。Storage 的 ResolveSubjects 与 ListDatasetSubjects
// 只把可交易的成员当作活跃，所以采集计划、补采、因子、监控、策略选股都不用各自处理停牌；策略对停牌的持仓另行冻结。
// 下架预告的检测（子包 delist）也在这个子模块里：它管“已经预告、将来不可交易”，由 Trade 的下架名单驱动清仓。
package tradingstatus

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/tagattr"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/robfig/cron"
	"trpc.group/trpc-go/trpc-go/log"
)

// 交易状态的取值，与 Storage 的 c_trading_status 一致。
const (
	Trading   = "trading"
	Suspended = "suspended"
	Delisted  = "delisted"
)

// Store 是检测用到的 Storage 元数据接口。
type Store interface {
	ListTags(context.Context) ([]*pb.Tag, error)
	ListTagMembers(ctx context.Context, spaceID, tagID, status string) ([]*pb.TagMember, error)
	SetTagMemberTradingStatus(ctx context.Context, spaceID, tagID string, items []*pb.TagMemberTradingStatus) (int, error)
}

// Source 判定一批标的的交易状态。返回里没有的标的表示“不知道”（请求失败、上游不认识），保留原值；
// 部分批次失败时返回已知的部分和错误，全部失败时返回空结果和错误。
type Source interface {
	Check(ctx context.Context, subjects []string) (map[string]string, error)
}

// Job 是一个市场的检测任务：这个空间里 market_type 相同的标签共用一份结果。
type Job struct {
	SpaceID    string
	MarketType string
	// Cron、Timezone 决定定时触发的时刻（标准 5 段 cron）。
	Cron     string
	Timezone string
	Source   Source
}

func (j Job) name() string { return j.SpaceID + "/" + j.MarketType }

// Runner 按 cron 运行各市场的检测，也可以立即运行某个市场。写 Storage 只在状态有变化时发生。
type Runner struct {
	Store Store
	Jobs  []Job
	Now   func() time.Time
}

// Current 返回调度判断用的当前时间。
func (r *Runner) Current() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// RunDueAt 运行在 (now-1m, now] 窗口内到期的任务。定时回调要在做别的事之前固定触发时间，避免被拖出窗口。
func (r *Runner) RunDueAt(ctx context.Context, now time.Time) error {
	if r == nil {
		return nil
	}
	now = now.UTC()
	var failures error
	for _, job := range r.Jobs {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		due, err := jobDue(job, now)
		if err != nil {
			log.ErrorContextf(ctx, "trading status job %s schedule: %v", job.name(), err)
			failures = errors.Join(failures, err)
			continue
		}
		if due {
			failures = errors.Join(failures, r.runJob(ctx, job))
		}
	}
	return failures
}

// RunAll 立即运行全部任务（Collector 启动后跑一次）。
func (r *Runner) RunAll(ctx context.Context) error {
	return r.Run(ctx, "", "")
}

// Run 立即运行匹配的任务：spaceID、marketType 为空表示不限。推送侧发现疑似下架、手工触发时用。
func (r *Runner) Run(ctx context.Context, spaceID, marketType string) error {
	if r == nil {
		return nil
	}
	var failures error
	for _, job := range r.Jobs {
		if (spaceID != "" && job.SpaceID != spaceID) || (marketType != "" && job.MarketType != marketType) {
			continue
		}
		failures = errors.Join(failures, r.runJob(ctx, job))
	}
	return failures
}

func jobDue(job Job, now time.Time) (bool, error) {
	schedule, err := cron.ParseStandard(job.Cron)
	if err != nil {
		return false, err
	}
	loc, err := time.LoadLocation(job.Timezone)
	if err != nil {
		return false, err
	}
	next := schedule.Next(now.Add(-time.Minute).In(loc))
	return !next.IsZero() && !next.After(now), nil
}

// runJob 检测一个市场：列出本空间这个 market_type 的标签的 active 成员（不受停牌过滤影响，才能发现复牌），问来源，
// 过了整体维护保护后，按标签写回有变化的成员。
func (r *Runner) runJob(ctx context.Context, job Job) error {
	tags, err := r.Store.ListTags(ctx)
	if err != nil {
		return fmt.Errorf("trading status %s: list tags: %w", job.name(), err)
	}
	current := map[string]map[string]string{} // 标签 → 标的 → 当前状态
	subjects := map[string]bool{}
	for _, tag := range tags {
		if tag.GetSpaceId() != job.SpaceID || tagattr.MarketType(tag) != job.MarketType {
			continue
		}
		members, err := r.Store.ListTagMembers(ctx, tag.GetSpaceId(), tag.GetTagId(), "active")
		if err != nil {
			return fmt.Errorf("trading status %s: list members of %s: %w", job.name(), tag.GetTagId(), err)
		}
		statuses := make(map[string]string, len(members))
		for _, member := range members {
			id := member.GetSubject().GetSubjectId()
			statuses[id] = normalize(member.GetTradingStatus())
			subjects[id] = true
		}
		current[tag.GetTagId()] = statuses
	}
	if len(subjects) == 0 {
		return nil
	}
	ids := make([]string, 0, len(subjects))
	for id := range subjects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	found, checkErr := job.Source.Check(ctx, ids)
	if checkErr != nil {
		log.WarnContextf(ctx, "trading status %s: source check: %v", job.name(), checkErr)
	}
	if len(found) == 0 {
		return checkErr
	}
	if reason := maintenance(current, found); reason != "" {
		log.WarnContextf(ctx, "trading status %s: %s, this round writes nothing", job.name(), reason)
		return checkErr
	}
	tagIDs := make([]string, 0, len(current))
	for tagID := range current {
		tagIDs = append(tagIDs, tagID)
	}
	sort.Strings(tagIDs)
	var failures error
	for _, tagID := range tagIDs {
		var items []*pb.TagMemberTradingStatus
		var changes []string
		for _, id := range sortedKeys(current[tagID]) {
			next, ok := found[id]
			if !ok || next == current[tagID][id] {
				continue
			}
			items = append(items, &pb.TagMemberTradingStatus{SubjectId: id, TradingStatus: next})
			changes = append(changes, id+" "+current[tagID][id]+"→"+next)
		}
		if len(items) == 0 {
			continue
		}
		if _, err := r.Store.SetTagMemberTradingStatus(ctx, job.SpaceID, tagID, items); err != nil {
			failures = errors.Join(failures, fmt.Errorf("trading status %s: write %s: %w", job.name(), tagID, err))
			continue
		}
		log.InfoContextf(ctx, "trading status %s %s changed=%d: %s", job.SpaceID, tagID, len(changes), strings.Join(changes, ", "))
	}
	return errors.Join(checkErr, failures)
}

// maintenance 是整体维护保护：查到的成员里一个正常交易的都没有，或者本轮新变成不可交易的成员超过当前可交易成员的一半，
// 就整轮不写（交易所维护、接口异常）。第二条只数这一轮新变的，长期停牌的成员不进比例。返回非空表示触发了保护。
func maintenance(current map[string]map[string]string, found map[string]string) string {
	trading, was, newly := 0, map[string]bool{}, map[string]bool{}
	for _, status := range found {
		if status == Trading {
			trading++
		}
	}
	if trading == 0 {
		return fmt.Sprintf("none of %d checked subjects is trading", len(found))
	}
	for _, statuses := range current {
		for id, status := range statuses {
			if status != Trading {
				continue
			}
			was[id] = true
			if next, ok := found[id]; ok && next != Trading {
				newly[id] = true
			}
		}
	}
	if len(was) > 0 && len(newly)*2 > len(was) {
		return fmt.Sprintf("%d of %d trading subjects became untradable at once", len(newly), len(was))
	}
	return ""
}

func normalize(status string) string {
	if status == "" {
		return Trading
	}
	return status
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
