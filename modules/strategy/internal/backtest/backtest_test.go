package backtest

import (
	"context"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"testing"
)

func TestHashDecisionStable(t *testing.T) {
	o := domain.Output{Action: domain.ActionHold, NextState: map[string]any{}}
	if HashDecision(o) != HashDecision(o) {
		t.Fatal()
	}
}

func TestReplayOrdersHistoricalTasksDeterministically(t *testing.T) {
	tasks := []domain.Task{{RunID: "b", TriggerBarTime: "2026-01-02"}, {RunID: "a", TriggerBarTime: "2026-01-01"}}
	job, decisions, err := Replay(context.Background(), tasks, func(_ context.Context, task domain.Task) (domain.Output, string, error) {
		return domain.Output{Action: domain.ActionHold, NextState: map[string]any{"run": task.RunID}}, task.RunID, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 || decisions[0].TriggerBarTime != "2026-01-01" || len(job.Decisions) != 2 || job.ConfigHash == "" {
		t.Fatalf("job=%+v decisions=%+v", job, decisions)
	}
}
