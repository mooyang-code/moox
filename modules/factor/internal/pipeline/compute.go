package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

type computeTask struct {
	subject string
	cross   bool
	factors []domain.FactorDef
	target  time.Time
}

func (r *Runner) Compute(ctx context.Context, plan Plan, loaded LoadResult) (Computation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil || r.clock == nil {
		return Computation{}, errors.New("factor period clock is required")
	}
	if len(plan.Factors) == 0 {
		return Computation{Results: map[string]map[string]pyexec.ItemResult{}, FactorStates: map[string]storageio.FactorState{}}, nil
	}
	target, err := planTarget(r.clock, plan)
	if err != nil {
		return Computation{}, err
	}
	freqDuration, err := r.clock.Duration(plan.Set.Freq)
	if err != nil {
		return Computation{}, err
	}
	targets := []time.Time{target}
	if plan.Mode == ModeRecalc {
		targets, err = periodRange(r.clock, plan.TargetStart, plan.TargetEnd, plan.Set.Freq)
		if err != nil {
			return Computation{}, err
		}
	}
	available := intersectLists(plan.Expected, loaded.Available)
	missing := subjectDifference(plan.Expected, available)
	missing = uniqueSorted(append(missing, loaded.FailedSubjects...))
	computation := Computation{
		Results:      make(map[string]map[string]pyexec.ItemResult, len(plan.Factors)),
		FactorStates: make(map[string]storageio.FactorState, len(plan.Factors)),
	}
	statesMu := sync.Mutex{}
	for _, factor := range plan.Factors {
		status := "complete"
		failed := append([]string(nil), missing...)
		if factor.FactorType == domain.FactorTypeCrossSection && len(missing) > 0 && !factor.AllowPartialUniverse {
			status = "skipped"
		}
		if factor.FactorType == domain.FactorTypeCrossSection && len(available) == 0 {
			status = "skipped"
		}
		if len(failed) > 0 && status != "skipped" {
			status = "degraded"
		}
		computation.Results[factor.FactorID] = make(map[string]pyexec.ItemResult)
		computation.FactorStates[factor.FactorID] = storageio.FactorState{
			FactorID: factor.FactorID, Status: status, FailedSubjects: failed, SourceHash: factor.SourceHash,
		}
	}
	addResult := func(factorID, key string, item pyexec.ItemResult) {
		statesMu.Lock()
		defer statesMu.Unlock()
		if _, ok := computation.Results[factorID]; !ok {
			computation.Results[factorID] = make(map[string]pyexec.ItemResult)
		}
		if previous, exists := computation.Results[factorID][key]; exists && previous.Err == nil && item.Err == nil {
			if len(previous.Columns) > 0 && len(item.Columns) > 0 && !equalStrings(previous.Columns, item.Columns) {
				item = pyexec.ItemResult{FactorID: factorID, Err: fmt.Errorf("executor changed result columns between recalc periods")}
			} else {
				if len(item.Columns) == 0 {
					item.Columns = previous.Columns
				}
				item.Rows = append(previous.Rows, item.Rows...)
			}
		}
		computation.Results[factorID][key] = item
		if item.Err != nil {
			state := computation.FactorStates[factorID]
			state.FailedSubjects = uniqueSorted(append(state.FailedSubjects, key))
			state.Status = "degraded"
			computation.FactorStates[factorID] = state
		}
	}

	var timeFactors []domain.FactorDef
	var crossFactors []domain.FactorDef
	for _, factor := range plan.Factors {
		if factor.Status != domain.FactorStatusEnabled {
			continue
		}
		switch factor.FactorType {
		case domain.FactorTypeTimeSeries:
			timeFactors = append(timeFactors, factor)
		case domain.FactorTypeCrossSection:
			if computation.FactorStates[factor.FactorID].Status != "skipped" {
				crossFactors = append(crossFactors, factor)
			}
		}
	}
	tasks := make([]computeTask, 0, len(available)+len(crossFactors))
	for _, subject := range available {
		if len(timeFactors) > 0 {
			tasks = append(tasks, computeTask{subject: subject, factors: timeFactors, target: target})
		}
	}
	for _, factor := range crossFactors {
		factorTargets := []time.Time{target}
		if plan.Mode == ModeRecalc {
			factorTargets = targets
		}
		for _, factorTarget := range factorTargets {
			tasks = append(tasks, computeTask{cross: true, factors: []domain.FactorDef{factor}, target: factorTarget})
		}
	}
	if len(tasks) == 0 {
		return computation, nil
	}
	if r.exec == nil {
		return Computation{}, errors.New("factor Python executor is required")
	}
	var panel storageio.Frame
	var panels *panelIndex
	if len(crossFactors) > 0 {
		var panelErr error
		panel, panelErr = buildPanelFrame(available, loaded.Frames)
		if panelErr != nil {
			return Computation{}, panelErr
		}
		if plan.Mode == ModeRecalc {
			if panels, panelErr = newPanelIndex(panel); panelErr != nil {
				return Computation{}, panelErr
			}
		}
	}

	workCtx := ctx
	cancel := func() {}
	if plan.Budget > 0 {
		workCtx, cancel = context.WithTimeout(ctx, plan.Budget)
	}
	defer cancel()
	workers := r.cfg.PythonWorkers
	if workers < 1 {
		workers = 1
	}
	if workers > len(tasks) {
		workers = len(tasks)
	}
	jobs := make(chan computeTask)
	var workersDone sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		workersDone.Add(1)
		go func() {
			defer workersDone.Done()
			for task := range jobs {
				frame := any(panel)
				contextValues, contextErr := crossSectionContext(r.clock, plan, task.target, task.factors, available)
				if task.cross && plan.Mode == ModeRecalc && contextErr == nil {
					factorStart, windowErr := r.clock.Window(task.target, plan.Set.Freq, task.factors[0].LookbackPeriods)
					if windowErr != nil {
						contextErr = windowErr
					} else {
						frame = panels.slice(factorStart[0], task.target.Add(freqDuration))
						contextValues["target_period_times"] = []string{task.target.UTC().Format(time.RFC3339Nano)}
					}
				}
				resultKey := ""
				if !task.cross {
					frame = frameFor(loaded.Frames[task.subject], task.subject, plan.CarryColumns)
					contextValues, contextErr = timeseriesContext(r.clock, plan, task.target, task.factors, task.subject)
					resultKey = task.subject
				}
				if contextErr != nil {
					for _, factor := range task.factors {
						addResult(factor.FactorID, resultKey, pyexec.ItemResult{FactorID: factor.FactorID, Err: contextErr})
					}
					continue
				}
				if workCtx.Err() != nil {
					for _, factor := range task.factors {
						addResult(factor.FactorID, resultKey, pyexec.ItemResult{FactorID: factor.FactorID, Err: workCtx.Err()})
					}
					continue
				}
				calls := make([]pyexec.FactorCall, 0, len(task.factors))
				for _, factor := range task.factors {
					calls = append(calls, factorCall(r.cfg.FactorsDir, factor))
				}
				items, callErr := r.exec.Exec(workCtx, pyexec.Request{Frame: frame, Factors: calls, Context: contextValues})
				itemsByID := make(map[string]pyexec.ItemResult, len(items))
				for _, item := range items {
					itemsByID[item.FactorID] = item
				}
				for _, factor := range task.factors {
					item, ok := itemsByID[factor.FactorID]
					if !ok {
						item = pyexec.ItemResult{FactorID: factor.FactorID, Err: fmt.Errorf("executor omitted factor result")}
					}
					if callErr != nil {
						item = pyexec.ItemResult{FactorID: factor.FactorID, Err: callErr}
					}
					if task.cross && item.Err != nil {
						statesMu.Lock()
						state := computation.FactorStates[factor.FactorID]
						state.FailedSubjects = uniqueSorted(append(state.FailedSubjects, available...))
						state.Status = "degraded"
						computation.FactorStates[factor.FactorID] = state
						statesMu.Unlock()
						if plan.Mode == ModeRecalc {
							item.Err = nil
						}
					}
					addResult(factor.FactorID, resultKey, item)
				}
			}
		}()
	}
	for _, task := range tasks {
		jobs <- task
	}
	close(jobs)
	workersDone.Wait()
	if ctx.Err() != nil {
		return computation, ctx.Err()
	}
	computation.BudgetHit = errors.Is(workCtx.Err(), context.DeadlineExceeded)
	return computation, nil
}

