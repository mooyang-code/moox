package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SubjectExclusion 是采集名单里的一条排除：MarketType 为空表示该空间所有市场，ExpiresAt 为零表示不过期。
type SubjectExclusion struct {
	SpaceID    string
	SubjectID  string
	MarketType string
	Reason     string
	Caller     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// Excludes 报告这条排除是否作用于某个市场。
func (e SubjectExclusion) Excludes(marketType string) bool {
	return e.MarketType == "" || e.MarketType == strings.ToLower(strings.TrimSpace(marketType))
}

type subjectExclusionRow struct {
	SpaceID    string     `gorm:"column:c_space_id;primaryKey"`
	SubjectID  string     `gorm:"column:c_subject_id;primaryKey"`
	MarketType string     `gorm:"column:c_market_type;primaryKey"`
	Reason     string     `gorm:"column:c_reason"`
	Caller     string     `gorm:"column:c_caller"`
	CreatedAt  time.Time  `gorm:"column:c_ctime"`
	ExpiresAt  *time.Time `gorm:"column:c_expires_at"`
}

func (subjectExclusionRow) TableName() string { return "t_collector_subject_exclusions" }

func (row subjectExclusionRow) exclusion() SubjectExclusion {
	item := SubjectExclusion{SpaceID: row.SpaceID, SubjectID: row.SubjectID, MarketType: row.MarketType, Reason: row.Reason, Caller: row.Caller, CreatedAt: row.CreatedAt.UTC()}
	if row.ExpiresAt != nil {
		item.ExpiresAt = row.ExpiresAt.UTC()
	}
	return item
}

// NormalizeSubjectExclusion 统一空间、标的与市场的写法（空间、市场小写，标的大写）。
func NormalizeSubjectExclusion(item SubjectExclusion) SubjectExclusion {
	item.SpaceID = strings.ToLower(strings.TrimSpace(item.SpaceID))
	item.SubjectID = strings.ToUpper(strings.TrimSpace(item.SubjectID))
	item.MarketType = strings.ToLower(strings.TrimSpace(item.MarketType))
	item.Reason = strings.TrimSpace(item.Reason)
	return item
}

// SubjectExclusionRepository 保存采集名单排除。
type SubjectExclusionRepository struct{ db *gorm.DB }

func NewSubjectExclusionRepository(db *gorm.DB) *SubjectExclusionRepository {
	return &SubjectExclusionRepository{db: db}
}

// Set 加入一批排除；同一（空间, 标的, 市场）重复设置时更新原因、调用方、设置时间与到期时间。
func (r *SubjectExclusionRepository) Set(ctx context.Context, items []SubjectExclusion) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("subject exclusion repository is not initialized")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range items {
			item = NormalizeSubjectExclusion(item)
			if item.SpaceID == "" || item.SubjectID == "" {
				return fmt.Errorf("排除需要空间与标的")
			}
			row := subjectExclusionRow{SpaceID: item.SpaceID, SubjectID: item.SubjectID, MarketType: item.MarketType, Reason: item.Reason, Caller: item.Caller, CreatedAt: item.CreatedAt.UTC()}
			if row.CreatedAt.IsZero() {
				row.CreatedAt = time.Now().UTC()
			}
			if !item.ExpiresAt.IsZero() {
				expires := item.ExpiresAt.UTC()
				row.ExpiresAt = &expires
			}
			conflict := clause.OnConflict{
				Columns:   []clause.Column{{Name: "c_space_id"}, {Name: "c_subject_id"}, {Name: "c_market_type"}},
				DoUpdates: clause.AssignmentColumns([]string{"c_reason", "c_caller", "c_ctime", "c_expires_at"}),
			}
			if err := tx.Clauses(conflict).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Clear 移除空间里这些标的的排除；marketType 为空时移除该标的全部市场的排除。返回实际移除的条数。
func (r *SubjectExclusionRepository) Clear(ctx context.Context, spaceID string, subjectIDs []string, marketType string) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("subject exclusion repository is not initialized")
	}
	ids := make([]string, 0, len(subjectIDs))
	for _, id := range subjectIDs {
		if id = strings.ToUpper(strings.TrimSpace(id)); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	query := r.db.WithContext(ctx).Where("c_space_id = ? AND c_subject_id IN ?", strings.ToLower(strings.TrimSpace(spaceID)), ids)
	if marketType = strings.ToLower(strings.TrimSpace(marketType)); marketType != "" {
		query = query.Where("c_market_type = ?", marketType)
	}
	result := query.Delete(&subjectExclusionRow{})
	return result.RowsAffected, result.Error
}

// Active 返回空间在 now 时刻生效的排除（未到期），按标的、市场排序。
func (r *SubjectExclusionRepository) Active(ctx context.Context, spaceID string, now time.Time) ([]SubjectExclusion, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("subject exclusion repository is not initialized")
	}
	var rows []subjectExclusionRow
	if err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND (c_expires_at IS NULL OR c_expires_at > ?)", strings.ToLower(strings.TrimSpace(spaceID)), now.UTC()).
		Order("c_subject_id").Order("c_market_type").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]SubjectExclusion, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.exclusion())
	}
	return result, nil
}
