package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 历史合成作业状态。
const (
	ResampleJobQueued    = "queued"
	ResampleJobRunning   = "running"
	ResampleJobSyncing   = "syncing"
	ResampleJobSucceeded = "succeeded"
	ResampleJobFailed    = "failed"
	ResampleJobCancelled = "cancelled"
)

// 历史合成作业与补写通知的来源。
const (
	ResampleOriginManual         = "manual"
	ResampleOriginSourceBackfill = "source_backfill"
	// NoticeOriginResample 是合成在周期结束后的补写；补采来源取补采作业的 auto / manual / force。
	NoticeOriginResample = "resample"
)

// maxSkippedSamples 是作业保留的跳过样例个数。
const maxSkippedSamples = 20

var (
	// ErrResampleJobNotFound 表示作业不存在。
	ErrResampleJobNotFound = errors.New("resample job not found")
	// ErrResampleJobFinished 表示作业已经结束，不能再取消。
	ErrResampleJobFinished = errors.New("resample job is already finished")
)

// ResampleSnapshot 是合成目标数据集登记过的一份标的快照。
type ResampleSnapshot struct {
	SpaceID       string
	TaskID        string
	EffectiveFrom int64
	SeriesHash    string
	Subjects      []string
}

// ResampleJob 是一个历史合成作业。Subjects 为空表示全部标的。
type ResampleJob struct {
	JobID          string
	SpaceID        string
	TaskID         string
	RequestID      string
	Origin         string
	Subjects       []string
	Start, End     time.Time
	NextBucket     time.Time
	NotBefore      time.Time
	Status         string
	TotalBuckets   int
	DoneBuckets    int
	Written        int64
	Skipped        int64
	SkippedSamples []string
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Finished 报告作业是否已经结束。
func (j ResampleJob) Finished() bool {
	return j.Status == ResampleJobSucceeded || j.Status == ResampleJobFailed || j.Status == ResampleJobCancelled
}

// BackfillNotice 是一条补写通知：某数据集的某标的在 [Start, End) 被补写过。
type BackfillNotice struct {
	ID        int64
	SpaceID   string
	DatasetID string
	Frequency string
	SubjectID string
	Start     time.Time
	End       time.Time
	Origin    string
	JobID     string
	BatchID   string
}

// OutboxEvent 是待发布的本地事件。
type OutboxEvent struct {
	ID           int64
	EventID      string
	SpaceID      string
	EventName    string
	SubjectToken string
	Payload      []byte
	CreatedAt    time.Time
}

type resampleSnapshotRow struct {
	ID            int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID       string    `gorm:"column:c_space_id"`
	TaskID        string    `gorm:"column:c_task_id"`
	EffectiveFrom int64     `gorm:"column:c_effective_from"`
	SeriesHash    string    `gorm:"column:c_series_hash"`
	Subjects      string    `gorm:"column:c_subjects"`
	CreatedAt     time.Time `gorm:"column:c_ctime"`
}

func (resampleSnapshotRow) TableName() string { return "t_collector_resample_snapshots" }

type resampleJobRow struct {
	ID             int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	JobID          string    `gorm:"column:c_job_id"`
	SpaceID        string    `gorm:"column:c_space_id"`
	TaskID         string    `gorm:"column:c_task_id"`
	RequestID      string    `gorm:"column:c_request_id"`
	Origin         string    `gorm:"column:c_origin"`
	Subjects       string    `gorm:"column:c_subjects"`
	Start          time.Time `gorm:"column:c_start"`
	End            time.Time `gorm:"column:c_end"`
	NextBucket     time.Time `gorm:"column:c_next_bucket"`
	NotBefore      time.Time `gorm:"column:c_not_before"`
	Status         string    `gorm:"column:c_status"`
	TotalBuckets   int       `gorm:"column:c_total_buckets"`
	DoneBuckets    int       `gorm:"column:c_done_buckets"`
	Written        int64     `gorm:"column:c_written"`
	Skipped        int64     `gorm:"column:c_skipped"`
	SkippedSamples string    `gorm:"column:c_skipped_samples"`
	LastError      string    `gorm:"column:c_last_error"`
	CreatedAt      time.Time `gorm:"column:c_ctime"`
	UpdatedAt      time.Time `gorm:"column:c_mtime"`
}

func (resampleJobRow) TableName() string { return "t_collector_resample_jobs" }

func (row resampleJobRow) job() ResampleJob {
	job := ResampleJob{
		JobID: row.JobID, SpaceID: row.SpaceID, TaskID: row.TaskID, RequestID: row.RequestID, Origin: row.Origin,
		Start: row.Start.UTC(), End: row.End.UTC(), NextBucket: row.NextBucket.UTC(), NotBefore: row.NotBefore.UTC(),
		Status: row.Status, TotalBuckets: row.TotalBuckets, DoneBuckets: row.DoneBuckets, Written: row.Written, Skipped: row.Skipped,
		LastError: row.LastError, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	_ = json.Unmarshal([]byte(row.Subjects), &job.Subjects)
	_ = json.Unmarshal([]byte(row.SkippedSamples), &job.SkippedSamples)
	return job
}

type backfillNoticeRow struct {
	ID        int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID   string    `gorm:"column:c_space_id"`
	DatasetID string    `gorm:"column:c_dataset_id"`
	Frequency string    `gorm:"column:c_frequency"`
	SubjectID string    `gorm:"column:c_subject_id"`
	Start     time.Time `gorm:"column:c_start"`
	End       time.Time `gorm:"column:c_end"`
	Origin    string    `gorm:"column:c_origin"`
	JobID     string    `gorm:"column:c_job_id"`
	BatchID   string    `gorm:"column:c_batch_id"`
	CreatedAt time.Time `gorm:"column:c_ctime"`
}

func (backfillNoticeRow) TableName() string { return "t_collector_backfill_notices" }

func (row backfillNoticeRow) notice() BackfillNotice {
	return BackfillNotice{ID: row.ID, SpaceID: row.SpaceID, DatasetID: row.DatasetID, Frequency: row.Frequency, SubjectID: row.SubjectID,
		Start: row.Start.UTC(), End: row.End.UTC(), Origin: row.Origin, JobID: row.JobID, BatchID: row.BatchID}
}

type eventOutboxRow struct {
	ID           int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	EventID      string    `gorm:"column:c_event_id"`
	SpaceID      string    `gorm:"column:c_space_id"`
	EventName    string    `gorm:"column:c_event_name"`
	SubjectToken string    `gorm:"column:c_subject_token"`
	Payload      []byte    `gorm:"column:c_payload"`
	Published    bool      `gorm:"column:c_published"`
	CreatedAt    time.Time `gorm:"column:c_ctime"`
}

func (eventOutboxRow) TableName() string { return "t_collector_event_outbox" }

// ResampleStateRepository 保存合成的快照、历史作业、补写通知与本地事件发件箱。
type ResampleStateRepository struct{ db *gorm.DB }

// NewResampleStateRepository 创建仓库。
func NewResampleStateRepository(db *gorm.DB) *ResampleStateRepository {
	return &ResampleStateRepository{db: db}
}

func (r *ResampleStateRepository) ready() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("resample state repository is not initialized")
	}
	return nil
}

