package doctor

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEngineDependencyTruthTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		upstream    CheckStatus
		required    bool
		want        CheckStatus
		wantRuns    int
		wantContext bool
	}{
		{name: "required pass executes", upstream: StatusPass, required: true, want: StatusPass, wantRuns: 2},
		{name: "required warn executes", upstream: StatusWarn, required: true, want: StatusPass, wantRuns: 2, wantContext: true},
		{name: "required fail blocks", upstream: StatusFail, required: true, want: StatusBlocked, wantRuns: 1},
		{name: "required unknown blocks", upstream: StatusUnknown, required: true, want: StatusBlocked, wantRuns: 1},
		{name: "required blocked blocks", upstream: StatusBlocked, required: true, want: StatusBlocked, wantRuns: 1},
		{name: "required skipped is config failure", upstream: StatusSkipped, required: true, want: StatusFail, wantRuns: 1},
		{name: "optional fail executes", upstream: StatusFail, required: false, want: StatusPass, wantRuns: 2, wantContext: true},
		{name: "optional unknown executes", upstream: StatusUnknown, required: false, want: StatusPass, wantRuns: 2, wantContext: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runs := 0
			spec := CheckSpec{ID: "downstream"}
			if tt.required {
				spec.RequiredDependencies = []string{"upstream"}
			} else {
				spec.OptionalDependencies = []string{"upstream"}
			}
			runner := RunnerFunc(func(_ context.Context, spec CheckSpec, dependencyContext []DependencyContext) CheckResult {
				runs++
				if spec.ID == "upstream" {
					return CheckResult{ID: spec.ID, Status: tt.upstream, Summary: "upstream"}
				}
				if tt.wantContext && len(dependencyContext) == 0 {
					t.Fatal("expected degraded dependency context")
				}
				return CheckResult{ID: spec.ID, Status: StatusPass, Summary: "downstream"}
			})

			report, err := (Engine{Mode: ModeDiagnose}).Run(context.Background(), []CheckSpec{{ID: "upstream"}, spec}, runner)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if runs != tt.wantRuns {
				t.Fatalf("runs = %d, want %d", runs, tt.wantRuns)
			}
			if got := report.CheckByID("downstream").Status; got != tt.want {
				t.Fatalf("downstream status = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestEngineConclusionAndRoots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statuses   map[string]CheckStatus
		want       Conclusion
		wantRoots  []string
		wantBlocks []string
	}{
		{name: "all pass", statuses: map[string]CheckStatus{"a": StatusPass}, want: ConclusionHealthy},
		{name: "warn", statuses: map[string]CheckStatus{"a": StatusWarn}, want: ConclusionDegraded},
		{name: "root fail wins unknown", statuses: map[string]CheckStatus{"a": StatusFail, "b": StatusUnknown}, want: ConclusionUnhealthy, wantRoots: []string{"a"}, wantBlocks: []string{"b"}},
		{name: "unknown", statuses: map[string]CheckStatus{"a": StatusUnknown}, want: ConclusionInconclusive, wantBlocks: []string{"a"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			specs := make([]CheckSpec, 0, len(tt.statuses))
			for id := range tt.statuses {
				specs = append(specs, CheckSpec{ID: id})
			}
			report, err := (Engine{Mode: ModeDiagnose}).Run(context.Background(), specs, RunnerFunc(func(_ context.Context, spec CheckSpec, _ []DependencyContext) CheckResult {
				return CheckResult{ID: spec.ID, Status: tt.statuses[spec.ID]}
			}))
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if report.Conclusion != tt.want {
				t.Fatalf("conclusion = %s, want %s", report.Conclusion, tt.want)
			}
			assertStrings(t, report.RootCauseCheckIDs, tt.wantRoots)
			assertStrings(t, report.BlockingCheckIDs, tt.wantBlocks)
		})
	}
}

func TestEngineRejectsCyclesAndUnknownDependencies(t *testing.T) {
	t.Parallel()

	runner := RunnerFunc(func(_ context.Context, spec CheckSpec, _ []DependencyContext) CheckResult {
		return CheckResult{ID: spec.ID, Status: StatusPass}
	})
	_, err := (Engine{Mode: ModeDiagnose}).Run(context.Background(), []CheckSpec{{ID: "a", RequiredDependencies: []string{"b"}}}, runner)
	if err == nil {
		t.Fatal("unknown dependency accepted")
	}
	_, err = (Engine{Mode: ModeDiagnose}).Run(context.Background(), []CheckSpec{{ID: "a", RequiredDependencies: []string{"b"}}, {ID: "b", RequiredDependencies: []string{"a"}}}, runner)
	if err == nil {
		t.Fatal("dependency cycle accepted")
	}
}

func TestEnginePropagatesTimeoutAndCancellation(t *testing.T) {
	t.Parallel()

	runner := RunnerFunc(func(ctx context.Context, spec CheckSpec, _ []DependencyContext) CheckResult {
		<-ctx.Done()
		return CheckResult{ID: spec.ID, Status: StatusUnknown, Error: ctx.Err().Error()}
	})
	report, err := (Engine{Mode: ModeDiagnose, TotalTimeout: 20 * time.Millisecond}).Run(context.Background(), []CheckSpec{{ID: "slow", Timeout: time.Second}}, runner)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.CheckByID("slow").Status != StatusUnknown {
		t.Fatalf("timeout status = %s", report.CheckByID("slow").Status)
	}
	if !errors.Is(report.ExecutionError(), context.DeadlineExceeded) {
		t.Fatalf("execution error = %v", report.ExecutionError())
	}
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
