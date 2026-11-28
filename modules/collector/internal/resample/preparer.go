package resample

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

// Preparer asynchronously makes the target catalog ready. Metadata calls are
// intentionally outside the Collector SQLite transaction.
type Preparer struct {
	Tasks   *store.TaskRepository
	Source  subjectSource
	Catalog *Catalog
	Limit   int
	mu      sync.Mutex
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
	// 只准备尚未就绪（pending / waiting_view / error）的重采样任务；已 Ready 任务目标数据集与列均已就绪，不再每分钟重复打元数据与视图 RPC。
	tasks, err := p.Tasks.ListResampleByPrepareStates(ctx, []domain.CollectionTaskPrepareState{
		domain.PrepareStatePending, domain.PrepareStateWaitingView, domain.PrepareStateError,
	}, limit)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		params, parseErr := domain.ParseCollectParams(task.CollectParams, task.DataType)
		if parseErr != nil {
			_ = p.Tasks.SetPrepareState(ctx, task.SpaceID, task.TaskID, domain.PrepareStateError, parseErr.Error())
			continue
		}
		source, sourceErr := p.Source.GetDataset(ctx, task.SpaceID, params.SourceDatasetID)
		if sourceErr == nil {
			subjects, resolveErr := p.Source.ResolveSubjects(ctx, task.SpaceID, source.SubjectTags)
			sourceErr = resolveErr
			if sourceErr == nil {
				sourceErr = p.Catalog.PrepareTarget(ctx, task, params, source, subjects)
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

var _ subjectSource = (*storagesource.DatasetSource)(nil)
