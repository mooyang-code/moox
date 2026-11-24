package resample

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
)

// subjectSource 读取源数据集的元数据与标的清单。
type subjectSource interface {
	GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error)
	ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error)
}

// SourceSubjects 返回任务源数据集当前的标的（去重、升序）：源数据集全部标签的成员。
func SourceSubjects(ctx context.Context, source subjectSource, task *Task) ([]string, error) {
	if source == nil {
		return nil, fmt.Errorf("resample subject source is required")
	}
	info, err := source.GetDataset(ctx, task.Spec.SpaceID, task.Spec.SourceDatasetID)
	if err != nil {
		return nil, fmt.Errorf("get resample source Dataset: %w", err)
	}
	return resolveSubjectIDs(ctx, source, task.Spec.SpaceID, info.SubjectTags)
}

func resolveSubjectIDs(ctx context.Context, source subjectSource, spaceID string, tagIDs []string) ([]string, error) {
	if len(tagIDs) == 0 {
		return nil, nil
	}
	subjects, err := source.ResolveSubjects(ctx, spaceID, tagIDs)
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
