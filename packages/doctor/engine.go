package doctor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

const defaultCheckTimeout = 5 * time.Second

type Runner interface {
	Run(context.Context, CheckSpec, []DependencyContext) CheckResult
}

type RunnerFunc func(context.Context, CheckSpec, []DependencyContext) CheckResult

func (f RunnerFunc) Run(ctx context.Context, spec CheckSpec, dependencies []DependencyContext) CheckResult {
	return f(ctx, spec, dependencies)
}

type Engine struct {
	Mode         Mode
	TotalTimeout time.Duration
	Now          func() time.Time
}

func (e Engine) Run(parent context.Context, specs []CheckSpec, runner Runner) (Report, error) {
	if err := e.Mode.Validate(); err != nil {
		return Report{}, err
	}
	if runner == nil {
		return Report{}, errors.New("doctor runner is required")
	}
	ordered, err := orderSpecs(specs)
	if err != nil {
		return Report{}, err
	}
	now := e.Now
	if now == nil {
		now = time.Now
	}
	totalTimeout := e.TotalTimeout
	if totalTimeout <= 0 {
		totalTimeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, totalTimeout)
	defer cancel()

	report := Report{
		SchemaVersion: ReportSchemaVersion,
		Mode:          e.Mode,
		StartedAt:     now().UTC(),
	}
	results := make(map[string]CheckResult, len(ordered))
	for _, spec := range ordered {
		dependencies := dependencyContexts(spec, results)
		if synthetic, ok := dependencyResult(spec, dependencies, now); ok {
			report.Checks = append(report.Checks, synthetic)
			results[spec.ID] = synthetic
			continue
		}
		result, executionErr := runOne(ctx, spec, dependencies, runner, now)
		if executionErr != nil {
			report.executionErr = executionErr
		}
		report.Checks = append(report.Checks, result)
		results[spec.ID] = result
		if ctx.Err() != nil {
			break
		}
	}
	if len(report.Checks) < len(ordered) {
		for _, spec := range ordered[len(report.Checks):] {
			result := CheckResult{
				ID:         spec.ID,
				Status:     StatusUnknown,
				Summary:    "doctor execution deadline reached before check started",
				Error:      ctx.Err().Error(),
				StartedAt:  now().UTC(),
				FinishedAt: now().UTC(),
			}
			report.Checks = append(report.Checks, result)
			results[spec.ID] = result
		}
	}
	report.FinishedAt = now().UTC()
	finalizeReport(&report, ordered, results)
	return report, nil
}

func orderSpecs(specs []CheckSpec) ([]CheckSpec, error) {
	if len(specs) > MaxReportChecks {
		return nil, fmt.Errorf("%d checks exceeds limit %d", len(specs), MaxReportChecks)
	}
	byID := make(map[string]CheckSpec, len(specs))
	position := make(map[string]int, len(specs))
	for i, spec := range specs {
		if spec.ID == "" {
			return nil, fmt.Errorf("check %d has empty id", i)
		}
		if _, exists := byID[spec.ID]; exists {
			return nil, fmt.Errorf("duplicate check id %q", spec.ID)
		}
		byID[spec.ID] = spec
		position[spec.ID] = i
	}
	indegree := make(map[string]int, len(specs))
	downstream := make(map[string][]string, len(specs))
	for _, spec := range specs {
		seen := map[string]bool{}
		for _, dependency := range append(append([]string{}, spec.RequiredDependencies...), spec.OptionalDependencies...) {
			if _, exists := byID[dependency]; !exists {
				return nil, fmt.Errorf("check %q depends on unknown check %q", spec.ID, dependency)
			}
			if seen[dependency] {
				return nil, fmt.Errorf("check %q repeats dependency %q", spec.ID, dependency)
			}
			seen[dependency] = true
			indegree[spec.ID]++
			downstream[dependency] = append(downstream[dependency], spec.ID)
		}
	}
	ready := make([]string, 0, len(specs))
	for _, spec := range specs {
		if indegree[spec.ID] == 0 {
			ready = append(ready, spec.ID)
		}
	}
	sort.SliceStable(ready, func(i, j int) bool { return position[ready[i]] < position[ready[j]] })
	ordered := make([]CheckSpec, 0, len(specs))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byID[id])
		for _, child := range downstream[id] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
			}
		}
		sort.SliceStable(ready, func(i, j int) bool { return position[ready[i]] < position[ready[j]] })
	}
	if len(ordered) != len(specs) {
		return nil, errors.New("doctor check dependency cycle detected")
	}
	return ordered, nil
}