// AddSnapshot 追加一份快照；同一生效点已存在时忽略（登记是幂等的）。
func (r *ResampleStateRepository) AddSnapshot(ctx context.Context, snapshot ResampleSnapshot) error {
	if err := r.ready(); err != nil {
		return err
	}
	subjects, _ := json.Marshal(snapshot.Subjects)
	row := resampleSnapshotRow{SpaceID: snapshot.SpaceID, TaskID: snapshot.TaskID, EffectiveFrom: snapshot.EffectiveFrom, SeriesHash: snapshot.SeriesHash, Subjects: string(subjects)}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

// SnapshotFor 返回对时刻 at（Unix 秒）生效的快照：生效点不晚于 at 的最新一份。
func (r *ResampleStateRepository) SnapshotFor(ctx context.Context, spaceID, taskID string, at int64) (ResampleSnapshot, bool, error) {
	if err := r.ready(); err != nil {
		return ResampleSnapshot{}, false, err
	}
	var row resampleSnapshotRow
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_task_id = ? AND c_effective_from <= ?", spaceID, taskID, at).Order("c_effective_from DESC").Take(&row).Error
	return snapshotFromRow(row, err)
}

// LatestSnapshot 返回生效点最晚的快照（包括尚未生效的）。
func (r *ResampleStateRepository) LatestSnapshot(ctx context.Context, spaceID, taskID string) (ResampleSnapshot, bool, error) {
	if err := r.ready(); err != nil {
		return ResampleSnapshot{}, false, err
	}
	var row resampleSnapshotRow
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_task_id = ?", spaceID, taskID).Order("c_effective_from DESC").Take(&row).Error
	return snapshotFromRow(row, err)
}

func snapshotFromRow(row resampleSnapshotRow, err error) (ResampleSnapshot, bool, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ResampleSnapshot{}, false, nil
	}
	if err != nil {
		return ResampleSnapshot{}, false, err
	}
	snapshot := ResampleSnapshot{SpaceID: row.SpaceID, TaskID: row.TaskID, EffectiveFrom: row.EffectiveFrom, SeriesHash: row.SeriesHash}
	_ = json.Unmarshal([]byte(row.Subjects), &snapshot.Subjects)
	return snapshot, true, nil
}

