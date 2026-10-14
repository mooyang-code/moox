package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 补采作业与分块状态。
const (
	BackfillPending   = "pending"
	BackfillRunning   = "running"
	BackfillSucceeded = "succeeded"
	BackfillFailed    = "failed"
	BackfillCanceled  = "canceled"
)

// BackfillJob 是一次补采作业。
type BackfillJob struct {
	JobID     string
	SpaceID   string
	Source    string
	Request   string
	Status    string
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// BackfillChunk 是补采的最小执行单位：一个数据集、一个标的、一段连续时间（至多 1000 根）。
type BackfillChunk struct {
	ID          int64
	JobID       string
	DatasetID   string
	Frequency   string
	SubjectID   string
	From        time.Time
	To          time.Time
	ChunkID     string
	Status      string
	Attempts    int
	Rows        int
	StartedAt   *time.Time
	LatestEndAt *time.Time
	LastError   string
}

type backfillJobRow struct {
	ID        int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	JobID     string    `gorm:"column:c_job_id"`
	SpaceID   string    `gorm:"column:c_space_id"`
	Source    string    `gorm:"column:c_source"`
	Request   string    `gorm:"column:c_request"`
	Status    string    `gorm:"column:c_status"`
	LastError string    `gorm:"column:c_last_error"`
	CreatedAt time.Time `gorm:"column:c_ctime"`
	UpdatedAt time.Time `gorm:"column:c_mtime"`
}

func (backfillJobRow) TableName() string { return "t_collector_backfill_jobs" }

type backfillChunkRow struct {
	ID          int64      `gorm:"column:c_id;primaryKey;autoIncrement"`
	JobID       string     `gorm:"column:c_job_id"`
	DatasetID   string     `gorm:"column:c_dataset_id"`
	Frequency   string     `gorm:"column:c_frequency"`
	SubjectID   string     `gorm:"column:c_subject_id"`
	From        time.Time  `gorm:"column:c_from"`
	To          time.Time  `gorm:"column:c_to"`
	ChunkID     string     `gorm:"column:c_chunk_id"`
	Status      string     `gorm:"column:c_status"`
	Attempts    int        `gorm:"column:c_attempts"`
	Rows        int        `gorm:"column:c_rows"`
	StartedAt   *time.Time `gorm:"column:c_started_at"`
	LatestEndAt *time.Time `gorm:"column:c_latest_end_at"`
	LastError   string     `gorm:"column:c_last_error"`
	UpdatedAt   time.Time  `gorm:"column:c_mtime"`
}

func (backfillChunkRow) TableName() string { return "t_collector_backfill_chunks" }

type backfillWatermarkRow struct {
	SpaceID      string    `gorm:"column:c_space_id;primaryKey"`
	DatasetID    string    `gorm:"column:c_dataset_id;primaryKey"`
	Frequency    string    `gorm:"column:c_frequency;primaryKey"`
	ScannedUntil time.Time `gorm:"column:c_scanned_until"`
	UpdatedAt    time.Time `gorm:"column:c_mtime"`
}

func (backfillWatermarkRow) TableName() string { return "t_collector_backfill_watermarks" }

func (row backfillChunkRow) chunk() BackfillChunk {
	return BackfillChunk{ID: row.ID, JobID: row.JobID, DatasetID: row.DatasetID, Frequency: row.Frequency, SubjectID: row.SubjectID, From: row.From, To: row.To, ChunkID: row.ChunkID, Status: row.Status, Attempts: row.Attempts, Rows: row.Rows, StartedAt: row.StartedAt, LatestEndAt: row.LatestEndAt, LastError: row.LastError}
}

// BackfillRepository 保存补采作业、分块与自动补采的扫描水位。
type BackfillRepository struct{ db *gorm.DB }

func NewBackfillRepository(db *gorm.DB) *BackfillRepository { return &BackfillRepository{db: db} }

// CreateJob 在一个事务中保存作业与全部分块。
func (r *BackfillRepository) CreateJob(ctx context.Context, job BackfillJob, chunks []BackfillChunk) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("backfill repository is not initialized")
	}
	if strings.TrimSpace(job.JobID) == "" || strings.TrimSpace(job.SpaceID) == "" || len(chunks) == 0 {
		return fmt.Errorf("job_id, space_id and chunks are required")
	}
	if job.Source == "" {
		job.Source = "manual"
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := backfillJobRow{JobID: job.JobID, SpaceID: job.SpaceID, Source: job.Source, Request: job.Request, Status: BackfillPending, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		rows := make([]backfillChunkRow, 0, len(chunks))
		for _, chunk := range chunks {
			rows = append(rows, backfillChunkRow{JobID: job.JobID, DatasetID: chunk.DatasetID, Frequency: chunk.Frequency, SubjectID: chunk.SubjectID, From: chunk.From.UTC(), To: chunk.To.UTC(), ChunkID: chunk.ChunkID, Status: BackfillPending, UpdatedAt: now})
		}
		return tx.CreateInBatches(&rows, 200).Error
	})
}

