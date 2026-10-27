package subjectsync

import (
	"context"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/internal/tagattr"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/robfig/cron"
	"trpc.group/trpc-go/trpc-go/log"
)

const sqliteTimeLayout = "2006-01-02 15:04:05"

type TagStore interface {
	ListTags(context.Context) ([]*pb.Tag, error)
	ApplyTagSnapshot(context.Context, string, string, []*pb.TagSnapshotItem) error
}

// RunStore 保存标签的同步运行结果（Collector 自己的库）：调度判断读它，结果写它。
type RunStore interface {
	List(ctx context.Context, spaceID string) ([]store.TagSyncRun, error)
	Record(ctx context.Context, run store.TagSyncRun) error
}

type TagRunner struct {
	Store        TagStore
	Runs         RunStore
	Listers      Listers
	FetchTimeout time.Duration
	Now          func() time.Time
	Metrics      *Metrics
	// RetryDelays 是快照取数失败或校验不通过时在本次同步内重试前的等待（默认 2 秒、4 秒，共 3 次）；
	// 都失败就保持原成员不变并上报失败，绝不用一份残缺的快照更新成员。
	RetryDelays []time.Duration
}

var defaultSnapshotRetryDelays = []time.Duration{2 * time.Second, 4 * time.Second}

func probing(tag *pb.Tag) bool {
	if tag == nil {
		return false
	}
	// 只有 auto 标签同步权威成员快照；manual 标签的成员由人维护。
	return strings.EqualFold(tag.GetMode(), "auto") && tagattr.Source(tag) != "" && tagattr.MarketType(tag) != ""
}

func parseTagTime(value string) (time.Time, error) {
	if parsed, err := time.ParseInLocation(sqliteTimeLayout, value, time.UTC); err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, value)
}

// tagDue 判断标签的标的同步是否到期：没有运行记录、标签在上次运行后被改过（含同步配置），或按 cron 到点了。
func tagDue(tag *pb.Tag, run *store.TagSyncRun, now time.Time) (bool, error) {
	if tag == nil || run == nil || run.LastRunAt.IsZero() {
		return true, nil
	}
	if updated, err := parseTagTime(tag.GetUpdatedAt()); err == nil && updated.After(run.LastRunAt) {
		return true, nil
	}
	schedule, err := cron.ParseStandard(tagattr.Cron(tag))
	if err != nil {
		return false, err
	}
	loc, err := time.LoadLocation(tagattr.Timezone(tag))
	if err != nil {
		return false, err
	}
	return !schedule.Next(run.LastRunAt.In(loc)).After(now), nil
}

func (r *TagRunner) RunOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tags, err := r.Store.ListTags(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "list tags: %v", err)
		return err
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	runs, err := r.runsByTag(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "list tag sync runs: %v", err)
		return err
	}
	var failures error
	for _, tag := range tags {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		run := runs[tagKey(tag.GetSpaceId(), tag.GetTagId())]
		if r.Metrics != nil {
			r.Metrics.observeTagInventory(tag, run)
		}
		if !probing(tag) {
			continue
		}
		due, err := tagDue(tag, run, now)
		if err != nil {
			log.ErrorContextf(ctx, "tag %s/%s schedule: %v", tag.GetSpaceId(), tag.GetTagId(), err)
			failures = errors.Join(failures, err)
			continue
		}
		if due {
			_, runErr := r.runTag(ctx, tag, now)
			failures = errors.Join(failures, runErr)
		}
	}
	return failures
}

func tagKey(spaceID, tagID string) string { return spaceID + "/" + tagID }