// SubmitJob 提交历史合成作业。同一任务的 request_id 重复提交返回已有作业（created 为假）；
// source_backfill 作业若与同任务尚未开始的排队作业范围重叠或相邻，就合并进去（标的取并集，最早开始时间取较晚的一个）。
func (r *ResampleStateRepository) SubmitJob(ctx context.Context, job ResampleJob) (ResampleJob, bool, error) {
	if err := r.ready(); err != nil {
		return ResampleJob{}, false, err
	}
	if strings.TrimSpace(job.JobID) == "" || strings.TrimSpace(job.SpaceID) == "" || strings.TrimSpace(job.TaskID) == "" || strings.TrimSpace(job.RequestID) == "" {
		return ResampleJob{}, false, fmt.Errorf("job_id, space_id, task_id and request_id are required")
	}
	if !job.End.After(job.Start) {
		return ResampleJob{}, false, fmt.Errorf("job range must not be empty")
	}
	if job.Origin == "" {
		job.Origin = ResampleOriginManual
	}
	var result ResampleJob
	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing resampleJobRow
		err := tx.Where("c_space_id = ? AND c_task_id = ? AND c_request_id = ?", job.SpaceID, job.TaskID, job.RequestID).Take(&existing).Error
		if err == nil {
			result = existing.job()
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if job.Origin == ResampleOriginSourceBackfill {
			var rows []resampleJobRow
			if err := tx.Where("c_space_id = ? AND c_task_id = ? AND c_origin = ? AND c_status = ? AND c_start <= ? AND c_end >= ?",
				job.SpaceID, job.TaskID, ResampleOriginSourceBackfill, ResampleJobQueued, job.End.UTC(), job.Start.UTC()).Order("c_id").Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				// 已经开始推进（游标不在起点）的作业不再合并，避免回头。
				if !row.NextBucket.Equal(row.Start) {
					continue
				}
				merged := row.job()
				merged.Start, merged.NextBucket = minTime(merged.Start, job.Start), minTime(merged.Start, job.Start)
				merged.End = maxTime(merged.End, job.End)
				merged.NotBefore = maxTime(merged.NotBefore, job.NotBefore)
				merged.Subjects = unionSubjects(merged.Subjects, job.Subjects)
				subjects, _ := json.Marshal(merged.Subjects)
				if err := tx.Model(&resampleJobRow{}).Where("c_id = ?", row.ID).Updates(map[string]any{
					"c_start": merged.Start, "c_next_bucket": merged.NextBucket, "c_end": merged.End, "c_not_before": merged.NotBefore,
					"c_subjects": string(subjects), "c_total_buckets": 0, "c_mtime": time.Now().UTC(),
				}).Error; err != nil {
					return err
				}
				result = merged
				return nil
			}
		}
		subjects, _ := json.Marshal(sortedUnique(job.Subjects))
		row := resampleJobRow{
			JobID: job.JobID, SpaceID: job.SpaceID, TaskID: job.TaskID, RequestID: job.RequestID, Origin: job.Origin, Subjects: string(subjects),
			Start: job.Start.UTC(), End: job.End.UTC(), NextBucket: job.Start.UTC(), NotBefore: job.NotBefore.UTC(), Status: ResampleJobQueued,
			TotalBuckets: job.TotalBuckets, SkippedSamples: "[]",
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		created = true
		result = row.job()
		return nil
	})
	return result, created, err
}

func unionSubjects(left, right []string) []string {
	// 空列表表示全部标的，并集仍是全部。
	if len(left) == 0 || len(right) == 0 {
		return nil
	}
	return sortedUnique(append(append([]string(nil), left...), right...))
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// GetJob 按作业 ID 读取。
func (r *ResampleStateRepository) GetJob(ctx context.Context, spaceID, jobID string) (ResampleJob, error) {
	if err := r.ready(); err != nil {
		return ResampleJob{}, err
	}
	var row resampleJobRow
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_job_id = ?", spaceID, jobID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ResampleJob{}, ErrResampleJobNotFound
	}
	if err != nil {
		return ResampleJob{}, err
	}
	return row.job(), nil
}

