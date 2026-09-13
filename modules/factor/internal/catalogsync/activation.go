package catalogsync

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
)

type OwnershipReader interface {
	ListOwned(context.Context) ([]store.OutputManifestKey, error)
}
type OutputCleaner interface {
	ClearFactorOutputs(context.Context, *engine.FactorTask) error
}

// EngineActivation uses the same operation gate as every engine trigger. The
// caller must share that gate; a separate gate would not fence active writers.
func EngineActivation(gate *taskrunner.OperationGate, ownership OwnershipReader, cleaner OutputCleaner) (Activation, error) {
	if gate == nil || ownership == nil || cleaner == nil {
		return nil, fmt.Errorf("engine activation requires execution gate and output ownership cleanup")
	}
	return func(ctx context.Context, previous, next domain.CatalogSnapshot, commit func() error) error {
		release, err := gate.AcquireContext(ctx)
		if err != nil {
			return err
		}
		defer release()
		if err := ctx.Err(); err != nil {
			return err
		}
		factors := map[string]domain.FactorDef{}
		for _, factor := range next.Factors {
			factors[factor.FactorID] = factor
		}
		active := map[string]string{}
		for _, binding := range next.Bindings {
			factor, ok := factors[binding.FactorID]
			if !ok || factor.Status != domain.FactorStatusEnabled || binding.Status != domain.BindingStatusEnabled {
				continue
			}
			if binding.BindingGeneration == "" {
				return fmt.Errorf("binding %q has no incarnation", binding.BindingID)
			}
			active[binding.BindingID] = taskrunner.ExecutionGeneration(taskrunner.TaskScope{
				BindingID: binding.BindingID, BindingGeneration: binding.BindingGeneration, SpaceID: binding.SpaceID,
				SourceViewID: binding.SourceViewID, ResultDatasetID: binding.ResultDatasetID, Freq: binding.Freq,
			}, factor)
		}
		keys, err := ownership.ListOwned(ctx)
		if err != nil {
			return err
		}
		retired := []*engine.FactorTask{}
		for _, key := range keys {
			if key.BindingGeneration != "" && active[key.BindingID] == key.BindingGeneration {
				continue
			}
			var task engine.FactorTask
			if err := json.Unmarshal([]byte(key.CleanupTaskJSON), &task); err != nil {
				return fmt.Errorf("decode retired output ownership: %w", err)
			}
			if task.BindingID != key.BindingID || task.BindingGeneration == "" || task.BindingGeneration != key.BindingGeneration || task.SubjectID != key.SubjectID || task.Freq != key.Frequency || task.PeriodTime != key.PeriodTime.Unix() || task.FilterSourceSeriesTag != key.FilterSourceSeriesTag || task.SourceSeriesTag != key.SourceSeriesTag {
				return fmt.Errorf("retired output ownership does not match manifest")
			}
			retired = append(retired, &task)
		}
		// A failed activation leaves the previous catalog active. A new retry
		// needs a fresh mutation ID in case that catalog wrote outputs again.
		attempt := "catalog-cleanup-" + rand.Text()
		for i, task := range retired {
			task.TaskID, task.TriggerEventID = fmt.Sprintf("%s-%d", attempt, i), attempt
			if err := cleaner.ClearFactorOutputs(ctx, task); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return commit()
	}, nil
}
