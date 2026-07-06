package scheduler

import "github.com/mooyang-code/moox/modules/factor/internal/engine"

// Task is a scheduler-owned executable task.
type Task struct {
	engine.FactorTask
	TriggerType string
	FactorIDs   []string
}

type taskKey struct {
	spaceID       string
	sourceDataset string
	targetDataset string
	subjectID     string
	freq          string
}

func keyOf(task Task) taskKey {
	return taskKey{
		spaceID:       task.SpaceID,
		sourceDataset: task.SourceDataset,
		targetDataset: task.TargetDataset,
		subjectID:     task.SubjectID,
		freq:          task.Freq,
	}
}
