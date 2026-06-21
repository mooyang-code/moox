package dao

import (
	"context"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AsyncJobDAO 异步Job数据访问对象接口
type AsyncJobDAO interface {
	// ========== Job管理 ==========

	// CreateAsyncJob 创建异步Job
	CreateAsyncJob(ctx context.Context, job *model.AsyncJob) error

	// GetAsyncJob 根据JobID获取Job
	GetAsyncJob(ctx context.Context, jobID string) (*model.AsyncJob, error)

	// ========== Job状态更新 ==========

	// SetJobStarted 设置Job为已启动状态
	SetJobStarted(ctx context.Context, jobID string) error

	// IncrementSuccessCount 原子递增成功计数
	IncrementSuccessCount(ctx context.Context, jobID string) error

	// IncrementFailedCount 原子递增失败计数
	IncrementFailedCount(ctx context.Context, jobID string) error
}

// asyncJobDAOImpl 实现异步任务作业表的数据访问逻辑。
type asyncJobDAOImpl struct {
	db *gorm.DB
}

// NewAsyncJobDAO 创建AsyncJobDAO实例
func NewAsyncJobDAO(db *gorm.DB) AsyncJobDAO {
	return &asyncJobDAOImpl{db: db}
}

// CreateAsyncJob 创建异步Job
func (d *asyncJobDAOImpl) CreateAsyncJob(ctx context.Context, job *model.AsyncJob) error {
	return d.db.WithContext(ctx).Create(job).Error
}

// GetAsyncJob 根据JobID获取Job
func (d *asyncJobDAOImpl) GetAsyncJob(ctx context.Context, jobID string) (*model.AsyncJob, error) {
	var job model.AsyncJob
	err := d.db.WithContext(ctx).
		Where("c_job_id = ?", jobID).
		First(&job).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &job, nil
}

// SetJobStarted 设置Job为已启动状态
func (d *asyncJobDAOImpl) SetJobStarted(ctx context.Context, jobID string) error {
	return d.db.WithContext(ctx).
		Model(&model.AsyncJob{}).
		Where("c_job_id = ?", jobID).
		Update("c_is_started", 1).Error
}

// IncrementSuccessCount 原子递增成功计数
func (d *asyncJobDAOImpl) IncrementSuccessCount(ctx context.Context, jobID string) error {
	// 使用 GORM 的 UpdateColumn 配合 clause.Expr 实现原子递增
	return d.db.WithContext(ctx).
		Model(&model.AsyncJob{}).
		Where("c_job_id = ?", jobID).
		UpdateColumn("c_success_task_cnt", gorm.Expr("c_success_task_cnt + ?", 1)).Error
}

// IncrementFailedCount 原子递增失败计数
func (d *asyncJobDAOImpl) IncrementFailedCount(ctx context.Context, jobID string) error {
	// 使用 GORM 的 UpdateColumn 配合 clause.Expr 实现原子递增
	return d.db.WithContext(ctx).
		Model(&model.AsyncJob{}).
		Where("c_job_id = ?", jobID).
		UpdateColumn("c_failed_task_cnt", gorm.Expr("c_failed_task_cnt + ?", 1)).Error
}

// BatchCreateAsyncJobs 批量创建Job（如果需要）
func (d *asyncJobDAOImpl) BatchCreateAsyncJobs(ctx context.Context, jobs []*model.AsyncJob) error {
	if len(jobs) == 0 {
		return nil
	}
	return d.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(jobs).Error
}

// GetJobsByPrefix 根据JobID前缀获取Job列表（如果需要查询功能）
func (d *asyncJobDAOImpl) GetJobsByPrefix(ctx context.Context, prefix string) ([]*model.AsyncJob, error) {
	var jobs []*model.AsyncJob
	err := d.db.WithContext(ctx).
		Where("c_job_id LIKE ?", fmt.Sprintf("%s%%", prefix)).
		Order("c_ctime DESC").
		Find(&jobs).Error
	if err != nil {
		return nil, err
	}
	return jobs, nil
}
