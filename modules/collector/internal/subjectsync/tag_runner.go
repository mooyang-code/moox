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

// TagRunner 同步自动标签的成员。每个标签的同步周期在管理台按标签单独设置，存放在 Storage 中。
type TagRunner struct {
	Store        TagStore
	Listers      Listers
	FetchTimeout time.Duration
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

// RunDue 从 Storage 读出全部标签，只同步到期的自动标签。是否到期由标签自己的 cron 和上次运行时间决定，上次运行时间
// 存在 Storage 里，进程重启后不会漏跑。返回本次失败的汇总，失败已经上报给 Storage。
func (r *TagRunner) RunDue(ctx context.Context, now time.Time) error {
	tags, err := r.Store.ListTags(ctx)
	if err != nil {
		return fmt.Errorf("读取标签失败: %w", err)
	}
	now = now.UTC()
	var errs []error
	for _, tag := range tags {
		if r.Metrics != nil {
			r.Metrics.observeTagInventory(tag)
		}
		if !probing(tag) {
			continue
		}
		due, err := tagDue(tag, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("标签 %s/%s 的计划无效: %w", tag.GetSpaceId(), tag.GetTagId(), err))
			continue
		}
		if !due {
			continue
		}
		if err := r.runTag(ctx, tag, now); err != nil {
			errs = append(errs, fmt.Errorf("标签 %s/%s 同步失败: %w", tag.GetSpaceId(), tag.GetTagId(), err))
		}
	}
	return errors.Join(errs...)
}

func (r *TagRunner) runTag(ctx context.Context, tag *pb.Tag, runAt time.Time) error {
	timeout := r.FetchTimeout
	if timeout <= 0 {
		timeout = DefaultFetchTimeout
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	instruments, err := FetchSnapshot(fetchCtx, r.Listers, []string{tag.GetSource()}, tag.GetMarketType())
	if err == nil {
		items := make([]*pb.TagSnapshotItem, 0, len(instruments))
		for _, item := range instruments {
			items = append(items, snapshotItem(tag, item))
		}
		log.InfoContextf(ctx, "tag %s/%s applying snapshot subjects=%d", tag.GetSpaceId(), tag.GetTagId(), len(items))
		err = r.Store.ApplyTagSnapshot(ctx, tag.GetSpaceId(), tag.GetTagId(), runAt, items)
	}
	if err != nil {
		log.WarnContextf(ctx, "tag %s/%s run failed: %v", tag.GetSpaceId(), tag.GetTagId(), err)
		if r.Metrics != nil {
			r.Metrics.observeTagFailure(tag)
		}
		if reportErr := r.Store.ReportTagRunFailure(ctx, tag.GetSpaceId(), tag.GetTagId(), runAt, err.Error()); reportErr != nil {
			log.ErrorContextf(ctx, "report tag %s/%s failure: %v", tag.GetSpaceId(), tag.GetTagId(), reportErr)
			return errors.Join(err, fmt.Errorf("向 Storage 上报失败时出错: %w", reportErr))
		}
		return err
	}
	if r.Metrics != nil {
		r.Metrics.observeTagSuccess(tag, runAt)
	}
	return nil
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
