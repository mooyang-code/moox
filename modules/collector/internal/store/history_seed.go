package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 历史补齐的状态。preparing 表示记录已建、补采作业还在拼；running 表示作业已全部提交；
// done 是全部分块结束；skipped 是用户手动放行；failed 是拼作业连续失败后放弃（没补，下游放行）。
// preparing 与 running 期间下游不生效。
const (
	SeedPreparing = "preparing"
	SeedRunning   = "running"
	SeedDone      = "done"
	SeedSkipped   = "skipped"
	SeedFailed    = "failed"
)

// HistorySeed 是一个数据集（某个频率）首次接入时的历史补齐记录。
type HistorySeed struct {
	SpaceID   string
	DatasetID string
	Frequency string
	Status    string
	// TargetFrom 是补齐的目标起点，EffectiveFrom 是补齐起算的采集计划生效点（补齐范围是 [TargetFrom, EffectiveFrom)）。
	TargetFrom    time.Time
	EffectiveFrom time.Time
	// JobIDs 是补齐作业的 ID，Retried 表示失败分块已自动重试过一次。
	JobIDs     []string
	Retried    bool
	Note       string
	StartedAt  time.Time
	FinishedAt *time.Time
}

// Active 表示补齐还没结束，下游应当不生效。
func (s HistorySeed) Active() bool { return s.Status == SeedPreparing || s.Status == SeedRunning }

type historySeedRow struct {
	SpaceID       string `gorm:"column:c_space_id;primaryKey"`
	DatasetID     string `gorm:"column:c_dataset_id;primaryKey"`
	Frequency     string `gorm:"column:c_frequency;primaryKey"`
	Status        string `gorm:"column:c_status"`
	TargetFrom    int64  `gorm:"column:c_target_from"`
	EffectiveFrom int64  `gorm:"column:c_effective_from"`
	JobIDs        string `gorm:"column:c_job_ids"`
	Retried       bool   `gorm:"column:c_retried"`
	Note          string `gorm:"column:c_note"`
	StartedAt     int64  `gorm:"column:c_started_at"`
	FinishedAt    *int64 `gorm:"column:c_finished_at"`
}

func (historySeedRow) TableName() string { return "t_collector_history_seeds" }

func (row historySeedRow) seed() HistorySeed {
	seed := HistorySeed{
		SpaceID: row.SpaceID, DatasetID: row.DatasetID, Frequency: row.Frequency, Status: row.Status,
		TargetFrom: fromUnixMilli(row.TargetFrom), EffectiveFrom: fromUnixMilli(row.EffectiveFrom), Retried: row.Retried, Note: row.Note,
		StartedAt: fromUnixMilli(row.StartedAt), FinishedAt: fromUnixMilliPtr(row.FinishedAt),
	}
	_ = json.Unmarshal([]byte(row.JobIDs), &seed.JobIDs)
	return seed
}

func newHistorySeedRow(seed HistorySeed) (historySeedRow, error) {
	ids := seed.JobIDs
	if ids == nil {
		ids = []string{}
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return historySeedRow{}, err
	}
	row := historySeedRow{
		SpaceID: seed.SpaceID, DatasetID: seed.DatasetID, Frequency: seed.Frequency, Status: seed.Status,
		TargetFrom: toUnixMilli(seed.TargetFrom), EffectiveFrom: toUnixMilli(seed.EffectiveFrom), JobIDs: string(raw), Retried: seed.Retried,
		Note: seed.Note, StartedAt: toUnixMilli(seed.StartedAt), FinishedAt: toUnixMilliPtr(seed.FinishedAt),
	}
	return row, nil
}

// HistorySeeds 返回历史补齐记录，space 为空返回全部空间；onlyActive 只返回补齐中（preparing、running）的。
// 补齐中的排前，其余按开始时间从新到旧。
func (r *BackfillRepository) HistorySeeds(ctx context.Context, spaceID string, onlyActive bool) ([]HistorySeed, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx).Model(&historySeedRow{})
	if spaceID = strings.TrimSpace(spaceID); spaceID != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	if onlyActive {
		query = query.Where("c_status IN ?", []string{SeedPreparing, SeedRunning})
	}
	var rows []historySeedRow
	if err := query.Order("c_started_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]HistorySeed, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.seed())
	}
	// 稳定排序：补齐中的排前，其余顺序保持。
	active := make([]HistorySeed, 0, len(out))
	rest := make([]HistorySeed, 0, len(out))
	for _, seed := range out {
		if seed.Active() {
			active = append(active, seed)
		} else {
			rest = append(rest, seed)
		}
	}
	return append(active, rest...), nil
}

// HistorySeedOf 返回一个数据集的补齐记录，没有时 ok 为 false。
func (r *BackfillRepository) HistorySeedOf(ctx context.Context, spaceID, datasetID, frequency string) (HistorySeed, bool, error) {
	if err := r.ready(); err != nil {
		return HistorySeed{}, false, err
	}
	var row historySeedRow
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ?", spaceID, datasetID, frequency).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return HistorySeed{}, false, nil
	}
	if err != nil {
		return HistorySeed{}, false, err
	}
	return row.seed(), true, nil
}

