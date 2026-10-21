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

type AttributeStore interface {
	UpdateSubjectAttributes(context.Context, string, []*pb.SubjectAttributes) (int, int, error)
}

type AttributeRunner struct {
	Store        AttributeStore
	Listers      Listers
	Jobs         []AttributeJob
	FetchTimeout time.Duration
	Now          func() time.Time
	Metrics      *Metrics
}

// Current 返回调度判断使用的当前时间。
func (r *AttributeRunner) Current() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *AttributeRunner) RunOnce(ctx context.Context) error {
	return r.RunOnceAt(ctx, r.Current())
}

// RunOnceAt 以给定的触发时间判断任务是否到期。定时回调应在等待共享同步锁之前固定触发时间，
// 否则锁被手动同步占用跨过计划分钟时，获得锁后再取时间会让当次任务落在窗口之外被跳过。
func (r *AttributeRunner) RunOnceAt(ctx context.Context, now time.Time) error {
	now = now.UTC()
	var failures error
	for _, job := range r.Jobs {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		schedule, err := cron.ParseStandard(job.Cron)
		if err != nil {
			log.ErrorContextf(ctx, "attribute job %s cron: %v", job.SpaceID, err)
			failures = errors.Join(failures, err)
			continue
		}
		loc, err := time.LoadLocation(job.Timezone)
		if err != nil {
			log.ErrorContextf(ctx, "attribute job %s timezone: %v", job.SpaceID, err)
			failures = errors.Join(failures, err)
			continue
		}
		// Adjacent windows (trigger - 1m, trigger] do not overlap. Scheduling
		// depends on the job timezone, independently of process uptime.
		next := schedule.Next(now.Add(-time.Minute).In(loc))
		if next.IsZero() {
			failures = errors.Join(failures, fmt.Errorf("attribute job %s has no scheduled occurrence", job.SpaceID))
			continue
		}
		if next.After(now) {
			continue
		}
		failures = errors.Join(failures, r.runJob(ctx, job))
	}
	return failures
}

func (r *AttributeRunner) runJob(ctx context.Context, job AttributeJob) error {
	instrumentType := job.InstrumentType
	if instrumentType == "" {
		if job.SpaceID == "crypto" {
			instrumentType = "spot"
		} else if job.SpaceID == "stockcn" {
			instrumentType = "equity"
		}
	}
	timeout := r.FetchTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	instruments, err := FetchSnapshot(fetchCtx, r.Listers, job.Sources, instrumentType)
	if err != nil {
		log.WarnContextf(ctx, "attribute job %s failed: %v", job.SpaceID, err)
		if r.Metrics != nil {
			r.Metrics.observeAttributeFailure(job.SpaceID)
		}
		return err
	}
	items := make([]*pb.SubjectAttributes, 0, len(instruments))
	for _, instrument := range instruments {
		items = append(items, &pb.SubjectAttributes{SubjectId: instrument.SubjectID, Name: instrument.Name, Attributes: attributes(job.SpaceID, instrument)})
	}
	if _, _, err := r.Store.UpdateSubjectAttributes(ctx, job.SpaceID, items); err != nil {
		log.WarnContextf(ctx, "update subject attributes %s failed: %v", job.SpaceID, err)
		if r.Metrics != nil {
			r.Metrics.observeAttributeFailure(job.SpaceID)
		}
		return fmt.Errorf("update subject attributes %s: %w", job.SpaceID, err)
	}
	return nil
}

// SubjectPairAttribute 是标的属性里记录交易所交易对的键，采集计划据此给 WS 推送带出 pair。
const SubjectPairAttribute = "pair"

func attributes(spaceID string, item marketdata.Instrument) map[string]string {
	switch spaceID {
	case "crypto":
		values := map[string]string{"base": strings.ToUpper(item.BaseAsset), "quote": strings.ToUpper(item.QuoteAsset)}
		// 永续合约的交易对：WS 连续合约 K 线按它订阅（目前 USDT 永续与 symbol 相同）。
		if pair := strings.ToUpper(strings.TrimSpace(item.Pair)); pair != "" {
			values[SubjectPairAttribute] = pair
		}
		return values
	case "stockcn":
		return map[string]string{"exchange": item.Exchange}
	default:
		return nil
	}
}

// SyncAttributes refreshes the matching attribute jobs right now, ignoring
// their cron windows. An empty filter space matches every job.
func (r *AttributeRunner) SyncAttributes(ctx context.Context, filter Filter) []AttributeResult {
	var results []AttributeResult
	for _, job := range r.Jobs {
		if filter.SpaceID != "" && filter.SpaceID != job.SpaceID {
			continue
		}
		result := AttributeResult{SpaceID: job.SpaceID, Status: StatusSuccess}
		if err := ctx.Err(); err != nil {
			result.Status, result.Message = StatusFailed, err.Error()
		} else if err := r.runJob(ctx, job); err != nil {
			result.Status, result.Message = StatusFailed, err.Error()
		}
		results = append(results, result)
	}
	return results
}