func planTarget(clock periodclock.Clock, plan Plan) (time.Time, error) {
	duration, err := clock.Duration(plan.Set.Freq)
	if err != nil {
		return time.Time{}, err
	}
	target := plan.TargetEnd.Add(-duration)
	aligned, err := clock.Align(target, plan.Set.Freq)
	if err != nil {
		return time.Time{}, err
	}
	return aligned, nil
}

func timeseriesContext(clock periodclock.Clock, plan Plan, target time.Time, factors []domain.FactorDef, subject string) (map[string]any, error) {
	if plan.Mode == ModeRecalc {
		return recalcTimeseriesContext(clock, plan, factors, subject)
	}
	return baseComputeContext(clock, plan, target, factors, map[string]any{"subject_id": subject})
}

func crossSectionContext(clock periodclock.Clock, plan Plan, target time.Time, factors []domain.FactorDef, available []string) (map[string]any, error) {
	extra := map[string]any{"expected_subjects": append([]string(nil), plan.Expected...), "available_subjects": append([]string(nil), available...)}
	if plan.Mode == ModeRecalc {
		extra["target_period_times"] = []string{target.UTC().Format(time.RFC3339Nano)}
	}
	return baseComputeContext(clock, plan, target, factors, extra)
}

func recalcTimeseriesContext(clock periodclock.Clock, plan Plan, factors []domain.FactorDef, subject string) (map[string]any, error) {
	targets, err := periodRange(clock, plan.TargetStart, plan.TargetEnd, plan.Set.Freq)
	if err != nil {
		return nil, err
	}
	periodsByFactor := make(map[string][]string, len(factors))
	for _, factor := range factors {
		window, err := clock.Window(plan.TargetStart, plan.Set.Freq, factor.LookbackPeriods)
		if err != nil {
			return nil, fmt.Errorf("factor %q lookback window: %w", factor.FactorID, err)
		}
		periods, err := periodRange(clock, window[0], plan.TargetEnd, plan.Set.Freq)
		if err != nil {
			return nil, err
		}
		periodsByFactor[factor.FactorID] = formatPeriods(periods)
	}
	last := targets[len(targets)-1]
	return map[string]any{
		"period_time": last.Unix(), "frequency": plan.Set.Freq,
		"period_times_by_factor": periodsByFactor, "target_period_times": formatPeriods(targets),
		"subject_id": subject,
	}, nil
}

