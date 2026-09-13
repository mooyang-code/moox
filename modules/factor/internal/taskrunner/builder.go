package taskrunner

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
)

type TaskScope struct {
	CatalogRevision             int64
	SourceNodeID                string
	SourceStoreID               string
	SourceSequence              uint64
	SourceEventID               string
	SourceSeriesTag             string
	FilterSourceSeriesTag       bool
	TaskID                      string
	BindingID                   string
	BindingGeneration           string
	TriggerType                 string
	SpaceID                     string
	SourceViewID                string
	ExpectedActiveIndexID       string
	ExpectedActiveIndexRevision uint64
	ResultDatasetID             string
	SourceDataset               string
	TargetDataset               string
	SubjectID                   string
	Freq                        string
	PeriodTime                  int64
	TriggerEventID              string
	TriggeredAt                 time.Time
	StartTime                   time.Time
	EndTime                     time.Time
	InputContractVersion        string
	ExpectedSubjects            []string
	AvailableSubjects           []string
	MissingSubjects             []string
}

func BuildTask(scope TaskScope, factor domain.FactorDef, factorsDir string) (Task, error) {
	if scope.TriggerType == "subject_ready" {
		if strings.TrimSpace(scope.TriggerEventID) == "" {
			return Task{}, errors.New("subject task source event is required")
		}
		if strings.TrimSpace(scope.InputContractVersion) == "" {
			return Task{}, errors.New("subject task input contract is required")
		}
	}
	if scope.SourceViewID == "" {
		scope.SourceViewID = scope.SourceDataset
	}
	if scope.ResultDatasetID == "" {
		scope.ResultDatasetID = scope.TargetDataset
	}
	if scope.StartTime.IsZero() || scope.EndTime.IsZero() || !scope.StartTime.Before(scope.EndTime) {
		return Task{}, errors.New("valid start_time and end_time are required")
	}
	if err := domain.ValidateFactorType(factor.FactorType); err != nil {
		return Task{}, err
	}
	if err := domain.ValidateFactorReadLimit(factor); err != nil {
		return Task{}, err
	}
	if factor.FactorType == domain.FactorTypeTimeSeries && strings.TrimSpace(scope.SubjectID) == "" {
		return Task{}, errors.New("subject_id is required")
	}
	if factor.Status != domain.FactorStatusEnabled {
		return Task{}, errors.New("factor is not enabled")
	}
	if factor.SourceHash == "" {
		return Task{}, errors.New("factor source hash is required")
	}
	if strings.TrimSpace(scope.BindingGeneration) == "" {
		return Task{}, errors.New("binding generation is required")
	}
	sourcePath := factor.SourcePath
	if sourcePath == "" {
		sourcePath = filepath.Join(factorsDir, ".versions", "factor", factor.Name, factor.SourceHash, "module.py")
	}
	generation := ExecutionGeneration(scope, factor)
	return Task{
		FactorTask: engine.FactorTask{
			CatalogRevision: scope.CatalogRevision,
			SourceNodeID:    scope.SourceNodeID, SourceStoreID: scope.SourceStoreID,
			SourceSequence: scope.SourceSequence, SourceEventID: scope.SourceEventID,
			SourceSeriesTag: scope.SourceSeriesTag, FilterSourceSeriesTag: scope.FilterSourceSeriesTag,
			TaskID: scope.TaskID, BindingID: scope.BindingID, SpaceID: scope.SpaceID,
			BindingGeneration: generation,
			SourceViewID:      scope.SourceViewID, ResultDatasetID: scope.ResultDatasetID,
			ExpectedActiveIndexID:       scope.ExpectedActiveIndexID,
			ExpectedActiveIndexRevision: scope.ExpectedActiveIndexRevision,
			SourceDataset:               scope.SourceViewID, TargetDataset: scope.ResultDatasetID,
			SubjectID: scope.SubjectID, Freq: scope.Freq, PeriodTime: scope.PeriodTime,
			TriggerEventID: scope.TriggerEventID, TriggeredAt: scope.TriggeredAt.UTC(),
			StartTime: scope.StartTime.UTC(), EndTime: scope.EndTime.UTC(),
			LookbackPeriods:      factor.LookbackPeriods,
			InputContractVersion: scope.InputContractVersion,
			ExpectedSubjects:     append([]string(nil), scope.ExpectedSubjects...),
			AvailableSubjects:    append([]string(nil), scope.AvailableSubjects...),
			MissingSubjects:      append([]string(nil), scope.MissingSubjects...),
			Factor: engine.FactorSpec{
				FactorType: factor.FactorType,
				FactorID:   factor.FactorID, Name: factor.Name, SourceHash: factor.SourceHash,
				SourcePath: sourcePath, InputColumns: append([]string(nil), factor.InputColumns...),
				Outputs: append([]string(nil), factor.Outputs...), ParamsJSON: factor.ParamsJSON,
			},
		},
		TriggerType: scope.TriggerType,
	}, nil
}

// ExecutionGeneration binds a catalog incarnation to output schema and routing.
func ExecutionGeneration(scope TaskScope, factor domain.FactorDef) string {
	ownership, _ := json.Marshal([]any{scope.BindingGeneration, scope.BindingID, scope.SpaceID, scope.SourceViewID, scope.ResultDatasetID, scope.Freq, factor.FactorID, factor.FactorType, factor.Outputs})
	return fmt.Sprintf("%x", sha256.Sum256(ownership))
}
