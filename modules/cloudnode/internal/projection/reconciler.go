package projection

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	"trpc.group/trpc-go/trpc-go/log"
)

// EnqueueReconciler republishes projection rows that failed before reaching JetStream.
type EnqueueReconciler struct {
	repo     *Repository
	queue    jobqueue.ExecutionQueue
	interval time.Duration
	limit    int
}

func NewEnqueueReconciler(repo *Repository, queue jobqueue.ExecutionQueue, interval time.Duration, limit int) *EnqueueReconciler {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	if limit <= 0 {
		limit = 100
	}
	return &EnqueueReconciler{repo: repo, queue: queue, interval: interval, limit: limit}
}

func (r *EnqueueReconciler) Run(ctx context.Context) {
	if r == nil || r.repo == nil || r.queue == nil {
		return
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil {
				log.WarnContextf(ctx, "[CloudNode] enqueue reconciler failed: %v", err)
			}
		}
	}
}

func (r *EnqueueReconciler) RunOnce(ctx context.Context) error {
	items, err := r.repo.ListEnqueueRetryCandidates(ctx, r.limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		pub, err := r.queue.Publish(ctx, item)
		if err != nil {
			_ = r.repo.MarkEnqueueFailed(ctx, item.GetSpaceId(), item.GetJobItemId(), err.Error())
			continue
		}
		if err := r.repo.MarkPublished(ctx, item.GetSpaceId(), item.GetJobItemId(), QueueMeta{
			Subject:   pub.Subject,
			Stream:    pub.Stream,
			StreamSeq: pub.Sequence,
		}); err != nil {
			return err
		}
	}
	return nil
}
