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

// 补采作业的来源与类型。
const (
	BackfillSourceManual = "manual"
	BackfillSourceAuto   = "auto"

	// BackfillKindGap 按缺口补采；BackfillKindForce 不检查缺口、整段重拉；BackfillKindHistory 确认历史扫描发现的缺口。
	BackfillKindGap     = "gap"
	BackfillKindForce   = "force"
	BackfillKindHistory = "history"
)

// BackfillJob 是一次补采作业。DatasetID、Frequency、SubjectID 为空表示全部。
type BackfillJob struct {
	JobID     string
	SpaceID   string
	Source    string
	Kind      string
	DatasetID string
	Frequency string
	SubjectID string
	From      time.Time
	To        time.Time
	// AdvanceWatermark 为 true 表示自动补采作业：作业全部成功后才把该数据集的扫描水位推进到 To。
	AdvanceWatermark bool
	Status           string
	LastError        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// BackfillChunk 是补采的最小执行单位：一个数据集、一个标的、一段连续时间（至多 1000 根）。
type BackfillChunk struct {
	ID        int64
	JobID     string
	DatasetID string
	Frequency string
	SubjectID string
	From      time.Time
	To        time.Time
	ChunkID   string
	Status    string
	Attempts  int
	Rows      int
	// Missing 是上一次执行后复查仍缺的 K 线（规范化区间串），用于判定“行情源没有”。
	Missing     string
	StartedAt   *time.Time
	LatestEndAt *time.Time
	LastError   string
	UpdatedAt   time.Time
}

// BackfillJobFilter 是作业列表的筛选条件。
type BackfillJobFilter struct {
	Source string
	// Attention 只返回未结束或有失败的作业。
	Attention bool
	DatasetID string
}

type backfillJobRow struct {
	ID               int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	JobID            string    `gorm:"column:c_job_id"`
	SpaceID          string    `gorm:"column:c_space_id"`
	Source           string    `gorm:"column:c_source"`
	Kind             string    `gorm:"column:c_kind"`
	DatasetID        string    `gorm:"column:c_dataset_id"`
	Frequency        string    `gorm:"column:c_frequency"`
	SubjectID        string    `gorm:"column:c_subject_id"`
	From             time.Time `gorm:"column:c_from"`
	To               time.Time `gorm:"column:c_to"`
	AdvanceWatermark bool      `gorm:"column:c_advance_watermark"`
	Status           string    `gorm:"column:c_status"`
	LastError        string    `gorm:"column:c_last_error"`
	CreatedAt        time.Time `gorm:"column:c_ctime"`
	UpdatedAt        time.Time `gorm:"column:c_mtime"`
}

func (backfillJobRow) TableName() string { return "t_collector_kline_backfill_jobs" }

func (row backfillJobRow) job() BackfillJob {
	return BackfillJob{
		JobID: row.JobID, SpaceID: row.SpaceID, Source: row.Source, Kind: row.Kind, DatasetID: row.DatasetID, Frequency: row.Frequency, SubjectID: row.SubjectID,
		From: row.From, To: row.To, AdvanceWatermark: row.AdvanceWatermark, Status: row.Status, LastError: row.LastError, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

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
	Missing     string     `gorm:"column:c_missing"`
	StartedAt   *time.Time `gorm:"column:c_started_at"`
	LatestEndAt *time.Time `gorm:"column:c_latest_end_at"`
	LastError   string     `gorm:"column:c_last_error"`
	UpdatedAt   time.Time  `gorm:"column:c_mtime"`
}

func (backfillChunkRow) TableName() string { return "t_collector_kline_backfill_chunks" }

func (row backfillChunkRow) chunk() BackfillChunk {
	return BackfillChunk{
		ID: row.ID, JobID: row.JobID, DatasetID: row.DatasetID, Frequency: row.Frequency, SubjectID: row.SubjectID, From: row.From, To: row.To, ChunkID: row.ChunkID,
		Status: row.Status, Attempts: row.Attempts, Rows: row.Rows, Missing: row.Missing, StartedAt: row.StartedAt, LatestEndAt: row.LatestEndAt, LastError: row.LastError, UpdatedAt: row.UpdatedAt,
	}
}

type backfillWatermarkRow struct {
	SpaceID      string    `gorm:"column:c_space_id;primaryKey"`
	DatasetID    string    `gorm:"column:c_dataset_id;primaryKey"`
	Frequency    string    `gorm:"column:c_frequency;primaryKey"`
	ScannedUntil time.Time `gorm:"column:c_scanned_until"`
	UpdatedAt    time.Time `gorm:"column:c_mtime"`
}

func (backfillWatermarkRow) TableName() string { return "t_collector_backfill_watermarks" }

const (
	backfillJobsTable   = "t_collector_kline_backfill_jobs"
	backfillChunksTable = "t_collector_kline_backfill_chunks"
)

// BackfillRepository 保存补采作业、分块、自动补采水位、已确认的行情源缺失与历史扫描结果。
type BackfillRepository struct{ db *gorm.DB }

func NewBackfillRepository(db *gorm.DB) *BackfillRepository { return &BackfillRepository{db: db} }

func (r *BackfillRepository) ready() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("backfill repository is not initialized")
	}
	return nil
}