func baseComputeContext(clock periodclock.Clock, plan Plan, target time.Time, factors []domain.FactorDef, extra map[string]any) (map[string]any, error) {
	periods := make(map[string][]string, len(factors))
	for _, factor := range factors {
		window, err := clock.Window(target, plan.Set.Freq, factor.LookbackPeriods)
		if err != nil {
			return nil, fmt.Errorf("factor %q lookback window: %w", factor.FactorID, err)
		}
		values := make([]string, len(window))
		for i, period := range window {
			values[i] = period.UTC().Format(time.RFC3339Nano)
		}
		periods[factor.FactorID] = values
	}
	contextValues := map[string]any{"period_time": target.Unix(), "frequency": plan.Set.Freq, "period_times_by_factor": periods}
	for key, value := range extra {
		contextValues[key] = value
	}
	return contextValues, nil
}

func periodRange(clock periodclock.Clock, start, end time.Time, freq string) ([]time.Time, error) {
	if !start.Before(end) {
		return nil, fmt.Errorf("factor period range must have start before end")
	}
	aligned, err := clock.Align(start, freq)
	if err != nil {
		return nil, err
	}
	if !aligned.Equal(start) {
		return nil, fmt.Errorf("factor period range start is not aligned")
	}
	duration, err := clock.Duration(freq)
	if err != nil {
		return nil, err
	}
	periods := make([]time.Time, 0)
	for at := start.UTC(); at.Before(end); at = at.Add(duration) {
		periods = append(periods, at)
	}
	if len(periods) == 0 {
		return nil, fmt.Errorf("factor period range contains no periods")
	}
	return periods, nil
}

func formatPeriods(periods []time.Time) []string {
	values := make([]string, len(periods))
	for i, period := range periods {
		values[i] = period.UTC().Format(time.RFC3339Nano)
	}
	return values
}

func factorCall(factorsDir string, factor domain.FactorDef) pyexec.FactorCall {
	return pyexec.FactorCall{
		FactorID: factor.FactorID, Name: factor.Name, SourceHash: factor.SourceHash,
		SourcePath: filepath.Join(factorsDir, factor.Name, factor.SourceHash+".py"),
		FactorType: factor.FactorType, InputColumns: append([]string(nil), factor.InputColumns...),
		Outputs: append([]string(nil), factor.Outputs...), Params: json.RawMessage(factor.ParamsJSON),
		LookbackPeriods: factor.LookbackPeriods,
	}
}

