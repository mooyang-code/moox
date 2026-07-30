package scheduler

import (
	"errors"
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

func BuildTask(scope TaskScope, factor domain.FactorDef, factorsDir string) (Task, error) {
	if scope.StartTime.IsZero() || scope.EndTime.IsZero() || !scope.StartTime.Before(scope.EndTime) {
		return Task{}, errors.New("valid start_time and end_time are required")
	}
	if strings.TrimSpace(scope.SubjectID) == "" {
		return Task{}, errors.New("subject_id is required")
	}
	if factor.Status != domain.FactorStatusEnabled {
		return Task{}, errors.New("factor is not enabled")
	}
	if factor.SourceHash == "" {
		return Task{}, errors.New("factor source hash is required")
	}
	sourcePath := factor.SourcePath
	if sourcePath == "" {
		sourcePath = filepath.Join(factorsDir, ".versions", "factor", factor.Name, factor.SourceHash, "module.py")
	}
	return Task{
		FactorTask: engine.FactorTask{
			TaskID: scope.TaskID, SpaceID: scope.SpaceID, SourceDataset: scope.SourceDataset,
			TargetDataset: scope.TargetDataset, SubjectID: scope.SubjectID, Freq: scope.Freq,
			StartTime: scope.StartTime.UTC(), EndTime: scope.EndTime.UTC(),
			LookbackPeriods: factor.LookbackPeriods,
			Factor: engine.FactorSpec{
				FactorID: factor.FactorID, Name: factor.Name, SourceHash: factor.SourceHash,
				SourcePath: sourcePath, InputColumns: append([]string(nil), factor.InputColumns...),
				Outputs: append([]string(nil), factor.Outputs...), ParamsJSON: factor.ParamsJSON,
			},
		},
		TriggerType: scope.TriggerType,
	}, nil
}