// CreateJob 在一个事务中保存作业与全部分块。
func (r *BackfillRepository) CreateJob(ctx context.Context, job BackfillJob, chunks []BackfillChunk) error {
	if err := r.ready(); err != nil {
		return err
	}
	if strings.TrimSpace(job.JobID) == "" || strings.TrimSpace(job.SpaceID) == "" || len(chunks) == 0 {
		return fmt.Errorf("job_id, space_id and chunks are required")
	}
	if job.From.IsZero() || job.To.IsZero() {
		return fmt.Errorf("job range is required")
	}
	if job.Source == "" {
		job.Source = BackfillSourceManual
	}
	if job.Kind == "" {
		job.Kind = BackfillKindGap
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := backfillJobRow{
			JobID: job.JobID, SpaceID: job.SpaceID, Source: job.Source, Kind: job.Kind, DatasetID: job.DatasetID, Frequency: job.Frequency, SubjectID: job.SubjectID,
			From: job.From.UTC(), To: job.To.UTC(), AdvanceWatermark: job.AdvanceWatermark, Status: BackfillPending, CreatedAt: now, UpdatedAt: now,
		}
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

// GetJob 返回作业及其分块；作业不存在时返回 nil。
func (r *BackfillRepository) GetJob(ctx context.Context, jobID string) (*BackfillJob, []BackfillChunk, error) {
	if err := r.ready(); err != nil {
		return nil, nil, err
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
	job := row.job()
	return &job, chunks, nil
}

// ActiveJobs 返回该空间未结束的作业（pending 或 running）。
func (r *BackfillRepository) ActiveJobs(ctx context.Context, spaceID string) ([]BackfillJob, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	var rows []backfillJobRow
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_status IN ?", strings.TrimSpace(spaceID), []string{BackfillPending, BackfillRunning}).Order("c_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return jobsOf(rows), nil
}

func jobsOf(rows []backfillJobRow) []BackfillJob {
	jobs := make([]BackfillJob, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, row.job())
	}
	return jobs
}

// SetJobStatus 更新作业状态。
func (r *BackfillRepository) SetJobStatus(ctx context.Context, jobID, status, lastError string) error {
	if err := r.ready(); err != nil {
		return err
	}
	// 已取消的作业不会被改回运行中。
	return r.db.WithContext(ctx).Model(&backfillJobRow{}).Where("c_job_id = ? AND c_status <> ?", jobID, BackfillCanceled).Updates(map[string]any{"c_status": status, "c_last_error": lastError, "c_mtime": time.Now().UTC()}).Error
}

// CancelJob 取消作业：把尚未开始的分块标记为取消；运行中的分块等其结束。
func (r *BackfillRepository) CancelJob(ctx context.Context, jobID string) error {
	if err := r.ready(); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := tx.Model(&backfillChunkRow{}).Where("c_job_id = ? AND c_status = ?", jobID, BackfillPending).Updates(map[string]any{"c_status": BackfillCanceled, "c_mtime": now}).Error; err != nil {
			return err
		}
		return tx.Model(&backfillJobRow{}).Where("c_job_id = ? AND c_status IN ?", jobID, []string{BackfillPending, BackfillRunning}).Updates(map[string]any{"c_status": BackfillCanceled, "c_mtime": now}).Error
	})
}

// ErrJobNotRetryable 表示作业不是“已结束且有失败分块”的状态，不能重试。
var ErrJobNotRetryable = fmt.Errorf("only a failed backfill job can be retried")

