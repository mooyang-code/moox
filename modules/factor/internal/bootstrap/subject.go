package bootstrap

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
	"github.com/mooyang-code/moox/packages/events"
)

// StartEngineSubject consumes DatasetRowsUpserted input commits for bound mdatasets.
func StartEngineSubject(ctx context.Context, cfg *EngineApplicationConfig, db *store.Store, runner trigger.CombinationTaskRunner, _ *taskrunner.OperationGate, barrier *trigger.PeriodBarrier) (*eventconsumer.RowsConsumer, error) {
	if cfg == nil || db == nil || runner == nil {
		return nil, fmt.Errorf("engine subject consumer dependencies are required")
	}
	if len(cfg.EventBus.URLs) == 0 {
		return nil, nil
	}
	handler := trigger.NewDatasetRowsRunner(db.Bindings(), db.Factors(), runner, db, cfg.Engine.FactorsDir).WithPeriodBarrier(barrier)
	filters, err := boundDatasetRowFilters(ctx, db)
	if err != nil {
		return nil, err
	}
	return eventconsumer.StartDatasetRows(ctx, eventconsumer.Config{
		URLs: cfg.EventBus.URLs, FetchMaxWait: cfg.EventBus.FetchMaxWait,
		CredentialFile: cfg.EventBus.CredentialFile,
	}, handler, filters)
}

func boundDatasetRowFilters(ctx context.Context, db *store.Store) ([]string, error) {
	if db == nil || db.Bindings() == nil {
		return nil, nil
	}
	bindings, err := db.Bindings().ListExecutable(ctx)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return nil, nil
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var filters []string
	for _, binding := range bindings {
		datasetID := binding.ResultDatasetID
		if datasetID == "" {
			datasetID = binding.SourceViewID
		}
		if binding.SpaceID == "" || datasetID == "" {
			continue
		}
		subject, err := registry.RenderSubject(events.DatasetRowsUpserted, binding.SpaceID, datasetID)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[subject]; ok {
			continue
		}
		seen[subject] = struct{}{}
		filters = append(filters, subject)
	}
	return filters, nil
}
