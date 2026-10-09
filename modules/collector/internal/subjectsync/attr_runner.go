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

// AttributeRunner 按任务计划刷新标的属性。
type AttributeRunner struct {
	Store        AttributeStore
	Listers      Listers
	Jobs         []AttributeJob
	FetchTimeout time.Duration
	Metrics      *Metrics
}

// attributeWindow 是判断任务是否到点的区间长度，与属性同步定时器的触发间隔相同。
const attributeWindow = time.Minute

// RunDue 执行在 (now-1分钟, now] 内有计划时间的任务。判断不依赖任何状态：定时器每分钟触发一次，每个计划时间恰好
// 落在一次触发的区间里，所以只执行一次；计划带时区，执行时间与主机时区无关。错过的计划（例如恰好在这时重启）不补跑。
func (r *AttributeRunner) RunDue(ctx context.Context, now time.Time) error {
	var errs []error
	for _, job := range r.Jobs {
		due, err := attributeJobDue(job, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("属性同步任务 %s 的计划无效: %w", job.SpaceID, err))
			continue
		}
		if !due {
			continue
		}
		if err := r.runJob(ctx, job); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func attributeJobDue(job AttributeJob, now time.Time) (bool, error) {
	schedule, err := cron.ParseStandard(job.Cron)
	if err != nil {
		return false, err
	}
	loc, err := time.LoadLocation(job.Timezone)
	if err != nil {
		return false, err
	}
	return !schedule.Next(now.Add(-attributeWindow).In(loc)).After(now), nil
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
		timeout = DefaultFetchTimeout
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	instruments, err := FetchSnapshot(fetchCtx, r.Listers, job.Sources, instrumentType)
	if err == nil {
		items := make([]*pb.SubjectAttributes, 0, len(instruments))
		for _, instrument := range instruments {
			items = append(items, &pb.SubjectAttributes{SubjectId: instrument.SubjectID, Name: instrument.Name, Attributes: attributes(job.SpaceID, instrument)})
		}
		_, _, err = r.Store.UpdateSubjectAttributes(ctx, job.SpaceID, items)
	}
	if err != nil {
		log.WarnContextf(ctx, "attribute job %s failed: %v", job.SpaceID, err)
		if r.Metrics != nil {
			r.Metrics.observeAttributeFailure(job.SpaceID)
		}
		return fmt.Errorf("属性同步任务 %s 失败: %w", job.SpaceID, err)
	}
	log.InfoContextf(ctx, "attribute job %s updated subjects=%d", job.SpaceID, len(instruments))
	return nil
}

func attributes(spaceID string, item marketdata.Instrument) map[string]string {
	switch spaceID {
	case "crypto":
		return map[string]string{"base": strings.ToUpper(item.BaseAsset), "quote": strings.ToUpper(item.QuoteAsset)}
	case "stockcn":
		return map[string]string{"exchange": item.Exchange}
	default:
		return nil
	}
}
