package scheduler

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
)

type TaskScope struct {
	TaskID        string
	TriggerType   string
	SpaceID       string
	SourceDataset string
	TargetDataset string
	SubjectID     string
	Freq          string
	StartTime     time.Time
	EndTime       time.Time
}

func BuildTask(scope TaskScope, factors []domain.FactorDef, factorsDir string) (Task, error) {
	if scope.StartTime.IsZero() || scope.EndTime.IsZero() || !scope.StartTime.Before(scope.EndTime) {
		return Task{}, errors.New("valid start_time and end_time are required")
	}
	if strings.TrimSpace(scope.SubjectID) == "" {
		return Task{}, errors.New("subject_id is required")
	}
	if len(factors) == 0 {
		return Task{}, errors.New("at least one factor is required")
	}
	specs := make([]engine.FactorSpec, 0, len(factors))
	lookback := 0
	for _, factor := range factors {
		if factor.Status != domain.FactorStatusEnabled {
			return Task{}, fmt.Errorf("factor %s is not enabled", factor.FactorID)
		}
		if factor.SourceHash == "" {
			return Task{}, fmt.Errorf("factor %s source hash is required", factor.FactorID)
		}
		sourcePath := factor.SourcePath
		if sourcePath == "" {
			sourcePath = filepath.Join(factorsDir, ".versions", "factor", factor.Name, factor.SourceHash, "module.py")
		}
		specs = append(specs, engine.FactorSpec{
			FactorID: factor.FactorID, Name: factor.Name, SourceHash: factor.SourceHash,
			SourcePath: sourcePath, Periods: append([]int(nil), factor.Periods...),
			Depends: append([]string(nil), factor.Depends...),
		})
		if factor.LookbackBars > lookback {
			lookback = factor.LookbackBars
		}
	}
	return Task{
		FactorTask: engine.FactorTask{
			TaskID: scope.TaskID, SpaceID: scope.SpaceID, SourceDataset: scope.SourceDataset,
			TargetDataset: scope.TargetDataset, SubjectID: scope.SubjectID, Freq: scope.Freq,
			StartTime: scope.StartTime.UTC(), EndTime: scope.EndTime.UTC(),
			LookbackBars: lookback, Factors: specs,
		},
		TriggerType: scope.TriggerType,
	}, nil
}
