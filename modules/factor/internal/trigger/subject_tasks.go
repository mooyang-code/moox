package trigger

import (
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
)

func validateSubjectEvent(event SubjectEvent) error {
	p := event.Ready
	if event.SpaceID == "" || event.EventID == "" || p.GetSourceViewId() == "" || p.GetSourceDatasetId() == "" || p.GetSubjectId() == "" || p.GetFrequency() == "" || p.GetPeriodTime() <= 0 || p.GetInputContractVersion() == "" || p.GetSourceEventId() == "" || p.GetSourceNodeId() == "" || p.GetSourceStoreId() == "" || p.GetSourceSequence() == 0 {
		return fmt.Errorf("subject readiness identity is incomplete")
	}
	return nil
}

// BuildSubjectTasks uses one activated catalog snapshot; the caller must hold
// the catalog operation gate through execution, not reload definitions mid-run.
func BuildSubjectTasks(snapshot domain.CatalogSnapshot, event SubjectEvent, factorsDir string) ([]taskrunner.Task, error) {
	if err := validateSubjectEvent(event); err != nil {
		return nil, err
	}
	if snapshot.Revision <= 0 {
		return nil, fmt.Errorf("activated catalog revision is required")
	}
	definitions := make(map[string]domain.FactorDef, len(snapshot.Factors))
	for _, factor := range snapshot.Factors {
		definitions[factor.FactorID] = factor
	}
	p := event.Ready
	start := time.Unix(p.PeriodTime, 0).UTC()
	end, err := domain.NextPeriod(start, p.Frequency)
	if err != nil {
		return nil, err
	}
	var tasks []taskrunner.Task
	for _, binding := range snapshot.Bindings {
		if binding.SpaceID != event.SpaceID || binding.SourceViewID != p.SourceViewId || binding.Freq != p.Frequency || binding.Status == domain.BindingStatusDisabled || binding.Status == domain.BindingStatusCleanupPending {
			continue
		}
		factor, found := definitions[binding.FactorID]
		if !found {
			return nil, fmt.Errorf("binding %s definition is missing", binding.BindingID)
		}
		if err := domain.ValidateFactorType(factor.FactorType); err != nil {
			return nil, err
		}
		if factor.FactorType != domain.FactorTypeTimeSeries || factor.Status != domain.FactorStatusEnabled || !domain.BindingAllowsSubject(binding, p.SubjectId) {
			continue
		}
		if binding.Status != domain.BindingStatusEnabled {
			continue
		}
		task, err := taskrunner.BuildTask(taskrunner.TaskScope{
			CatalogRevision: snapshot.Revision,
			SourceNodeID:    p.SourceNodeId, SourceStoreID: p.SourceStoreId,
			SourceSequence: p.SourceSequence, SourceEventID: p.SourceEventId,
			BindingID: binding.BindingID, BindingGeneration: binding.BindingGeneration,
			TriggerType: "subject_ready", SpaceID: event.SpaceID, SourceViewID: binding.SourceViewID,
			ResultDatasetID: binding.ResultDatasetID, SubjectID: p.SubjectId, Freq: p.Frequency,
			PeriodTime: p.PeriodTime, StartTime: start, EndTime: end,
			TriggerEventID: event.EventID, TriggeredAt: time.Now().UTC(),
			InputContractVersion: p.InputContractVersion,
			SourceSeriesTag:      p.SeriesTag, FilterSourceSeriesTag: true,
		}, factor, factorsDir)
		if err != nil {
			return nil, err
		}
		task.TaskID = taskrunner.DeterministicTaskID(task)
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].BindingID < tasks[j].BindingID })
	return tasks, nil
}