// CreateHistorySeed 新建补齐记录；同一数据集同一频率已有记录时返回 false，不覆盖。
func (r *BackfillRepository) CreateHistorySeed(ctx context.Context, seed HistorySeed) (bool, error) {
	if err := r.ready(); err != nil {
		return false, err
	}
	if seed.SpaceID == "" || seed.DatasetID == "" || seed.Frequency == "" {
		return false, fmt.Errorf("history seed needs space, dataset and frequency")
	}
	row, err := newHistorySeedRow(seed)
	if err != nil {
		return false, err
	}
	result := r.db.WithContext(ctx).Exec(
		`INSERT INTO t_collector_history_seeds (c_space_id, c_dataset_id, c_frequency, c_status, c_target_from, c_effective_from, c_job_ids, c_retried, c_note, c_started_at, c_finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (c_space_id, c_dataset_id, c_frequency) DO NOTHING`,
		row.SpaceID, row.DatasetID, row.Frequency, row.Status, row.TargetFrom, row.EffectiveFrom, row.JobIDs, row.Retried, row.Note, row.StartedAt, row.FinishedAt)
	return result.RowsAffected > 0, result.Error
}

// UpdateHistorySeed 覆盖一条已有补齐记录的可变部分（状态、作业、重试标记、说明、结束时间）。from 非空时只在记录当前状态
// 属于 from 时才写（拼作业的协程、推进、手动放行各自改记录，条件写避免手动放行被拼作业的协程改回去），返回是否写入。
func (r *BackfillRepository) UpdateHistorySeed(ctx context.Context, seed HistorySeed, from ...string) (bool, error) {
	if err := r.ready(); err != nil {
		return false, err
	}
	row, err := newHistorySeedRow(seed)
	if err != nil {
		return false, err
	}
	query := r.db.WithContext(ctx).Model(&historySeedRow{}).
		Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ?", row.SpaceID, row.DatasetID, row.Frequency)
	if len(from) > 0 {
		query = query.Where("c_status IN ?", from)
	}
	result := query.Updates(map[string]any{"c_status": row.Status, "c_job_ids": row.JobIDs, "c_retried": row.Retried, "c_note": row.Note, "c_finished_at": row.FinishedAt, "c_mtime": nowUnixMilli()})
	return result.RowsAffected > 0, result.Error
}

// RetryHistorySeed 在单个事务内原子重试补齐下全部已失败的作业，并把补齐记录标记为已重试（c_retried = 1）。
// 若补齐记录不再处于 running 或已重试过，直接返回 false, nil；
// 若任一待重试作业尚未被 Tick 结束（仍为 pending 或 running），整个事务回滚并返回 ErrJobNotRetryable，
// 避免多作业场景下部分作业被提前重试、随后又因未置 c_retried 而反复重试。
func (r *BackfillRepository) RetryHistorySeed(ctx context.Context, spaceID, datasetID, frequency string, failedJobIDs []string) (bool, error) {
	if err := r.ready(); err != nil {
		return false, err
	}
	uniqueIDs := make([]string, 0, len(failedJobIDs))
	seen := make(map[string]struct{}, len(failedJobIDs))
	for _, id := range failedJobIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	var retried bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var seed historySeedRow
		find := tx.Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ?", spaceID, datasetID, frequency).Limit(1).Find(&seed)
		if find.Error != nil {
			return find.Error
		}
		if find.RowsAffected == 0 || seed.Status != SeedRunning || seed.Retried {
			return nil
		}
		var toRetry []string
		if len(uniqueIDs) > 0 {
			var jobs []backfillJobRow
			if err := tx.Where("c_job_id IN ?", uniqueIDs).Find(&jobs).Error; err != nil {
				return err
			}
			for _, job := range jobs {
				switch job.Status {
				case BackfillQueued, BackfillRunning:
					return ErrJobNotRetryable
				case BackfillFailed:
					toRetry = append(toRetry, job.JobID)
				}
			}
		}
		now := nowUnixMilli()
		if len(toRetry) > 0 {
			if err := tx.Model(&backfillJobRow{}).Where("c_job_id IN ? AND c_status = ?", toRetry, BackfillFailed).
				Updates(map[string]any{"c_status": BackfillQueued, "c_last_error": "", "c_mtime": now}).Error; err != nil {
				return err
			}
			if err := tx.Model(&backfillChunkRow{}).Where("c_job_id IN ? AND c_status = ?", toRetry, BackfillFailed).
				Updates(map[string]any{"c_status": BackfillQueued, "c_attempts": 0, "c_missing": "", "c_last_error": "", "c_started_at": nil, "c_latest_end_at": nil, "c_mtime": now}).Error; err != nil {
				return err
			}
		}
		updated := tx.Model(&historySeedRow{}).
			Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_status = ? AND c_retried = ?", spaceID, datasetID, frequency, SeedRunning, false).
			Updates(map[string]any{"c_retried": true, "c_mtime": now})
		if updated.Error != nil {
			return updated.Error
		}
		retried = updated.RowsAffected > 0
		return nil
	})
	return retried, err
}
