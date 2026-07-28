package scheduler

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
)

// Task is a scheduler-owned executable task.
type Task struct {
	engine.FactorTask
	TriggerType string
}

// DeterministicTaskID identifies the executable task content, independent of enqueue order.
func DeterministicTaskID(task Task) string {
	factorIDs := make([]string, 0, len(task.Factors))
	for _, factor := range task.Factors {
		factorIDs = append(factorIDs, factor.FactorID)
	}
	sort.Strings(factorIDs)

	h := sha256.New()
	write := func(value string) {
		_, _ = h.Write([]byte(fmt.Sprintf("%d:%s;", len(value), value)))
	}
	for _, value := range []string{
		task.SpaceID,
		task.SourceDataset,
		task.TargetDataset,
		task.SubjectID,
		task.Freq,
		task.StartTime.UTC().Format(time.RFC3339Nano),
		task.EndTime.UTC().Format(time.RFC3339Nano),
		task.TriggerType,
	} {
		write(value)
	}
	for _, factorID := range factorIDs {
		write("factor:" + factorID)
	}
	return fmt.Sprintf("ft-%x", h.Sum(nil)[:16])
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