// ListJobs 按任务、状态列出作业，最新的在前；taskID、status 为空表示不限。
func (r *ResampleStateRepository) ListJobs(ctx context.Context, spaceID, taskID, status string, limit int) ([]ResampleJob, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := r.db.WithContext(ctx).Where("c_space_id = ?", spaceID)
	if taskID != "" {
		query = query.Where("c_task_id = ?", taskID)
	}
	if status != "" {
		query = query.Where("c_status = ?", status)
	}
	var rows []resampleJobRow
	if err := query.Order("c_id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ResampleJob, len(rows))
	for i, row := range rows {
		out[i] = row.job()
	}
	return out, nil
}

// RunnableJobs 返回已到最早开始时间的未结束作业，每个任务只取最早的一个（同一任务同一时间只运行一个）。
func (r *ResampleStateRepository) RunnableJobs(ctx context.Context, now time.Time, limit int) ([]ResampleJob, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	var rows []resampleJobRow
	if err := r.db.WithContext(ctx).Where("c_status IN ?", []string{ResampleJobQueued, ResampleJobRunning, ResampleJobSyncing}).Order("c_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []ResampleJob
	for _, row := range rows {
		key := row.SpaceID + "\x00" + row.TaskID
		if seen[key] {
			continue
		}
		seen[key] = true // 任务里最早的未结束作业还没到开始时间时，后面的也不越过它
		if row.NotBefore.After(now) {
			continue
		}
		out = append(out, row.job())
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// StartJob 把排队的作业标为运行中并记下总桶数。
func (r *ResampleStateRepository) StartJob(ctx context.Context, jobID string, totalBuckets int) error {
	if err := r.ready(); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&resampleJobRow{}).Where("c_job_id = ? AND c_status = ?", jobID, ResampleJobQueued).
		Updates(map[string]any{"c_status": ResampleJobRunning, "c_total_buckets": totalBuckets, "c_mtime": time.Now().UTC()}).Error
}

// JobProgress 是一轮推进的结果。
type JobProgress struct {
	NextBucket     time.Time
	DoneBuckets    int
	Written        int64
	Skipped        int64
	SkippedSamples []string
}

// AdvanceJob 累加一轮的进度并移动游标；作业已被取消时不改动。
func (r *ResampleStateRepository) AdvanceJob(ctx context.Context, jobID string, progress JobProgress) error {
	if err := r.ready(); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row resampleJobRow
		if err := tx.Where("c_job_id = ?", jobID).Take(&row).Error; err != nil {
			return err
		}
		if row.Status != ResampleJobRunning {
			return nil
		}
		var samples []string
		_ = json.Unmarshal([]byte(row.SkippedSamples), &samples)
		for _, sample := range progress.SkippedSamples {
			if len(samples) >= maxSkippedSamples {
				break
			}
			samples = append(samples, sample)
		}
		encoded, _ := json.Marshal(samples)
		return tx.Model(&resampleJobRow{}).Where("c_id = ?", row.ID).Updates(map[string]any{
			"c_next_bucket": progress.NextBucket.UTC(), "c_done_buckets": row.DoneBuckets + progress.DoneBuckets,
			"c_written": row.Written + progress.Written, "c_skipped": row.Skipped + progress.Skipped,
			"c_skipped_samples": string(encoded), "c_mtime": time.Now().UTC(),
		}).Error
	})
}

// SetJobStatus 更新作业状态；已取消或已结束的作业不再改变。
func (r *ResampleStateRepository) SetJobStatus(ctx context.Context, jobID, status, lastError string) error {
	if err := r.ready(); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&resampleJobRow{}).
		Where("c_job_id = ? AND c_status NOT IN ?", jobID, []string{ResampleJobSucceeded, ResampleJobFailed, ResampleJobCancelled}).
		Updates(map[string]any{"c_status": status, "c_last_error": lastError, "c_mtime": time.Now().UTC()}).Error
}

// CancelJob 取消排队或运行中的作业（已写入的保留）。
func (r *ResampleStateRepository) CancelJob(ctx context.Context, spaceID, jobID string) (ResampleJob, error) {
	job, err := r.GetJob(ctx, spaceID, jobID)
	if err != nil {
		return ResampleJob{}, err
	}
	if job.Finished() {
		return job, ErrResampleJobFinished
	}
	if err := r.SetJobStatus(ctx, jobID, ResampleJobCancelled, ""); err != nil {
		return ResampleJob{}, err
	}
	return r.GetJob(ctx, spaceID, jobID)
}

