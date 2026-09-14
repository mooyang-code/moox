package trigger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
)

// LocalRecalcExecutor runs frozen-scope recalc jobs on the engine. It never
// publishes FactorPeriodComputed; job status is the independent completion.
type LocalRecalcExecutor struct {
	bindings      PeriodBindingSource
	factors       PeriodFactorSource
	runner        CombinationTaskRunner
	factorsDir    string
	operationGate *taskrunner.OperationGate
}

func NewLocalRecalcExecutor(bindings PeriodBindingSource, factors PeriodFactorSource, runner CombinationTaskRunner, factorsDir string, gate *taskrunner.OperationGate) *LocalRecalcExecutor {
	return &LocalRecalcExecutor{bindings: bindings, factors: factors, runner: runner, factorsDir: factorsDir, operationGate: gate}
}

func (e *LocalRecalcExecutor) Run(ctx context.Context, job RecalcJob) error {
	if e == nil || e.runner == nil || e.bindings == nil || e.factors == nil {
		return ErrRecalcEngineOffline
	}
	if e.operationGate != nil {
		release, err := e.operationGate.AcquireContext(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	bindings, err := e.bindings.ListExecutable(ctx)
	if err != nil {
		return err
	}
	var selected *domain.FactorBinding
	for index := range bindings {
		binding := bindings[index]
		if job.BindingID != "" && binding.BindingID != job.BindingID {
			continue
		}
		if binding.SpaceID != job.SpaceID || binding.Freq != job.Frequency {
			continue
		}
		source := strings.TrimSpace(binding.SourceViewID)
		if source == "" {
			source = strings.TrimSpace(binding.SourceDataset)
		}
		if source != job.SourceViewID {
			continue
		}
		if job.FactorID != "" && binding.FactorID != job.FactorID {
			continue
		}
		if !domain.BindingAllowsSubject(binding, job.SubjectID) {
			continue
		}
		copied := binding
		selected = &copied
		break
	}
	if selected == nil {
		return fmt.Errorf("stale binding generation")
	}
	if job.BindingGeneration != "" && selected.BindingGeneration != job.BindingGeneration {
		return fmt.Errorf("stale binding generation")
	}
	factor, err := e.factors.Get(ctx, firstNonEmpty(job.FactorID, selected.FactorID))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRecalcMissingInput, err)
	}
	start := time.Unix(job.StartTime, 0).UTC()
	end := time.Unix(job.EndTime, 0).UTC()
	for period := start; period.Before(end); {
		next, nextErr := domain.NextPeriod(period, job.Frequency)
		if nextErr != nil {
			return nextErr
		}
		task, buildErr := taskrunner.BuildTask(taskrunner.TaskScope{
			TaskID:            fmt.Sprintf("recalc-%s-%d", job.JobID, period.Unix()),
			BindingID:         selected.BindingID,
			BindingGeneration: selected.BindingGeneration,
			TriggerType:       "recalc",
			SpaceID:           job.SpaceID,
			SourceViewID:      job.SourceViewID,
			ResultDatasetID:   firstNonEmpty(job.DatasetID, selected.ResultDatasetID),
			SubjectID:         job.SubjectID,
			Freq:              job.Frequency,
			PeriodTime:        period.Unix(),
			TriggerEventID:    fmt.Sprintf("recalc-%s-%d", job.JobID, period.Unix()),
			TriggeredAt:       time.Now().UTC(),
			StartTime:         period,
			EndTime:           next,
			ConfigSnapshotID:  job.JobID,
		}, *factor, e.factorsDir)
		if buildErr != nil {
			return classifyRecalcRunError(buildErr)
		}
		results := e.runner.RunAll(ctx, []taskrunner.Task{task})
		if len(results) == 0 {
			return fmt.Errorf("recalc produced no task result")
		}
		if results[0].Err != nil {
			return classifyRecalcRunError(results[0].Err)
		}
		period = next
	}
	return nil
}

func classifyRecalcRunError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrRecalcMissingInput) || errors.Is(err, ErrRecalcViewWaiting) || errors.Is(err, ErrRecalcEngineOffline) {
		return err
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "missing"), strings.Contains(message, "not found"), strings.Contains(message, "no such"):
		return fmt.Errorf("%w: %s", ErrRecalcMissingInput, err.Error())
	case strings.Contains(message, "waiting for view"), strings.Contains(message, "view application"):
		return fmt.Errorf("%w: %s", ErrRecalcViewWaiting, err.Error())
	default:
		return err
	}
}
