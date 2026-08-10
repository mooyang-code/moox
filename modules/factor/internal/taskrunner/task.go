package taskrunner

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
)

// Task is a task-runner-owned executable task.
type Task struct {
	engine.FactorTask
	TriggerType string
}

// DeterministicTaskID identifies the executable task snapshot, independent of enqueue order.
func DeterministicTaskID(task Task) string {
	factor := task.Factor
	inputs := append([]string(nil), factor.InputColumns...)
	outputs := append([]string(nil), factor.Outputs...)
	sort.Strings(inputs)
	sort.Strings(outputs)
	factorPayload := encodeTaskIDParts(
		factor.FactorID,
		factor.Name,
		factor.SourceHash,
		factor.SourcePath,
		encodeTaskIDParts(inputs...),
		encodeTaskIDParts(outputs...),
		factor.ParamsJSON,
	)

	h := sha256.New()
	write := func(value string) {
		_, _ = h.Write([]byte(fmt.Sprintf("%d:%s;", len(value), value)))
	}
	for _, value := range []string{
		task.BindingID,
		task.SpaceID,
		task.SourceViewID,
		task.ResultDatasetID,
		task.SubjectID,
		task.Freq,
		task.StartTime.UTC().Format(time.RFC3339Nano),
		task.EndTime.UTC().Format(time.RFC3339Nano),
		task.TriggerType,
		strconv.Itoa(task.LookbackPeriods),
	} {
		write(value)
	}
	write("factor:" + factorPayload)
	return fmt.Sprintf("ft-%x", h.Sum(nil)[:16])
}

func encodeTaskIDParts(parts ...string) string {
	var encoded strings.Builder
	for _, part := range parts {
		_, _ = fmt.Fprintf(&encoded, "%d:%s;", len(part), part)
	}
	return encoded.String()
}

type Result struct {
	Task Task
	Err  error
}
