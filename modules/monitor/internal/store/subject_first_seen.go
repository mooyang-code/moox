package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// SubjectFirstSeenRepository 记录活跃对象第一次出现在采集对象清单里的时间，供 K 线新鲜度给新上线对象宽限期。
type SubjectFirstSeenRepository struct{ store *Store }

type subjectFirstSeenRow struct {
	SpaceID     string    `gorm:"column:c_space_id;primaryKey"`
	DatasetID   string    `gorm:"column:c_dataset_id;primaryKey"`
	SubjectID   string    `gorm:"column:c_subject_id;primaryKey"`
	FirstSeenAt time.Time `gorm:"column:c_first_seen_at"`
	Baseline    bool      `gorm:"column:c_baseline"`
}

func (subjectFirstSeenRow) TableName() string { return "t_monitor_subject_first_seen" }

// subjectDeleteChunk 控制一次 DELETE 的绑定参数个数，远低于 SQLite 的变量上限。
const subjectDeleteChunk = 200

// NewSubjectFirstSeenRepository 创建对象首次出现时间的仓储。
func NewSubjectFirstSeenRepository(store *Store) *SubjectFirstSeenRepository {
	return &SubjectFirstSeenRepository{store: store}
}

// ObserveSubjects 对账一个数据集当前的活跃对象，返回其中新上线对象的首次出现时间：
//   - 数据集还没有任何记录时（首次部署，或数据集刚出现），当前全部活跃对象记为存量，不给宽限；
//   - 之后第一次出现的对象记下 now；
//   - 已不再活跃的对象删除记录，下次重新上线时重新拿到宽限，表也不会无限增长。
//
// active 为空时不做任何修改：清单异常为空不能清空历史。
func (r *SubjectFirstSeenRepository) ObserveSubjects(ctx context.Context, spaceID, datasetID string, active map[string]struct{}, now time.Time) (map[string]time.Time, error) {
	if len(active) == 0 {
		return nil, nil
	}
	type result struct {
		fresh map[string]time.Time
		err   error
	}
	value, err := WithDatabase(r.store, func(db *gorm.DB) result {
		fresh := make(map[string]time.Time)
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var rows []subjectFirstSeenRow
			if err := tx.Where("c_space_id = ? AND c_dataset_id = ?", spaceID, datasetID).Find(&rows).Error; err != nil {
				return err
			}
			known := make(map[string]subjectFirstSeenRow, len(rows))
			for _, row := range rows {
				known[row.SubjectID] = row
			}
			baseline := len(rows) == 0
			var added []subjectFirstSeenRow
			for subjectID := range active {
				if _, ok := known[subjectID]; ok {
					continue
				}
				added = append(added, subjectFirstSeenRow{SpaceID: spaceID, DatasetID: datasetID, SubjectID: subjectID, FirstSeenAt: now.UTC(), Baseline: baseline})
			}
			if len(added) > 0 {
				if err := tx.CreateInBatches(&added, subjectDeleteChunk).Error; err != nil {
					return err
				}
			}
			var retired []string
			for subjectID, row := range known {
				if _, ok := active[subjectID]; !ok {
					retired = append(retired, subjectID)
				} else if !row.Baseline {
					fresh[subjectID] = row.FirstSeenAt.UTC()
				}
			}
			for _, row := range added {
				if !row.Baseline {
					fresh[row.SubjectID] = row.FirstSeenAt
				}
			}
			for start := 0; start < len(retired); start += subjectDeleteChunk {
				end := min(start+subjectDeleteChunk, len(retired))
				if err := tx.Where("c_space_id = ? AND c_dataset_id = ? AND c_subject_id IN ?", spaceID, datasetID, retired[start:end]).Delete(&subjectFirstSeenRow{}).Error; err != nil {
					return err
				}
			}
			return nil
		})
		return result{fresh: fresh, err: err}
	})
	if err != nil {
		return nil, fmt.Errorf("observe subject first seen: %w", err)
	}
	if value.err != nil {
		return nil, fmt.Errorf("observe subject first seen: %w", value.err)
	}
	return value.fresh, nil
}