// GetJob 返回作业及其分块。
func (r *BackfillRepository) GetJob(ctx context.Context, jobID string) (*BackfillJob, []BackfillChunk, error) {
	if r == nil || r.db == nil {
		return nil, nil, fmt.Errorf("backfill repository is not initialized")
	}
	var row backfillJobRow
	result := r.db.WithContext(ctx).Where("c_job_id = ?", strings.TrimSpace(jobID)).Limit(1).Find(&row)
	if result.Error != nil {
		return nil, nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil, nil
	}
	var chunkRows []backfillChunkRow
	if err := r.db.WithContext(ctx).Where("c_job_id = ?", row.JobID).Order("c_id ASC").Find(&chunkRows).Error; err != nil {
		return nil, nil, err
	}
	chunks := make([]BackfillChunk, 0, len(chunkRows))
	for _, chunkRow := range chunkRows {
		chunks = append(chunks, chunkRow.chunk())
	}
	return &BackfillJob{JobID: row.JobID, SpaceID: row.SpaceID, Source: row.Source, Request: row.Request, Status: row.Status, LastError: row.LastError, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, chunks, nil
}

// ActiveJobs 返回该空间未结束的作业（pending 或 running）。
func (r *BackfillRepository) ActiveJobs(ctx context.Context, spaceID string) ([]BackfillJob, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("backfill repository is not initialized")
	}
	var rows []backfillJobRow
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_status IN ?", strings.TrimSpace(spaceID), []string{BackfillPending, BackfillRunning}).Order("c_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	jobs := make([]BackfillJob, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, BackfillJob{JobID: row.JobID, SpaceID: row.SpaceID, Source: row.Source, Request: row.Request, Status: row.Status, LastError: row.LastError, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
	}
	return jobs, nil
}

// SetJobStatus 更新作业状态。
func (r *BackfillRepository) SetJobStatus(ctx context.Context, jobID, status, lastError string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("backfill repository is not initialized")
	}
	// 已取消的作业不会被改回运行中。
	return r.db.WithContext(ctx).Model(&backfillJobRow{}).Where("c_job_id = ? AND c_status <> ?", jobID, BackfillCanceled).Updates(map[string]any{"c_status": status, "c_last_error": lastError, "c_mtime": time.Now().UTC()}).Error
}

// CancelJob 取消作业：把尚未开始的分块标记为取消；运行中的分块等其结束。
func (r *BackfillRepository) CancelJob(ctx context.Context, jobID string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("backfill repository is not initialized")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := tx.Model(&backfillChunkRow{}).Where("c_job_id = ? AND c_status = ?", jobID, BackfillPending).Updates(map[string]any{"c_status": BackfillCanceled, "c_mtime": now}).Error; err != nil {
			return err
		}
		return tx.Model(&backfillJobRow{}).Where("c_job_id = ? AND c_status IN ?", jobID, []string{BackfillPending, BackfillRunning}).Updates(map[string]any{"c_status": BackfillCanceled, "c_mtime": now}).Error
	})
}