// RetryJob 把已失败作业的失败分块退回待执行（尝试次数与上次缺失清零），作业改回等待中；返回退回的分块数。
func (r *BackfillRepository) RetryJob(ctx context.Context, jobID string) (int, error) {
	if err := r.ready(); err != nil {
		return 0, err
	}
	var retried int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		result := tx.Model(&backfillJobRow{}).Where("c_job_id = ? AND c_status = ?", jobID, BackfillFailed).Updates(map[string]any{"c_status": BackfillPending, "c_last_error": "", "c_mtime": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrJobNotRetryable
		}
		chunks := tx.Model(&backfillChunkRow{}).Where("c_job_id = ? AND c_status = ?", jobID, BackfillFailed).
			Updates(map[string]any{"c_status": BackfillPending, "c_attempts": 0, "c_missing": "", "c_last_error": "", "c_started_at": nil, "c_latest_end_at": nil, "c_mtime": now})
		retried = chunks.RowsAffected
		return chunks.Error
	})
	return int(retried), err
}

// ClaimChunks 取出至多 limit 个可执行的分块并标记为 running（记录开始时间与最迟结束时间）。
// 仍在 running 且未超过最迟结束时间的分块视为占用名额，不会被重复取出。
func (r *BackfillRepository) ClaimChunks(ctx context.Context, jobID string, limit int, latestEnd time.Time) ([]BackfillChunk, error) {
	if err := r.ready(); err != nil {
		return nil, err
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

// InFlightChunks 返回该空间正在占用名额的分块数（running 且未超过最迟结束时间）。
func (r *BackfillRepository) InFlightChunks(ctx context.Context, spaceID string) (int, error) {
	if err := r.ready(); err != nil {
		return 0, err
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&backfillChunkRow{}).
		Joins("JOIN "+backfillJobsTable+" ON "+backfillJobsTable+".c_job_id = "+backfillChunksTable+".c_job_id").
		Where(backfillJobsTable+".c_space_id = ? AND "+backfillChunksTable+".c_status = ? AND "+backfillChunksTable+".c_latest_end_at >= ?", strings.TrimSpace(spaceID), BackfillRunning, time.Now().UTC()).Count(&count).Error
	return int(count), err
}

// ErrChunkLost 表示分块已被重新认领或取消，当前尝试不再拥有它。
var ErrChunkLost = fmt.Errorf("backfill chunk is no longer owned by this attempt")

// chunkOwned 限定只有仍持有该次尝试（尝试号一致且运行中）的调用才能改动分块，
// 过期的旧尝试不会覆盖新尝试的状态。
func (r *BackfillRepository) chunkOwned(ctx context.Context, id int64, attempt int) *gorm.DB {
	return r.db.WithContext(ctx).Model(&backfillChunkRow{}).Where("c_id = ? AND c_attempts = ? AND c_status = ?", id, attempt, BackfillRunning)
}

func ownedResult(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrChunkLost
	}
	return nil
}

// FinishChunk 记录分块的结果；分块已不属于该次尝试时返回 ErrChunkLost。
func (r *BackfillRepository) FinishChunk(ctx context.Context, id int64, attempt int, status string, rows int, lastError string) error {
	if err := r.ready(); err != nil {
		return err
	}
	return ownedResult(r.chunkOwned(ctx, id, attempt).Updates(map[string]any{"c_status": status, "c_rows": rows, "c_last_error": lastError, "c_mtime": time.Now().UTC()}))
}

// RequeueChunk 把执行完但复查仍缺的分块退回待执行，记下这次仍缺的 K 线，尝试次数照常累计。
func (r *BackfillRepository) RequeueChunk(ctx context.Context, id int64, attempt int, missing, note string) error {
	if err := r.ready(); err != nil {
		return err
	}
	return ownedResult(r.chunkOwned(ctx, id, attempt).Updates(map[string]any{"c_status": BackfillPending, "c_missing": missing, "c_last_error": note, "c_started_at": nil, "c_latest_end_at": nil, "c_mtime": time.Now().UTC()}))
}

// RenewChunk 在真正调用函数之前把名额有效期续到新的最迟结束时间。
func (r *BackfillRepository) RenewChunk(ctx context.Context, id int64, attempt int, latestEnd time.Time) error {
	if err := r.ready(); err != nil {
		return err
	}
	return ownedResult(r.chunkOwned(ctx, id, attempt).Updates(map[string]any{"c_latest_end_at": latestEnd.UTC(), "c_mtime": time.Now().UTC()}))
}

// ReleaseChunk 把还没真正执行的分块放回待认领，并退回这次尝试（等待函数就绪不算执行失败）。
func (r *BackfillRepository) ReleaseChunk(ctx context.Context, id int64, attempt int) error {
	if err := r.ready(); err != nil {
		return err
	}
	return r.chunkOwned(ctx, id, attempt).Updates(map[string]any{"c_status": BackfillPending, "c_attempts": attempt - 1, "c_started_at": nil, "c_latest_end_at": nil, "c_mtime": time.Now().UTC()}).Error
}

// ChunkStatusCounts 返回作业各状态的分块数。
func (r *BackfillRepository) ChunkStatusCounts(ctx context.Context, jobID string) (map[string]int, error) {
	counts, err := r.ChunkStatusCountsOf(ctx, []string{jobID})
	if err != nil {
		return nil, err
	}
	if counts[jobID] == nil {
		return map[string]int{}, nil
	}
	return counts[jobID], nil
}

// ChunkStatusCountsOf 一次返回多个作业各状态的分块数。
func (r *BackfillRepository) ChunkStatusCountsOf(ctx context.Context, jobIDs []string) (map[string]map[string]int, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]int, len(jobIDs))
	if len(jobIDs) == 0 {
		return out, nil
	}
	type countRow struct {
		JobID  string `gorm:"column:c_job_id"`
		Status string `gorm:"column:c_status"`
		Count  int    `gorm:"column:n"`
	}
	var rows []countRow
	if err := r.db.WithContext(ctx).Model(&backfillChunkRow{}).Select("c_job_id, c_status, COUNT(*) AS n").Where("c_job_id IN ?", jobIDs).Group("c_job_id, c_status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if out[row.JobID] == nil {
			out[row.JobID] = map[string]int{}
		}
		out[row.JobID][row.Status] = row.Count
	}
	return out, nil
}

