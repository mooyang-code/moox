package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
)

// StartEngineViewReady consumes MergePeriodCompleted-associated ViewDataReady for cross-section factors.
func StartEngineViewReady(
	ctx context.Context,
	cfg *EngineApplicationConfig,
	db *store.Store,
	runner trigger.CombinationTaskRunner,
	storage *storageio.Client,
	gate *taskrunner.OperationGate,
	barrier *trigger.PeriodBarrier,
) (*eventconsumer.Consumer, error) {
	if cfg == nil || db == nil || runner == nil || storage == nil {
		return nil, fmt.Errorf("engine View-ready consumer dependencies are required")
	}
	if len(cfg.EventBus.URLs) == 0 {
		return nil, nil
	}
	executor := trigger.NewViewReadyRunner(db.Bindings(), db.Factors(), runner, storage, cfg.Engine.FactorsDir,
		trigger.WithOperationGate(gate),
		trigger.WithExecutionUnitTimeout(time.Duration(cfg.Engine.TaskTimeoutMS)*time.Millisecond),
		trigger.WithExecutionParallelism(cfg.Engine.PythonWorkers),
		trigger.WithBatchExecution(cfg.Engine.BatchEnabled),
		trigger.WithPeriodBarrier(barrier),
		trigger.WithCatalogStore(db),
	)
	consumer := eventconsumer.New(eventconsumer.Config{
		URLs: cfg.EventBus.URLs, FetchMaxWait: cfg.EventBus.FetchMaxWait,
		CredentialFile: cfg.EventBus.CredentialFile, ExecutionTimeout: cfg.EventBus.ExecutionTimeout,
		StallThreshold: cfg.EventBus.StallThreshold, MaxExecutionAttempts: cfg.EventBus.MaxExecutionAttempts,
	}, executor)
	if err := consumer.Start(ctx); err != nil {
		return nil, err
	}
	return consumer, nil
}