// ClaimChunks 取出至多 limit 个可执行的分块并标记为 running（记录开始时间与最迟结束时间）。
// 仍在 running 且未超过最迟结束时间的分块视为占用名额，不会被重复取出。
func (r *BackfillRepository) ClaimChunks(ctx context.Context, jobID string, limit int, latestEnd time.Time) ([]BackfillChunk, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("backfill repository is not initialized")
	}
	if limit <= 0 {
		return nil, nil
	}
	var claimed []BackfillChunk
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		var rows []backfillChunkRow
		if err := tx.Where("c_job_id = ? AND (c_status = ? OR (c_status = ? AND c_latest_end_at IS NOT NULL AND c_latest_end_at < ?))", jobID, BackfillPending, BackfillRunning, now).Order("c_id ASC").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := tx.Model(&backfillChunkRow{}).Where("c_id = ?", row.ID).Updates(map[string]any{"c_status": BackfillRunning, "c_attempts": row.Attempts + 1, "c_started_at": now, "c_latest_end_at": latestEnd.UTC(), "c_mtime": now}).Error; err != nil {
				return err
			}
			row.Status, row.Attempts, row.StartedAt = BackfillRunning, row.Attempts+1, &now
			end := latestEnd.UTC()
			row.LatestEndAt = &end
			claimed = append(claimed, row.chunk())
		}
		return nil
	})
	return claimed, err
}

// InFlightChunks 返回全局正在占用名额的分块数（running 且未超过最迟结束时间）。
func (r *BackfillRepository) InFlightChunks(ctx context.Context, spaceID string) (int, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("backfill repository is not initialized")
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&backfillChunkRow{}).
		Joins("JOIN t_collector_backfill_jobs ON t_collector_backfill_jobs.c_job_id = t_collector_backfill_chunks.c_job_id").
		Where("t_collector_backfill_jobs.c_space_id = ? AND t_collector_backfill_chunks.c_status = ? AND t_collector_backfill_chunks.c_latest_end_at >= ?", strings.TrimSpace(spaceID), BackfillRunning, time.Now().UTC()).Count(&count).Error
	return int(count), err
}

// ErrChunkLost 表示分块已被重新认领或取消，当前尝试不再拥有它。
var ErrChunkLost = fmt.Errorf("backfill chunk is no longer owned by this attempt")

// chunkOwned 限定只有仍持有该次尝试（尝试号一致且运行中）的调用才能改动分块，
// 过期的旧尝试不会覆盖新尝试的状态。
func (r *BackfillRepository) chunkOwned(ctx context.Context, id int64, attempt int) *gorm.DB {
	return r.db.WithContext(ctx).Model(&backfillChunkRow{}).Where("c_id = ? AND c_attempts = ? AND c_status = ?", id, attempt, BackfillRunning)
}

// FinishChunk 记录分块的结果；分块已不属于该次尝试时返回 ErrChunkLost。
func (r *BackfillRepository) FinishChunk(ctx context.Context, id int64, attempt int, status string, rows int, lastError string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("backfill repository is not initialized")
	}
	result := r.chunkOwned(ctx, id, attempt).Updates(map[string]any{"c_status": status, "c_rows": rows, "c_last_error": lastError, "c_mtime": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrChunkLost
	}
	return nil
}

// RenewChunk 在真正调用函数之前把名额有效期续到新的最迟结束时间。
func (r *BackfillRepository) RenewChunk(ctx context.Context, id int64, attempt int, latestEnd time.Time) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("backfill repository is not initialized")
	}
	result := r.chunkOwned(ctx, id, attempt).Updates(map[string]any{"c_latest_end_at": latestEnd.UTC(), "c_mtime": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrChunkLost
	}
	return nil
}

// ReleaseChunk 把还没真正执行的分块放回待认领，并退回这次尝试（等待函数就绪不算执行失败）。
func (r *BackfillRepository) ReleaseChunk(ctx context.Context, id int64, attempt int) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("backfill repository is not initialized")
	}
	result := r.chunkOwned(ctx, id, attempt).Updates(map[string]any{"c_status": BackfillPending, "c_attempts": attempt - 1, "c_started_at": nil, "c_latest_end_at": nil, "c_mtime": time.Now().UTC()})
	return result.Error
}