func dependencyContexts(spec CheckSpec, results map[string]CheckResult) []DependencyContext {
	contexts := make([]DependencyContext, 0, len(spec.RequiredDependencies)+len(spec.OptionalDependencies))
	for _, id := range spec.RequiredDependencies {
		result := results[id]
		contexts = append(contexts, DependencyContext{CheckID: id, Status: result.Status, Summary: result.Summary, Required: true})
	}
	for _, id := range spec.OptionalDependencies {
		result := results[id]
		if result.Status != StatusPass {
			contexts = append(contexts, DependencyContext{CheckID: id, Status: result.Status, Summary: result.Summary, Required: false})
		}
	}
	return contexts
}

func dependencyResult(spec CheckSpec, dependencies []DependencyContext, now func() time.Time) (CheckResult, bool) {
	for _, dependency := range dependencies {
		if dependency.Required && dependency.Status == StatusSkipped {
			return syntheticResult(spec.ID, StatusFail, "required dependency was skipped", dependencies, now), true
		}
	}
	for _, dependency := range dependencies {
		if dependency.Required && (dependency.Status == StatusFail || dependency.Status == StatusUnknown || dependency.Status == StatusBlocked) {
			return syntheticResult(spec.ID, StatusBlocked, "required dependency did not pass", dependencies, now), true
		}
	}
	return CheckResult{}, false
}

func syntheticResult(id string, status CheckStatus, summary string, dependencies []DependencyContext, now func() time.Time) CheckResult {
	at := now().UTC()
	return CheckResult{ID: id, Status: status, Summary: summary, DependencyContext: dependencies, StartedAt: at, FinishedAt: at}
}

func runOne(parent context.Context, spec CheckSpec, dependencies []DependencyContext, runner Runner, now func() time.Time) (CheckResult, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	startedAt := now().UTC()
	resultCh := make(chan CheckResult, 1)
	go func() {
		resultCh <- runner.Run(ctx, spec, dependencies)
	}()
	select {
	case result := <-resultCh:
		if result.ID == "" {
			result.ID = spec.ID
		}
		if result.ID != spec.ID {
			return CheckResult{ID: spec.ID, Status: StatusFail, Summary: "runner returned mismatched check id", Error: result.ID, StartedAt: startedAt, FinishedAt: now().UTC()}, nil
		}
		result.StartedAt = startedAt
		result.FinishedAt = now().UTC()
		result.DependencyContext = dependencies
		if err := result.Validate(); err != nil {
			return CheckResult{ID: spec.ID, Status: StatusFail, Summary: "runner returned invalid result", Error: err.Error(), StartedAt: startedAt, FinishedAt: now().UTC()}, nil
		}
		return result, nil
	case <-ctx.Done():
		return CheckResult{ID: spec.ID, Status: StatusUnknown, Summary: "check deadline exceeded", Error: ctx.Err().Error(), DependencyContext: dependencies, StartedAt: startedAt, FinishedAt: now().UTC()}, ctx.Err()
	}
}

func finalizeReport(report *Report, specs []CheckSpec, results map[string]CheckResult) {
	requiredDependencies := make(map[string][]string, len(specs))
	for _, spec := range specs {
		requiredDependencies[spec.ID] = append([]string{}, spec.RequiredDependencies...)
	}
	for _, result := range report.Checks {
		switch result.Status {
		case StatusFail:
			if !hasAncestorStatus(result.ID, requiredDependencies, results, StatusFail, StatusUnknown) {
				report.RootCauseCheckIDs = append(report.RootCauseCheckIDs, result.ID)
			}
		case StatusUnknown:
			if !hasAncestorStatus(result.ID, requiredDependencies, results, StatusUnknown) {
				report.BlockingCheckIDs = append(report.BlockingCheckIDs, result.ID)
			}
		}
	}
	switch {
	case len(report.RootCauseCheckIDs) > 0:
		report.Conclusion = ConclusionUnhealthy
	case len(report.BlockingCheckIDs) > 0:
		report.Conclusion = ConclusionInconclusive
	default:
		report.Conclusion = ConclusionHealthy
		for _, result := range report.Checks {
			if result.Status == StatusWarn {
				report.Conclusion = ConclusionDegraded
				break
			}
		}
	}
}

func hasAncestorStatus(id string, dependencies map[string][]string, results map[string]CheckResult, statuses ...CheckStatus) bool {
	wanted := make(map[CheckStatus]bool, len(statuses))
	for _, status := range statuses {
		wanted[status] = true
	}
	seen := map[string]bool{}
	var visit func(string) bool
	visit = func(current string) bool {
		for _, dependency := range dependencies[current] {
			if seen[dependency] {
				continue
			}
			seen[dependency] = true
			if wanted[results[dependency].Status] || visit(dependency) {
				return true
			}
		}
		return false
	}
	return visit(id)
}