func frameFor(frame *storageio.Frame, subject string, columns []string) storageio.Frame {
	if frame == nil {
		return storageio.Frame{SubjectID: subject, Columns: append([]string{"data_time", "series_tag"}, columns...), Rows: [][]any{}}
	}
	return *frame
}

func buildPanelFrame(subjects []string, frames map[string]*storageio.Frame) (storageio.Frame, error) {
	ordered := append([]string(nil), subjects...)
	sort.Strings(ordered)
	panel := storageio.Frame{Columns: []string{"data_time", "series_tag", "subject_id"}}
	var expectedColumns []string
	for _, subject := range ordered {
		frame := frames[subject]
		if frame == nil {
			continue
		}
		if len(frame.Columns) < 2 || frame.Columns[0] != "data_time" || frame.Columns[1] != "series_tag" {
			return storageio.Frame{}, fmt.Errorf("frame for %q does not start with data_time, series_tag", subject)
		}
		if expectedColumns == nil {
			expectedColumns = append([]string(nil), frame.Columns[2:]...)
			panel.Columns = append(panel.Columns, expectedColumns...)
		} else if !equalStrings(expectedColumns, frame.Columns[2:]) {
			return storageio.Frame{}, fmt.Errorf("frame columns differ for subject %q", subject)
		}
		for rowNo, row := range frame.Rows {
			if len(row) != len(frame.Columns) {
				return storageio.Frame{}, fmt.Errorf("frame row %d for %q has %d values for %d columns", rowNo, subject, len(row), len(frame.Columns))
			}
			values := make([]any, 0, len(row)+1)
			values = append(values, row[:2]...)
			values = append(values, subject)
			values = append(values, row[2:]...)
			panel.Rows = append(panel.Rows, values)
		}
	}
	return panel, nil
}

// panelIndex keeps the cross-section panel grouped by subject with rows ordered
// by data_time, so a recalc period can cut its lookback window by binary search
// instead of re-scanning and re-parsing the whole chunk for every period.
type panelIndex struct {
	columns  []string
	subjects []panelSubject
}

type panelSubject struct {
	times []time.Time
	rows  [][]any
}

func newPanelIndex(panel storageio.Frame) (*panelIndex, error) {
	index := &panelIndex{columns: append([]string(nil), panel.Columns...)}
	current := -1
	var currentSubject any
	for rowNo, row := range panel.Rows {
		at, err := parseDataTime(row[0])
		if err != nil {
			return nil, fmt.Errorf("panel row %d data_time: %w", rowNo, err)
		}
		if current < 0 || row[2] != currentSubject {
			index.subjects = append(index.subjects, panelSubject{})
			current++
			currentSubject = row[2]
		}
		group := &index.subjects[current]
		group.times = append(group.times, at)
		group.rows = append(group.rows, row)
	}
	for i := range index.subjects {
		group := &index.subjects[i]
		if !sort.SliceIsSorted(group.rows, func(a, b int) bool { return group.times[a].Before(group.times[b]) }) {
			order := make([]int, len(group.rows))
			for k := range order {
				order[k] = k
			}
			sort.SliceStable(order, func(a, b int) bool { return group.times[order[a]].Before(group.times[order[b]]) })
			times := make([]time.Time, len(order))
			rows := make([][]any, len(order))
			for k, from := range order {
				times[k], rows[k] = group.times[from], group.rows[from]
			}
			group.times, group.rows = times, rows
		}
	}
	return index, nil
}

// slice returns the rows with start <= data_time < end, subject by subject.
func (p *panelIndex) slice(start, end time.Time) storageio.Frame {
	frame := storageio.Frame{Columns: append([]string(nil), p.columns...)}
	for _, group := range p.subjects {
		lo := sort.Search(len(group.times), func(i int) bool { return !group.times[i].Before(start) })
		hi := sort.Search(len(group.times), func(i int) bool { return !group.times[i].Before(end) })
		frame.Rows = append(frame.Rows, group.rows[lo:hi]...)
	}
	return frame
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func subjectDifference(expected, available []string) []string {
	availableSet := make(map[string]struct{}, len(available))
	for _, subject := range available {
		availableSet[subject] = struct{}{}
	}
	var missing []string
	for _, subject := range expected {
		if _, ok := availableSet[subject]; !ok {
			missing = append(missing, subject)
		}
	}
	return uniqueSorted(missing)
}
