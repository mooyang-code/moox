package catalog

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/packages/storagepolicy"
)

// ListCategories 返回全部因子分类。
func (s *Service) ListCategories(ctx context.Context) ([]domain.FactorCategory, error) {
	return s.db.ListCategories(ctx)
}

// UpsertCategory 新建分类，或修改已有分类的名称与顺序。
func (s *Service) UpsertCategory(ctx context.Context, category domain.FactorCategory) (domain.FactorCategory, error) {
	return s.db.UpsertCategory(ctx, category)
}

// DeleteCategory 删除没有定义使用的分类。
func (s *Service) DeleteCategory(ctx context.Context, categoryID string) error {
	return s.db.DeleteCategory(ctx, categoryID)
}

// SetFactorCategory 修改定义的分类：分类是元数据，不进 definition_hash，成员启用期间也可以改。
func (s *Service) SetFactorCategory(ctx context.Context, factorID, categoryID string) (domain.FactorDef, error) {
	factorID, categoryID = strings.TrimSpace(factorID), strings.TrimSpace(categoryID)
	if categoryID == "" {
		categoryID = domain.DefaultCategoryID
	}
	unlock, err := s.locks.LockFactorContext(ctx, factorID)
	if err != nil {
		return domain.FactorDef{}, err
	}
	defer unlock()
	if err := s.db.SetFactorCategory(ctx, factorID, categoryID); err != nil {
		return domain.FactorDef{}, err
	}
	return s.db.GetFactor(ctx, factorID)
}

// freezeCutoff 返回集合冻结区间的清理点：结果数据集保留期之外再留一个最大回看（超出后补算不再可能，没有读取方）。
func (s *Service) freezeCutoff(ctx context.Context, set domain.FactorSet, now time.Time) (time.Time, bool) {
	if s.metadata == nil {
		return time.Time{}, false
	}
	dataset, err := s.metadata.GetDataset(ctx, set.SpaceID, set.ResultDatasetID)
	if err != nil {
		return time.Time{}, false
	}
	retention, err := storagepolicy.ParsePeriod(dataset.Retention)
	if err != nil || retention.Forever {
		return time.Time{}, false
	}
	clock, err := periodclock.ForSpace(set.SpaceID)
	if err != nil {
		return time.Time{}, false
	}
	duration, err := clock.Duration(set.Freq)
	if err != nil {
		return time.Time{}, false
	}
	members, err := s.db.ListMembers(ctx, set.SetID, "")
	if err != nil {
		return time.Time{}, false
	}
	lookback := 1
	for _, member := range members {
		lookback = max(lookback, member.Factor.LookbackPeriods)
	}
	return now.Add(-retention.Duration - time.Duration(lookback)*duration), true
}