// CleanupFinishedJobs 删除早于 before 结束的作业。
func (r *ResampleStateRepository) CleanupFinishedJobs(ctx context.Context, before time.Time) (int64, error) {
	if err := r.ready(); err != nil {
		return 0, err
	}
	result := r.db.WithContext(ctx).Where("c_status IN ? AND c_mtime < ?", []string{ResampleJobSucceeded, ResampleJobFailed, ResampleJobCancelled}, before.UTC()).Delete(&resampleJobRow{})
	return result.RowsAffected, result.Error
}

// AddNotices 记录补写通知。
func (r *ResampleStateRepository) AddNotices(ctx context.Context, notices []BackfillNotice) error {
	if err := r.ready(); err != nil {
		return err
	}
	return addNoticesTx(r.db.WithContext(ctx), notices)
}

func addNoticesTx(tx *gorm.DB, notices []BackfillNotice) error {
	if len(notices) == 0 {
		return nil
	}
	rows := make([]backfillNoticeRow, len(notices))
	for i, notice := range notices {
		rows[i] = backfillNoticeRow{SpaceID: notice.SpaceID, DatasetID: notice.DatasetID, Frequency: notice.Frequency, SubjectID: notice.SubjectID,
			Start: notice.Start.UTC(), End: notice.End.UTC(), Origin: notice.Origin, JobID: notice.JobID}
	}
	return tx.CreateInBatches(&rows, 200).Error
}

// UnsealedNotices 返回还没有成批的通知，按 ID 升序。
func (r *ResampleStateRepository) UnsealedNotices(ctx context.Context, limit int) ([]BackfillNotice, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 5000
	}
	var rows []backfillNoticeRow
	if err := r.db.WithContext(ctx).Where("c_batch_id = ''").Order("c_id").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]BackfillNotice, len(rows))
	for i, row := range rows {
		out[i] = row.notice()
	}
	return out, nil
}

// SealBatch 在一个事务里把通知标记成批并写入发件箱；通知已被别的批次封存时什么都不做。
func (r *ResampleStateRepository) SealBatch(ctx context.Context, noticeIDs []int64, event OutboxEvent) error {
	if err := r.ready(); err != nil {
		return err
	}
	if len(noticeIDs) == 0 || strings.TrimSpace(event.EventID) == "" {
		return fmt.Errorf("notice ids and event id are required")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&backfillNoticeRow{}).Where("c_id IN ? AND c_batch_id = ''", noticeIDs).Update("c_batch_id", event.EventID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		row := eventOutboxRow{EventID: event.EventID, SpaceID: event.SpaceID, EventName: event.EventName, SubjectToken: event.SubjectToken, Payload: event.Payload}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	})
}

// PendingEvents 返回还没有发布的事件，按 ID 升序。
func (r *ResampleStateRepository) PendingEvents(ctx context.Context, limit int) ([]OutboxEvent, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	var rows []eventOutboxRow
	if err := r.db.WithContext(ctx).Where("c_published = ?", false).Order("c_id").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]OutboxEvent, len(rows))
	for i, row := range rows {
		out[i] = OutboxEvent{ID: row.ID, EventID: row.EventID, SpaceID: row.SpaceID, EventName: row.EventName, SubjectToken: row.SubjectToken, Payload: row.Payload, CreatedAt: row.CreatedAt}
	}
	return out, nil
}

// MarkPublished 标记事件已发布。
func (r *ResampleStateRepository) MarkPublished(ctx context.Context, id int64) error {
	if err := r.ready(); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&eventOutboxRow{}).Where("c_id = ?", id).Update("c_published", true).Error
}

// CleanupNotices 删除早于 before 的通知与已发布事件，返回删除的行数。没有事件总线时通知不会成批，也靠它清理。
func (r *ResampleStateRepository) CleanupNotices(ctx context.Context, before time.Time) (int64, error) {
	if err := r.ready(); err != nil {
		return 0, err
	}
	var total int64
	result := r.db.WithContext(ctx).Where("c_ctime < ?", before.UTC()).Delete(&backfillNoticeRow{})
	if result.Error != nil {
		return 0, result.Error
	}
	total += result.RowsAffected
	result = r.db.WithContext(ctx).Where("c_published = ? AND c_ctime < ?", true, before.UTC()).Delete(&eventOutboxRow{})
	return total + result.RowsAffected, result.Error
}
