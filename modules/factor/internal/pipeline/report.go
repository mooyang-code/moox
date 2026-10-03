package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

func outcomeFor(plan Plan, loaded LoadResult, computation Computation) Outcome {
	outcome := Outcome{
		Status:         "complete",
		FailedSubjects: uniqueSorted(append(append([]string(nil), plan.UpstreamFailed...), loaded.FailedSubjects...)),
		StageDurations: make(map[string]time.Duration),
	}
	for _, factor := range plan.Factors {
		state, ok := computation.FactorStates[factor.FactorID]
		if !ok {
			state = storageio.FactorState{FactorID: factor.FactorID, Status: "degraded", SourceHash: factor.SourceHash}
		}
		state.FailedSubjects = uniqueSorted(state.FailedSubjects)
		outcome.Factors = append(outcome.Factors, state)
		if state.Status != "complete" {
			outcome.Status = "degraded"
		}
	}
	if len(outcome.FailedSubjects) > 0 {
		outcome.Status = "degraded"
	}
	if computation.BudgetHit {
		outcome.Status = "degraded"
	}
	sort.Slice(outcome.Factors, func(i, j int) bool { return outcome.Factors[i].FactorID < outcome.Factors[j].FactorID })
	return outcome
}

func (r *Runner) Report(ctx context.Context, plan Plan, outcome Outcome) error {
	if plan.Mode != ModeLive {
		return nil
	}
	if r == nil || r.store == nil || r.clock == nil {
		return fmt.Errorf("factor Storage store and period clock are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	target, err := planTarget(r.clock, plan)
	if err != nil {
		return err
	}
	marker := storageio.PeriodMarker{
		SpaceID: plan.Set.SpaceID, ResultDatasetID: plan.Set.ResultDatasetID,
		SourceDatasetID: plan.Set.SourceDatasetID, Frequency: plan.Set.Freq,
		PeriodTime: target.Unix(), Status: outcome.Status,
		UniverseSubjects: uniqueSorted(plan.Expected), FailedSubjects: uniqueSorted(outcome.FailedSubjects),
		Factors:        append([]storageio.FactorState(nil), outcome.Factors...),
		TriggerEventID: plan.TriggerEventID, ComputedAt: time.Now().UTC(),
	}
	if err := r.store.ReportComputed(ctx, marker); err != nil {
		return fmt.Errorf("%w: report factor period computed: %v", storageio.ErrInfra, err)
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, plan Plan) (Outcome, error) {
	if r == nil || r.store == nil || r.clock == nil {
		return Outcome{}, errors.New("factor pipeline requires Storage and period clock")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	outcome := Outcome{StageDurations: make(map[string]time.Duration)}
	planStarted := time.Now()
	if plan.Mode != ModeLive && plan.Mode != ModeRecalc {
		return Outcome{}, fmt.Errorf("unknown factor pipeline mode %d", plan.Mode)
	}
	if _, err := planTarget(r.clock, plan); err != nil {
		return Outcome{}, err
	}
	outcome.StageDurations["plan"] = time.Since(planStarted)
	loaded := LoadResult{Frames: map[string]*storageio.Frame{}, Available: append([]string(nil), plan.Available...)}
	computation := Computation{Results: map[string]map[string]pyexec.ItemResult{}, FactorStates: map[string]storageio.FactorState{}}
	for _, factor := range plan.Factors {
		computation.FactorStates[factor.FactorID] = storageio.FactorState{
			FactorID: factor.FactorID, Status: "complete", SourceHash: factor.SourceHash,
		}
	}
	rowsWritten := 0
	if len(plan.Factors) > 0 {
		stage := time.Now()
		var err error
		loaded, err = r.Load(ctx, plan)
		outcome.StageDurations["load"] = time.Since(stage)
		if err != nil {
			return Outcome{StageDurations: outcome.StageDurations}, err
		}
		stage = time.Now()
		computation, err = r.Compute(ctx, plan, loaded)
		outcome.StageDurations["compute"] = time.Since(stage)
		if err != nil {
			return Outcome{StageDurations: outcome.StageDurations}, err
		}
		stage = time.Now()
		rows, assemblyErr := r.Assemble(plan, loaded, computation)
		outcome.StageDurations["assemble"] = time.Since(stage)
		if assemblyErr != nil {
			return Outcome{StageDurations: outcome.StageDurations}, assemblyErr
		}
		stage = time.Now()
		target, targetErr := planTarget(r.clock, plan)
		if targetErr != nil {
			return Outcome{StageDurations: outcome.StageDurations}, targetErr
		}
		if writeErr := r.Write(ctx, plan, target, rows, r.cfg.WriteBatchRows); writeErr != nil {
			return Outcome{StageDurations: outcome.StageDurations}, writeErr
		}
		rowsWritten = len(rows)
		outcome.StageDurations["write"] = time.Since(stage)
	} else {
		outcome.StageDurations["load"] = 0
		outcome.StageDurations["compute"] = 0
		outcome.StageDurations["assemble"] = 0
		outcome.StageDurations["write"] = 0
	}
	outcome = mergeOutcomeDurations(outcomeFor(plan, loaded, computation), outcome)
	outcome.RowsWritten = rowsWritten
	if len(plan.Factors) == 0 {
		outcome.Status = "complete"
	}
	stage := time.Now()
	if err := r.Report(ctx, plan, outcome); err != nil {
		return outcome, err
	}
	outcome.StageDurations["report"] = time.Since(stage)
	return outcome, nil
}

func mergeOutcomeDurations(outcome, previous Outcome) Outcome {
	for name, duration := range previous.StageDurations {
		outcome.StageDurations[name] = duration
	}
	return outcome
}