// ChunkStatusCounts 返回作业各状态的分块数。
func (r *BackfillRepository) ChunkStatusCounts(ctx context.Context, jobID string) (map[string]int, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("backfill repository is not initialized")
	}
	type countRow struct {
		Status string `gorm:"column:c_status"`
		Count  int    `gorm:"column:n"`
	}
	var rows []countRow
	if err := r.db.WithContext(ctx).Model(&backfillChunkRow{}).Select("c_status, COUNT(*) AS n").Where("c_job_id = ?", jobID).Group("c_status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.Status] = row.Count
	}
	return counts, nil
}

// Watermark 返回自动补采的扫描水位；没有则返回零值。
func (r *BackfillRepository) Watermark(ctx context.Context, spaceID, datasetID, frequency string) (time.Time, error) {
	if r == nil || r.db == nil {
		return time.Time{}, fmt.Errorf("backfill repository is not initialized")
	}
	var row backfillWatermarkRow
	result := r.db.WithContext(ctx).Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ?", spaceID, datasetID, frequency).Limit(1).Find(&row)
	if result.Error != nil {
		return time.Time{}, result.Error
	}
	if result.RowsAffected == 0 {
		return time.Time{}, nil
	}
	return row.ScannedUntil, nil
}

// SetWatermark 推进扫描水位（只前进，不后退）。
func (r *BackfillRepository) SetWatermark(ctx context.Context, spaceID, datasetID, frequency string, scannedUntil time.Time) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("backfill repository is not initialized")
	}
	row := backfillWatermarkRow{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, ScannedUntil: scannedUntil.UTC(), UpdatedAt: time.Now().UTC()}
	return r.db.WithContext(ctx).Exec(`INSERT INTO t_collector_backfill_watermarks (c_space_id, c_dataset_id, c_frequency, c_scanned_until, c_mtime) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (c_space_id, c_dataset_id, c_frequency) DO UPDATE SET c_scanned_until = MAX(c_scanned_until, excluded.c_scanned_until), c_mtime = excluded.c_mtime`,
		row.SpaceID, row.DatasetID, row.Frequency, row.ScannedUntil, row.UpdatedAt).Error
}

// Reset 清掉该空间全部补采作业、分块与自动补采水位，返回清掉的作业数。用于 Storage 数据被清空之后：
// 旧作业指向已经不存在的数据，水位不清会让自动补采沿用旧位置。运行中的分块写回状态时找不到行，直接放弃。
func (r *BackfillRepository) Reset(ctx context.Context, spaceID string) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("backfill repository is not initialized")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return 0, fmt.Errorf("space_id is required")
	}
	var jobs int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		jobIDs := tx.Model(&backfillJobRow{}).Select("c_job_id").Where("c_space_id = ?", spaceID)
		if err := tx.Where("c_job_id IN (?)", jobIDs).Delete(&backfillChunkRow{}).Error; err != nil {
			return err
		}
		result := tx.Where("c_space_id = ?", spaceID).Delete(&backfillJobRow{})
		if result.Error != nil {
			return result.Error
		}
		jobs = result.RowsAffected
		return tx.Where("c_space_id = ?", spaceID).Delete(&backfillWatermarkRow{}).Error
	})
	return jobs, err
}

// ListJobs 按创建时间倒序分页返回该空间的补采作业，并返回总数。
func (r *BackfillRepository) ListJobs(ctx context.Context, spaceID string, page, size int) ([]BackfillJob, int64, error) {
	if r == nil || r.db == nil {
		return nil, 0, fmt.Errorf("backfill repository is not initialized")
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	query := r.db.WithContext(ctx).Model(&backfillJobRow{}).Where("c_space_id = ?", strings.TrimSpace(spaceID))
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []backfillJobRow
	if err := query.Order("c_id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	jobs := make([]BackfillJob, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, BackfillJob{JobID: row.JobID, SpaceID: row.SpaceID, Source: row.Source, Request: row.Request, Status: row.Status, LastError: row.LastError, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
	}
	return jobs, total, nil
}
