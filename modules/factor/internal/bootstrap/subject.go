package bootstrap

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
)

// StartEngineSubject starts only after catalog activation. The caller must
// close the returned consumer before closing its task runner or SQLite store.
func StartEngineSubject(ctx context.Context, cfg *EngineApplicationConfig, replica *store.Store, runner trigger.CombinationTaskRunner, gate *taskrunner.OperationGate) (*eventconsumer.Consumer, error) {
	if cfg == nil || replica == nil || runner == nil || gate == nil || len(cfg.EventBus.URLs) == 0 {
		return nil, fmt.Errorf("subject runtime requires engine config, activated replica, runner and shared gate")
	}
	snapshot, err := replica.CatalogSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot == nil || snapshot.Revision <= 0 {
		return nil, fmt.Errorf("subject runtime requires an activated catalog")
	}
	commit, err := trigger.NewSubjectReceiptCommit(replica)
	if err != nil {
		return nil, err
	}
	executor, err := trigger.NewSubjectRunner(replica, replica, runner, gate, commit, cfg.Engine.FactorsDir)
	if err != nil {
		return nil, err
	}
	consumer, err := eventconsumer.NewSubject(eventconsumer.Config{
		URLs: append([]string(nil), cfg.EventBus.URLs...), CredentialFile: cfg.EventBus.CredentialFile,
		FetchMaxWait: cfg.EventBus.FetchMaxWait, ExecutionTimeout: cfg.EventBus.ExecutionTimeout,
		StallThreshold: cfg.EventBus.StallThreshold, MaxExecutionAttempts: cfg.EventBus.MaxExecutionAttempts,
	}, cfg.SubjectBatch, executor.Execute)
	if err != nil {
		return nil, err
	}
	if err := consumer.Start(ctx); err != nil {
		_ = consumer.Close()
		return nil, err
	}
	return consumer, nil
}
