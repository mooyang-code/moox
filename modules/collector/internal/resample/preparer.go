package resample

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

// Preparer asynchronously makes the target catalog ready. Metadata calls are
// intentionally outside the Collector SQLite transaction.
type Preparer struct {
	Tasks        *store.TaskRepository
	Source       subjectSource
	Catalog      *Catalog
	KeepDuration string
	Limit        int
	mu           sync.Mutex
}

func (p *Preparer) RunOnce(ctx context.Context) error {
	if p == nil || p.Tasks == nil || p.Source == nil || p.Catalog == nil {
		return fmt.Errorf("resample preparer dependencies are required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	tasks, err := p.Tasks.ListResampleByPrepareStates(ctx, []domain.CollectionTaskPrepareState{
		domain.PrepareStatePending, domain.PrepareStateWaitingView, domain.PrepareStateError, domain.PrepareStateReady,
	}, limit)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		params, parseErr := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
		if parseErr != nil {
			_ = p.Tasks.SetPrepareState(ctx, task.SpaceID, task.TaskID, domain.PrepareStateError, parseErr.Error())
			continue
		}
		source, sourceErr := p.Source.GetDataset(ctx, task.SpaceID, params.SourceDatasetID)
		if sourceErr == nil {
			subjects, resolveErr := p.Source.ResolveSubjects(ctx, task.SpaceID, source.SubjectTags)
			sourceErr = resolveErr
			if sourceErr == nil {
				sourceErr = p.Catalog.PrepareTarget(ctx, task, params, source, subjects, p.KeepDuration)
			}
		}
		if sourceErr != nil {
			state := domain.PrepareStateError
			if errors.Is(sourceErr, ErrTargetViewNotReady) {
				state = domain.PrepareStateWaitingView
			}
			_ = p.Tasks.SetPrepareState(ctx, task.SpaceID, task.TaskID, state, sourceErr.Error())
			continue
		}
		if err := p.Tasks.SetPrepareState(ctx, task.SpaceID, task.TaskID, domain.PrepareStateReady, ""); err != nil {
			return err
		}
	}
	return nil
}

// Retry marks an error task as pending for an explicit operator retry.
func (p *Preparer) Retry(ctx context.Context, spaceID, taskID string) error {
	if p == nil || p.Tasks == nil {
		return fmt.Errorf("resample preparer is not initialized")
	}
	return p.Tasks.SetPrepareState(ctx, strings.TrimSpace(spaceID), strings.TrimSpace(taskID), domain.PrepareStatePending, "")
}

// Start launches the bounded preparation loop. A timer should still invoke
// RunOnce; the loop is a liveness fallback for tasks created while no timer is
// configured in a development environment.
func (p *Preparer) Start(ctx context.Context, interval time.Duration) (func(), error) {
	if ctx == nil {
		return nil, fmt.Errorf("resample preparer context is required")
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				_ = p.RunOnce(loopCtx)
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

var _ subjectSource = (*storagesource.DatasetSource)(nil)
