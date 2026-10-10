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

func (r *AttributeRunner) RunOnce(ctx context.Context) error {
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
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
