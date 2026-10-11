package subjectsync

import (
	"context"
	"errors"
	"fmt"
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
	ApplyTagSnapshot(context.Context, string, string, time.Time, []*pb.TagSnapshotItem) error
	ReportTagRunFailure(context.Context, string, string, time.Time, string) error
}

type TagRunner struct {
	Store        TagStore
	Listers      Listers
	FetchTimeout time.Duration
	Now          func() time.Time
	Metrics      *Metrics
}

func probing(tag *pb.Tag) bool {
	if tag == nil {
		return false
	}
	// Only auto tags synchronize their authoritative member snapshot. Manual
	// tags still carry source/market identity, but membership remains operator-owned.
	return strings.EqualFold(tag.GetMode(), "auto") && tag.GetSource() != "" && tag.GetMarketType() != ""
}

func parseTagTime(value string) (time.Time, error) {
	if parsed, err := time.ParseInLocation(sqliteTimeLayout, value, time.UTC); err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, value)
}

func tagDue(tag *pb.Tag, now time.Time) (bool, error) {
	if tag == nil || tag.GetLastRunAt() == "" {
		return true, nil
	}
	lastRun, err := parseTagTime(tag.GetLastRunAt())
	if err != nil {
		return false, fmt.Errorf("parse last_run_at: %w", err)
	}
	if updated, err := parseTagTime(tag.GetUpdatedAt()); err == nil && updated.After(lastRun) {
		return true, nil
	}
	schedule, err := cron.ParseStandard(tag.GetCron())
	if err != nil {
		return false, err
	}
	loc, err := time.LoadLocation(tag.GetTimezone())
	if err != nil {
		return false, err
	}
	return !schedule.Next(lastRun.In(loc)).After(now), nil
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
	var failures error
	for _, tag := range tags {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		if r.Metrics != nil {
			r.Metrics.observeTagInventory(tag)
		}
		if !probing(tag) {
			continue
		}
		due, err := tagDue(tag, now)
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

func (r *TagRunner) runTag(ctx context.Context, tag *pb.Tag, runAt time.Time) (int, error) {
	timeout := r.FetchTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	instruments, err := FetchSnapshot(fetchCtx, r.Listers, []string{tag.GetSource()}, tag.GetMarketType())
	subjects := 0
	if err == nil {
		items := make([]*pb.TagSnapshotItem, 0, len(instruments))
		for _, item := range instruments {
			items = append(items, snapshotItem(tag, item))
		}
		log.InfoContextf(ctx, "tag %s/%s applying snapshot subjects=%d", tag.GetSpaceId(), tag.GetTagId(), len(items))
		err = r.Store.ApplyTagSnapshot(ctx, tag.GetSpaceId(), tag.GetTagId(), runAt, items)
		subjects = len(items)
	}
	if err != nil {
		log.WarnContextf(ctx, "tag %s/%s run failed: %v", tag.GetSpaceId(), tag.GetTagId(), err)
		if r.Metrics != nil {
			r.Metrics.observeTagFailure(tag)
		}
		if reportErr := r.Store.ReportTagRunFailure(ctx, tag.GetSpaceId(), tag.GetTagId(), runAt, err.Error()); reportErr != nil {
			log.ErrorContextf(ctx, "report tag %s/%s failure: %v", tag.GetSpaceId(), tag.GetTagId(), reportErr)
			err = errors.Join(err, reportErr)
		}
		return 0, err
	}
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
	out := &pb.TagSnapshotItem{SubjectId: item.SubjectID, Name: item.Name}
	switch tag.GetSpaceId() {
	case "crypto":
		out.SubjectType, out.Market, out.Currency, out.Timezone = "crypto_pair", "CRYPTO", strings.ToUpper(item.QuoteAsset), "UTC"
	case "stockcn":
		out.SubjectType, out.Market, out.Currency, out.Timezone = "stock", item.Exchange, "CNY", "Asia/Shanghai"
	}
	return out
}
