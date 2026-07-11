package backtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

type Job struct {
	ID, ConfigHash string
	Decisions      []domain.Output
}

// Runner is deliberately the same shape as the live action's evaluation
// callback. A backtest therefore changes only the task source (historical
// snapshots), not strategy code or output validation.
type Runner func(context.Context, domain.Task) (domain.Output, string, error)

type Decision struct {
	TriggerBarTime string
	InputHash      string
	Output         domain.Output
}

// Replay evaluates tasks in timestamp order and returns decisions in that
// order. Equal timestamps use run_id as a deterministic tie breaker.
func Replay(ctx context.Context, tasks []domain.Task, run Runner) (Job, []Decision, error) {
	if run == nil {
		return Job{}, nil, errors.New("backtest runner is required")
	}
	ordered := append([]domain.Task(nil), tasks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		ti, ei := time.Parse(time.RFC3339Nano, ordered[i].TriggerBarTime)
		tj, ej := time.Parse(time.RFC3339Nano, ordered[j].TriggerBarTime)
		if ei == nil && ej == nil && !ti.Equal(tj) {
			return ti.Before(tj)
		}
		if (ei == nil && ej == nil && ti.Equal(tj)) || ordered[i].TriggerBarTime == ordered[j].TriggerBarTime || (ei != nil || ej != nil) {
			return ordered[i].RunID < ordered[j].RunID
		}
		return ordered[i].TriggerBarTime < ordered[j].TriggerBarTime
	})
	configBytes, err := json.Marshal(ordered)
	if err != nil {
		return Job{}, nil, err
	}
	configHash := sha256.Sum256(configBytes)
	job := Job{ID: hex.EncodeToString(configHash[:8]), ConfigHash: hex.EncodeToString(configHash[:])}
	decisions := make([]Decision, 0, len(ordered))
	job.Decisions = make([]domain.Output, 0, len(ordered))
	for _, task := range ordered {
		out, inputHash, err := run(ctx, task)
		if err != nil {
			return Job{}, nil, err
		}
		decisions = append(decisions, Decision{TriggerBarTime: task.TriggerBarTime, InputHash: inputHash, Output: out})
		job.Decisions = append(job.Decisions, out)
	}
	return job, decisions, nil
}

func HashDecision(o domain.Output) string {
	b, _ := json.Marshal(o)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
