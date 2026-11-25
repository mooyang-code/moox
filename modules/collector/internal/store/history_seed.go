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
// done 是全部分块结束；skipped 是用户手动放行。preparing 与 running 期间下游不生效。
const (
	SeedPreparing = "preparing"
	SeedRunning   = "running"
	SeedDone      = "done"
	SeedSkipped   = "skipped"
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
	SpaceID       string     `gorm:"column:c_space_id;primaryKey"`
	DatasetID     string     `gorm:"column:c_dataset_id;primaryKey"`
	Frequency     string     `gorm:"column:c_frequency;primaryKey"`
	Status        string     `gorm:"column:c_status"`
	TargetFrom    time.Time  `gorm:"column:c_target_from"`
	EffectiveFrom time.Time  `gorm:"column:c_effective_from"`
	JobIDs        string     `gorm:"column:c_job_ids"`
	Retried       bool       `gorm:"column:c_retried"`
	Note          string     `gorm:"column:c_note"`
	StartedAt     time.Time  `gorm:"column:c_started_at"`
	FinishedAt    *time.Time `gorm:"column:c_finished_at"`
}

func (historySeedRow) TableName() string { return "t_collector_history_seeds" }

func (row historySeedRow) seed() HistorySeed {
	seed := HistorySeed{
		SpaceID: row.SpaceID, DatasetID: row.DatasetID, Frequency: row.Frequency, Status: row.Status,
		TargetFrom: row.TargetFrom.UTC(), EffectiveFrom: row.EffectiveFrom.UTC(), Retried: row.Retried, Note: row.Note,
		StartedAt: row.StartedAt.UTC(), FinishedAt: row.FinishedAt,
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
		TargetFrom: seed.TargetFrom.UTC(), EffectiveFrom: seed.EffectiveFrom.UTC(), JobIDs: string(raw), Retried: seed.Retried,
		Note: seed.Note, StartedAt: seed.StartedAt.UTC(),
	}
	if seed.FinishedAt != nil {
		finished := seed.FinishedAt.UTC()
		row.FinishedAt = &finished
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
	result := query.Updates(map[string]any{"c_status": row.Status, "c_job_ids": row.JobIDs, "c_retried": row.Retried, "c_note": row.Note, "c_finished_at": row.FinishedAt, "c_mtime": time.Now().UTC()})
	return result.RowsAffected > 0, result.Error
}