// runsByTag 一次读出所有标签的运行记录，按（空间，标签）索引。
func (r *TagRunner) runsByTag(ctx context.Context) (map[string]*store.TagSyncRun, error) {
	out := map[string]*store.TagSyncRun{}
	if r.Runs == nil {
		return out, nil
	}
	runs, err := r.Runs.List(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range runs {
		out[tagKey(runs[i].SpaceID, runs[i].TagID)] = &runs[i]
	}
	return out, nil
}

// record 把一次运行结果写进 Collector 的库；写不进去只记日志，不影响本次同步的结果。
func (r *TagRunner) record(ctx context.Context, tag *pb.Tag, runAt time.Time, runErr error) {
	if r.Runs == nil {
		return
	}
	run := store.TagSyncRun{SpaceID: tag.GetSpaceId(), TagID: tag.GetTagId(), LastRunAt: runAt, LastStatus: store.TagSyncSuccess}
	if runErr != nil {
		run.LastStatus, run.LastError = store.TagSyncFailed, runErr.Error()
	}
	if err := r.Runs.Record(ctx, run); err != nil {
		log.ErrorContextf(ctx, "record tag %s/%s sync run: %v", tag.GetSpaceId(), tag.GetTagId(), err)
	}
}

func (r *TagRunner) runTag(ctx context.Context, tag *pb.Tag, runAt time.Time) (int, error) {
	timeout := r.FetchTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	delays := r.RetryDelays
	if delays == nil {
		delays = defaultSnapshotRetryDelays
	}
	instruments, err := FetchSnapshot(fetchCtx, r.Listers, []string{tagattr.Source(tag)}, tagattr.MarketType(tag))
	for attempt := 0; err != nil && attempt < len(delays) && fetchCtx.Err() == nil; attempt++ {
		log.WarnContextf(ctx, "tag %s/%s snapshot attempt %d failed, retrying: %v", tag.GetSpaceId(), tag.GetTagId(), attempt+1, err)
		select {
		case <-time.After(delays[attempt]):
		case <-fetchCtx.Done():
		}
		instruments, err = FetchSnapshot(fetchCtx, r.Listers, []string{tagattr.Source(tag)}, tagattr.MarketType(tag))
	}
	subjects := 0
	if err == nil {
		items := make([]*pb.TagSnapshotItem, 0, len(instruments))
		for _, item := range instruments {
			items = append(items, snapshotItem(tag, item))
		}
		log.InfoContextf(ctx, "tag %s/%s applying snapshot subjects=%d", tag.GetSpaceId(), tag.GetTagId(), len(items))
		err = r.Store.ApplyTagSnapshot(ctx, tag.GetSpaceId(), tag.GetTagId(), items)
		subjects = len(items)
	}
	if err != nil {
		log.WarnContextf(ctx, "tag %s/%s run failed: %v", tag.GetSpaceId(), tag.GetTagId(), err)
		if r.Metrics != nil {
			r.Metrics.observeTagFailure(tag)
		}
		r.record(ctx, tag, runAt, err)
		return 0, err
	}
	r.record(ctx, tag, runAt, nil)
	if r.Metrics != nil {
		r.Metrics.observeTagSuccess(tag, runAt)
	}
	return subjects, nil
}

// SyncTags synchronizes the matching auto tags right now, whether or not they
// are due. An empty filter field matches everything. Every selected tag gets a
// result, so a caller can tell a failed tag from one that was not selected.
func (r *TagRunner) SyncTags(ctx context.Context, filter Filter) ([]TagResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tags, err := r.Store.ListTags(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	runAt := time.Now().UTC()
	if r.Now != nil {
		runAt = r.Now().UTC()
	}
	var results []TagResult
	for _, tag := range tags {
		if !filter.matchTag(tag) {
			continue
		}
		result := TagResult{SpaceID: tag.GetSpaceId(), TagID: tag.GetTagId()}
		switch {
		case !probing(tag):
			result.Status, result.Message = StatusSkipped, "not an auto tag with a source and market type"
		case ctx.Err() != nil:
			result.Status, result.Message = StatusFailed, ctx.Err().Error()
		default:
			subjects, runErr := r.runTag(ctx, tag, runAt)
			if runErr != nil {
				result.Status, result.Message = StatusFailed, runErr.Error()
			} else {
				result.Status, result.Subjects = StatusSuccess, subjects
			}
		}
		results = append(results, result)
	}
	return results, nil
}

func snapshotItem(tag *pb.Tag, item marketdata.Instrument) *pb.TagSnapshotItem {
	out := &pb.TagSnapshotItem{SubjectId: item.SubjectID, Name: item.Name, Attributes: subjectAttributes(tag.GetSpaceId(), item)}
	switch tag.GetSpaceId() {
	case "crypto":
		out.Timezone = "UTC"
	case "stockcn":
		out.Timezone = "Asia/Shanghai"
	}
	return out
}
