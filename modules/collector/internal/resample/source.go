package resample

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/tagattr"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// subjectSource 读取源数据集的元数据与标的清单。
type subjectSource interface {
	GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error)
	ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error)
}

// tagSource 读取标签定义；统一数据集按标签的市场类型把成员分到各条源序列，需要它。
type tagSource interface {
	GetTag(ctx context.Context, spaceID, tagID string) (*storagepb.Tag, error)
}

// SourceMembers 返回任务源数据集当前各条源序列的成员（每项去重、升序）。
// 单序列任务只有一项，成员是源数据集全部标签的标的；统一数据集每个市场一项，只取该市场标签的成员，不取并集。
func SourceMembers(ctx context.Context, source subjectSource, task *Task) (Members, error) {
	if source == nil {
		return nil, fmt.Errorf("resample subject source is required")
	}
	info, err := source.GetDataset(ctx, task.Spec.SpaceID, task.Spec.SourceDatasetID)
	if err != nil {
		return nil, fmt.Errorf("get resample source Dataset: %w", err)
	}
	if len(task.Tags) == 1 {
		subjects, err := resolveSubjectIDs(ctx, source, task.Spec.SpaceID, info.SubjectTags)
		if err != nil {
			return nil, err
		}
		return Members{task.Tags[0]: subjects}.normalized(), nil
	}
	reader, ok := source.(tagSource)
	if !ok {
		return nil, fmt.Errorf("resample subject source cannot read tags, needed by a unified source Dataset")
	}
	tagIDsByMarket := map[string][]string{}
	for _, tagID := range info.SubjectTags {
		tag, err := reader.GetTag(ctx, task.Spec.SpaceID, tagID)
		if err != nil {
			return nil, fmt.Errorf("get resample source tag %s: %w", tagID, err)
		}
		market := tagattr.MarketType(tag)
		tagIDsByMarket[market] = append(tagIDsByMarket[market], tagID)
	}
	members := make(Members, len(task.Tags))
	for _, seriesTag := range task.Tags {
		subjects, err := resolveSubjectIDs(ctx, source, task.Spec.SpaceID, tagIDsByMarket[seriesset.SeriesTagMarket(seriesTag)])
		if err != nil {
			return nil, err
		}
		members[seriesTag] = subjects
	}
	return members.normalized(), nil
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
