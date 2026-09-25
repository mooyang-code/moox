package subjectsync

import (
	"context"
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
	// Auto tags always fetch their authoritative member snapshot. Manual tags
	// only fetch when sources and instrument_type are both configured, which is
	// the optional validity-probe mode.
	return len(tag.GetSources()) > 0 && tag.GetInstrumentType() != "" &&
		(strings.EqualFold(tag.GetMode(), "auto") || strings.EqualFold(tag.GetMode(), "manual"))
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

func (r *TagRunner) RunOnce(ctx context.Context) {
	tags, err := r.Store.ListTags(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "list tags: %v", err)
		return
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	for _, tag := range tags {
		if r.Metrics != nil {
			r.Metrics.observeTagInventory(tag)
		}
		if !probing(tag) {
			continue
		}
		due, err := tagDue(tag, now)
		if err != nil {
			log.ErrorContextf(ctx, "tag %s/%s schedule: %v", tag.GetSpaceId(), tag.GetTagId(), err)
			continue
		}
		if due {
			r.runTag(ctx, tag, now)
		}
	}
}

func (r *TagRunner) runTag(ctx context.Context, tag *pb.Tag, runAt time.Time) {
	timeout := r.FetchTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	instruments, err := FetchSnapshot(fetchCtx, r.Listers, tag.GetSources(), tag.GetInstrumentType())
	if err == nil {
		items := make([]*pb.TagSnapshotItem, 0, len(instruments))
		for _, item := range instruments {
			items = append(items, snapshotItem(tag, item))
		}
		err = r.Store.ApplyTagSnapshot(ctx, tag.GetSpaceId(), tag.GetTagId(), runAt, items)
	}
	if err != nil {
		log.WarnContextf(ctx, "tag %s/%s run failed: %v", tag.GetSpaceId(), tag.GetTagId(), err)
		if r.Metrics != nil {
			r.Metrics.observeTagFailure(tag)
		}
		if reportErr := r.Store.ReportTagRunFailure(ctx, tag.GetSpaceId(), tag.GetTagId(), runAt, err.Error()); reportErr != nil {
			log.ErrorContextf(ctx, "report tag %s/%s failure: %v", tag.GetSpaceId(), tag.GetTagId(), reportErr)
		}
		return
	}
	if r.Metrics != nil {
		r.Metrics.observeTagSuccess(tag, runAt)
	}
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