// ListJobs 按创建时间倒序分页返回该空间符合筛选的补采作业，并返回总数。
func (r *BackfillRepository) ListJobs(ctx context.Context, spaceID string, filter BackfillJobFilter, page, size int) ([]BackfillJob, int64, error) {
	if err := r.ready(); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	query := r.db.WithContext(ctx).Model(&backfillJobRow{}).Where("c_space_id = ?", strings.TrimSpace(spaceID))
	if filter.Source != "" {
		query = query.Where("c_source = ?", filter.Source)
	}
	if filter.Attention {
		query = query.Where("c_status IN ?", []string{BackfillPending, BackfillRunning, BackfillFailed})
	}
	if filter.DatasetID != "" {
		// 不限数据集的手动作业也覆盖该数据集。
		query = query.Where("c_dataset_id IN ?", []string{filter.DatasetID, ""})
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []backfillJobRow
	if err := query.Order("c_id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return jobsOf(rows), total, nil
}

// BackfillJobCounts 是空间补采作业的汇总：未结束的作业数与 since 之后结束为失败的作业数。
type BackfillJobCounts struct {
	Active int
	Failed int
}

// JobCounts 返回空间的补采作业汇总。
func (r *BackfillRepository) JobCounts(ctx context.Context, spaceID string, since time.Time) (BackfillJobCounts, error) {
	if err := r.ready(); err != nil {
		return BackfillJobCounts{}, err
	}
	var active, failed int64
	db := r.db.WithContext(ctx).Model(&backfillJobRow{})
	if err := db.Where("c_space_id = ? AND c_status IN ?", spaceID, []string{BackfillPending, BackfillRunning}).Count(&active).Error; err != nil {
		return BackfillJobCounts{}, err
	}
	if err := r.db.WithContext(ctx).Model(&backfillJobRow{}).Where("c_space_id = ? AND c_status = ? AND c_mtime >= ?", spaceID, BackfillFailed, since.UTC()).Count(&failed).Error; err != nil {
		return BackfillJobCounts{}, err
	}
	return BackfillJobCounts{Active: int(active), Failed: int(failed)}, nil
}

// AutoChunksSince 返回 since 之后创建的自动补采作业的全部分块（实时采集漏掉的 K 线），至多 limit 个。
func (r *BackfillRepository) AutoChunksSince(ctx context.Context, spaceID string, since time.Time, limit int) ([]BackfillChunk, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	jobIDs := r.db.Model(&backfillJobRow{}).Select("c_job_id").Where("c_space_id = ? AND c_source = ? AND c_ctime >= ?", spaceID, BackfillSourceAuto, since.UTC())
	var rows []backfillChunkRow
	if err := r.db.WithContext(ctx).Where("c_job_id IN (?)", jobIDs).Order("c_id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	chunks := make([]BackfillChunk, 0, len(rows))
	for _, row := range rows {
		chunks = append(chunks, row.chunk())
	}
	return chunks, nil
}

// FailedChunkCounts 返回 since 之后失败的分块数，按“数据集|频率”分组。
func (r *BackfillRepository) FailedChunkCounts(ctx context.Context, spaceID string, since time.Time) (map[string]int, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	type countRow struct {
		DatasetID string `gorm:"column:c_dataset_id"`
		Frequency string `gorm:"column:c_frequency"`
		Count     int    `gorm:"column:n"`
	}
	jobIDs := r.db.Model(&backfillJobRow{}).Select("c_job_id").Where("c_space_id = ?", spaceID)
	var rows []countRow
	if err := r.db.WithContext(ctx).Model(&backfillChunkRow{}).Select("c_dataset_id, c_frequency, COUNT(*) AS n").
		Where("c_status = ? AND c_mtime >= ? AND c_job_id IN (?)", BackfillFailed, since.UTC(), jobIDs).Group("c_dataset_id, c_frequency").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, row := range rows {
		out[row.DatasetID+"|"+row.Frequency] = row.Count
	}
	return out, nil
}

// CleanupFinishedJobs 删除已结束且超过保留期的作业及其分块：自动作业早于 autoBefore、手动作业早于 manualBefore。
// 每次至多删除 limit 个作业，返回删除的作业数。
func (r *BackfillRepository) CleanupFinishedJobs(ctx context.Context, spaceID string, autoBefore, manualBefore time.Time, limit int) (int64, error) {
	if err := r.ready(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, nil
	}
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var jobIDs []string
		if err := tx.Model(&backfillJobRow{}).Select("c_job_id").
			Where("c_space_id = ? AND c_status IN ? AND ((c_source = ? AND c_mtime < ?) OR (c_source = ? AND c_mtime < ?))",
				spaceID, []string{BackfillSucceeded, BackfillFailed, BackfillCanceled}, BackfillSourceAuto, autoBefore.UTC(), BackfillSourceManual, manualBefore.UTC()).
			Order("c_mtime ASC").Limit(limit).Pluck("c_job_id", &jobIDs).Error; err != nil {
			return err
		}
		if len(jobIDs) == 0 {
			return nil
		}
		if err := tx.Where("c_job_id IN ?", jobIDs).Delete(&backfillChunkRow{}).Error; err != nil {
			return err
		}
		result := tx.Where("c_job_id IN ?", jobIDs).Delete(&backfillJobRow{})
		deleted = result.RowsAffected
		return result.Error
	})
	return deleted, err
}

// Watermark 返回自动补采的扫描水位；没有则返回零值。
func (r *BackfillRepository) Watermark(ctx context.Context, spaceID, datasetID, frequency string) (time.Time, error) {
	if err := r.ready(); err != nil {
		return time.Time{}, err
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

// Watermarks 返回空间全部数据集的扫描水位，键为“数据集|频率”。
func (r *BackfillRepository) Watermarks(ctx context.Context, spaceID string) (map[string]time.Time, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	var rows []backfillWatermarkRow
	if err := r.db.WithContext(ctx).Where("c_space_id = ?", spaceID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		out[row.DatasetID+"|"+row.Frequency] = row.ScannedUntil
	}
	return out, nil
}

// SetWatermark 推进扫描水位（只前进，不后退）。
func (r *BackfillRepository) SetWatermark(ctx context.Context, spaceID, datasetID, frequency string, scannedUntil time.Time) error {
	if err := r.ready(); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Exec(`INSERT INTO t_collector_backfill_watermarks (c_space_id, c_dataset_id, c_frequency, c_scanned_until, c_mtime) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (c_space_id, c_dataset_id, c_frequency) DO UPDATE SET c_scanned_until = MAX(c_scanned_until, excluded.c_scanned_until), c_mtime = excluded.c_mtime`,
		spaceID, datasetID, frequency, scannedUntil.UTC(), time.Now().UTC()).Error
}

// Reset 清掉该空间全部补采作业、分块、自动补采水位与历史扫描记录，返回清掉的作业数。用于 Storage 数据被清空之后：
// 旧作业指向已经不存在的数据，水位不清会让自动补采沿用旧位置。运行中的分块写回状态时找不到行，直接放弃。
// 已确认的行情源缺失是行情源本身的事实，与 Storage 无关，保留。
func (r *BackfillRepository) Reset(ctx context.Context, spaceID string) (int64, error) {
	if err := r.ready(); err != nil {
		return 0, err
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
		if err := tx.Where("c_space_id = ?", spaceID).Delete(&historyScanRow{}).Error; err != nil {
			return err
		}
		return tx.Where("c_space_id = ?", spaceID).Delete(&backfillWatermarkRow{}).Error
	})
	return jobs, err
}
