package subjectsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/tagattr"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/cnstock"
	"github.com/robfig/cron"
	"trpc.group/trpc-go/trpc-go/log"
)

const sqliteTimeLayout = "2006-01-02 15:04:05"

type TagStore interface {
	ListTags(context.Context) ([]*pb.Tag, error)
	ApplyTagSnapshot(context.Context, string, string, []*pb.TagSnapshotItem) error
	// ListTagMembers 读标签成员（含交易状态）；只在快照带交易所原始状态（币安）时用到。
	ListTagMembers(ctx context.Context, spaceID, tagID, status string) ([]*pb.TagMember, error)
}

// RunStore 保存标签的同步运行结果（Collector 自己的库）：调度判断读它，结果写它。
type RunStore interface {
	List(ctx context.Context, spaceID string) ([]domain.TagSyncRun, error)
	Record(ctx context.Context, run domain.TagSyncRun) error
}

type TagRunner struct {
	Store   TagStore
	Runs    RunStore
	Listers Listers
	// IndexListers 用于带 sync.index 的标签：同步指数成分股。
	IndexListers IndexListers
	FetchTimeout time.Duration
	Now          func() time.Time
	Metrics      *Metrics
	// KeepHaltedMembers 表示交易状态检测在管带交易所原始状态的市场（crypto）：已是成员、临时不可交易（BREAK 等）的交易对留在
	// 标签里交给它标成停牌。关掉时快照只留正常交易的，否则这些成员会留在标签里却没人标成停牌、照常被采集。
	KeepHaltedMembers bool
	// RetryDelays 是快照取数失败或校验不通过时在本次同步内重试前的等待（默认 2 秒、4 秒，共 3 次）；
	// 都失败就保持原成员不变并上报失败，绝不用一份残缺的快照更新成员。
	RetryDelays []time.Duration
	// SnapshotScopes 给出主来源快照的标的范围（键是主来源的 ListerKey）：用备用来源时只加入范围内的新标的，
	// 避免备用来源口径更宽（新浪含科创板、北交所，东方财富不含）时成员在两种口径之间来回变。没登记范围的主来源不过滤。
	SnapshotScopes map[ListerKey]func(subjectID string) bool
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
func tagDue(tag *pb.Tag, run *domain.TagSyncRun, now time.Time) (bool, error) {
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
func (r *TagRunner) runsByTag(ctx context.Context) (map[string]*domain.TagSyncRun, error) {
	out := map[string]*domain.TagSyncRun{}
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
	run := domain.TagSyncRun{SpaceID: tag.GetSpaceId(), TagID: tag.GetTagId(), LastRunAt: runAt, LastStatus: domain.TagSyncSuccess}
	if runErr != nil {
		run.LastStatus, run.LastError = domain.TagSyncFailed, runErr.Error()
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
	instruments, err := r.fetchSnapshot(fetchCtx, tag)
	for attempt := 0; err != nil && snapshotRetryable(err) && attempt < len(delays) && fetchCtx.Err() == nil; attempt++ {
		log.WarnContextf(ctx, "tag %s/%s snapshot attempt %d failed, retrying: %v", tag.GetSpaceId(), tag.GetTagId(), attempt+1, err)
		select {
		case <-time.After(delays[attempt]):
		case <-fetchCtx.Done():
		}
		instruments, err = r.fetchSnapshot(fetchCtx, tag)
	}
	if err != nil && tagattr.FallbackSource(tag) != "" && tagattr.Index(tag) == "" {
		instruments, err = r.fallbackSnapshot(ctx, tag, timeout, err)
	}
	subjects := 0
	var items []*pb.TagSnapshotItem
	if err == nil {
		instruments, err = r.keepTradable(ctx, tag, instruments)
	}
	if err == nil {
		items, err = snapshotItems(tag, instruments)
	}
	if err == nil {
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

// keepTradable 处理带交易所原始交易状态的快照（币安）。交易所长期保留已下架的交易对（现货 3751 个里正常交易的只有 1376 个），
// 整份应用会把它们加进标签、并重新激活早被移出的成员，所以只保留：正常交易的，加上（开着 KeepHaltedMembers 时）已是 active
// 成员、而且没被标成退市的非交易交易对。没有原始状态的来源（股票）不适用，原样返回。一个正常交易的都没有时按失败处理、成员不变
// （交易所整体维护或接口异常），读成员失败同样按失败处理，不能退回“只留正常交易的”把停牌成员移出。
func (r *TagRunner) keepTradable(ctx context.Context, tag *pb.Tag, instruments []marketdata.Instrument) ([]marketdata.Instrument, error) {
	raw, trading := false, 0
	for _, item := range instruments {
		if item.TradingStatus != "" {
			raw = true
		}
		if item.TradingStatus == marketdata.BinanceTrading {
			trading++
		}
	}
	if !raw {
		return instruments, nil
	}
	if trading == 0 {
		return nil, fmt.Errorf("%w: tag %s/%s snapshot has no instrument in TRADING", marketdata.ErrProtocol, tag.GetSpaceId(), tag.GetTagId())
	}
	keep := map[string]bool{}
	if r.KeepHaltedMembers && trading < len(instruments) {
		members, err := r.Store.ListTagMembers(ctx, tag.GetSpaceId(), tag.GetTagId(), "active")
		if err != nil {
			return nil, fmt.Errorf("list members of %s/%s: %w", tag.GetSpaceId(), tag.GetTagId(), err)
		}
		for _, member := range members {
			if member.GetTradingStatus() != "delisted" {
				keep[member.GetSubject().GetSubjectId()] = true
			}
		}
	}
	kept := make([]marketdata.Instrument, 0, len(instruments))
	for _, item := range instruments {
		if item.TradingStatus == marketdata.BinanceTrading || keep[item.SubjectID] {
			kept = append(kept, item)
		}
	}
	return kept, nil
}

// fallbackSnapshot 在主来源重试后仍失败时改用 sync.fallback_source 取一次全部标的，超时预算与主来源分开。结果只加不减：
// 备用来源里主来源范围内（SnapshotScopes）的标的，加上当前全部 active 成员。备用来源少的标的（东方财富独有的退市旧代码、
// 新浪还没收录的新股）不会因此移出，等主来源恢复再按它的完整快照增减。备用来源也失败、或读成员失败时返回两边的错误，成员不变。
func (r *TagRunner) fallbackSnapshot(ctx context.Context, tag *pb.Tag, timeout time.Duration, primaryErr error) ([]marketdata.Instrument, error) {
	source := tagattr.FallbackSource(tag)
	log.WarnContextf(ctx, "tag %s/%s primary source %s failed, falling back to %s: %v", tag.GetSpaceId(), tag.GetTagId(), tagattr.Source(tag), source, primaryErr)
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	items, err := FetchSnapshot(fetchCtx, r.Listers, tag.GetSpaceId(), []string{source}, tagattr.MarketType(tag))
	if err != nil {
		return nil, fmt.Errorf("%w; fallback %s: %v", primaryErr, source, err)
	}
	members, err := r.Store.ListTagMembers(ctx, tag.GetSpaceId(), tag.GetTagId(), "active")
	if err != nil {
		return nil, fmt.Errorf("%w; fallback %s: list members: %v", primaryErr, source, err)
	}
	inScope := r.SnapshotScopes[ListerKey{MarketID: tag.GetSpaceId(), Source: tagattr.Source(tag), InstrumentType: tagattr.MarketType(tag)}]
	seen := make(map[string]bool, len(members))
	out := make([]marketdata.Instrument, 0, len(members))
	for _, member := range members {
		id := member.GetSubject().GetSubjectId()
		if id != "" && !seen[id] {
			seen[id] = true
			// 已有标的的名称与属性 Storage 不会改，这里只需要代码。
			out = append(out, marketdata.Instrument{SubjectID: id, Name: member.GetSubject().GetName()})
		}
	}
	added := 0
	for _, item := range items {
		if seen[item.SubjectID] || (inScope != nil && !inScope(item.SubjectID)) {
			continue
		}
		seen[item.SubjectID] = true
		out = append(out, item)
		added++
	}
	log.WarnContextf(ctx, "tag %s/%s used fallback %s: kept %d members, added %d new", tag.GetSpaceId(), tag.GetTagId(), source, len(out)-added, added)
	return out, nil
}

// snapshotRetryable 判断取快照失败后本次同步内是否值得再试：东方财富主机冷却中（主动停手）时请求根本没有发出去，
// 立即重试只会得到同样的错误，也违背冷却的本意，所以不重试，等下一轮调度。
func snapshotRetryable(err error) bool { return !errors.Is(err, cnstock.ErrCoolingDown) }

// fetchSnapshot 取标签的成员快照：配置了 sync.index 时取该指数的成分股（来源是 sync.index_source，不写时等于
// sync.source），否则取 sync.source 的全部标的。
func (r *TagRunner) fetchSnapshot(ctx context.Context, tag *pb.Tag) ([]marketdata.Instrument, error) {
	if index := tagattr.Index(tag); index != "" {
		return FetchIndexSnapshot(ctx, r.IndexListers, tag.GetSpaceId(), tagattr.IndexSource(tag), index)
	}
	return FetchSnapshot(ctx, r.Listers, tag.GetSpaceId(), []string{tagattr.Source(tag)}, tagattr.MarketType(tag))
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

// snapshotItems 把标的快照换成标签成员：属性由市场的标的规则给出，时区取市场登记的时区。
func snapshotItems(tag *pb.Tag, instruments []marketdata.Instrument) ([]*pb.TagSnapshotItem, error) {
	rules, err := markets.RulesFor(tag.GetSpaceId())
	if err != nil {
		return nil, err
	}
	location, err := markets.Location(tag.GetSpaceId())
	if err != nil {
		return nil, err
	}
	items := make([]*pb.TagSnapshotItem, 0, len(instruments))
	for _, item := range instruments {
		items = append(items, &pb.TagSnapshotItem{SubjectId: item.SubjectID, Name: item.Name, Attributes: rules.SubjectAttributes(item), Timezone: location.String()})
	}
	return items, nil
}
