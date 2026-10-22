package resample

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
)

// subjectSource 读取源数据集的元数据与标的清单。
type subjectSource interface {
	GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error)
	ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error)
}

// SourceSubjects 返回任务源数据集当前的全部标的 ID（去重、升序）。
func SourceSubjects(ctx context.Context, source subjectSource, task *Task) ([]string, error) {
	if source == nil {
		return nil, fmt.Errorf("resample subject source is required")
	}
	info, err := source.GetDataset(ctx, task.Spec.SpaceID, task.Spec.SourceDatasetID)
	if err != nil {
		return nil, fmt.Errorf("get resample source Dataset: %w", err)
	}
	subjects, err := source.ResolveSubjects(ctx, task.Spec.SpaceID, info.SubjectTags)
	if err != nil {
		return nil, fmt.Errorf("list resample source subjects: %w", err)
	}
	ids := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		if id := strings.TrimSpace(subject.SubjectID); id != "" {
			ids = append(ids, id)
		}
	}
	return uniqueSorted(ids), nil
}

// sortedCopy 返回升序的副本。
func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
