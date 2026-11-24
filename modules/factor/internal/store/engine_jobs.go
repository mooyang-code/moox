package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// PullRecalcJob 给登记的引擎领取一个补算作业（调用方已确认它是登记的引擎进程），同时只有一条作业处于运行中：
//   - 有运行中的作业就原样返回它：引擎重启、管理端重启后，或上一次上报失败放弃了作业，都从已上报的进度续跑；
//   - 运行中的作业属于 skipTasks（结果数据集还没就绪）时把它退回待领取，再按下一条处理，不让它挡住别的作业；
//   - 没有运行中的作业，才把一条待领取的作业置为运行中（按 c_mtime 排序：归还会更新 c_mtime，被门禁挡住而归还的作业排到后面，
//     不会每轮都抢在别的作业前）；skipTasks 里任务的作业留到之后再领。
//
// 补算按整窗口读取源数据，同时跑多个会把 Storage 打满（2026-10-14 两个 1h 因子任务同时回填把存储进程打到 330% CPU），
// 所以只发一条。found 为假表示没有可领取的作业。
func (s *Store) PullRecalcJob(ctx context.Context, now time.Time, skipTasks map[string]bool) (RecalcJob, bool, error) {
	if s == nil || s.db == nil {
		return RecalcJob{}, false, fmt.Errorf("factor database is not open")
	}
	var pulled RecalcJob
	found := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var running []recalcJobRow
		if err := tx.Raw(`SELECT ` + recalcJobColumns + ` FROM t_factor_recalc_jobs WHERE c_status = 'running'
			ORDER BY c_mtime, c_job_id`).Scan(&running).Error; err != nil {
			return err
		}
		for _, row := range running {
			if !skipTasks[row.TaskID] {
				job, err := row.job()
				pulled, found = job, err == nil
				return err
			}
			if err := tx.Exec(`UPDATE t_factor_recalc_jobs SET c_status = 'accepted', c_mtime = ? WHERE c_job_id = ? AND c_status = 'running'`,
				now.UTC(), row.JobID).Error; err != nil {
				return err
			}
		}
		var rows []recalcJobRow
		if err := tx.Raw(`SELECT ` + recalcJobColumns + ` FROM t_factor_recalc_jobs WHERE c_status = 'accepted'
			ORDER BY c_mtime, c_job_id`).Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if skipTasks[row.TaskID] {
				continue
			}
			result := tx.Exec(`UPDATE t_factor_recalc_jobs SET c_status = 'running', c_progress_time = ?, c_mtime = ?
				WHERE c_job_id = ? AND c_status = 'accepted'`, max(row.ProgressTime, row.StartTime), now.UTC(), row.JobID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			job, err := readRecalcJob(tx, row.JobID)
			if err != nil {
				return err
			}
			pulled, found = job, true
			return nil
		}
		return nil
	})
	if err != nil {
		return RecalcJob{}, false, err
	}
	return pulled, found, nil
}

// ReportRecalcProgress 记录引擎上报的一个分块（调用方已确认它是登记的引擎进程）。已取消或已结束的作业原样返回，引擎据此停下；
// 作业不在运行中或进度倒退是 ErrConflict。
// defaultFilled 是引擎到目前为止累计的「按默认参数新补的期次」；nil 表示这次没有变化，非 nil 时整体替换。
// status 为 accepted 表示引擎归还作业（被门禁挡住、引擎停止）：作业回到待领取，进度保持管理端已记下的值。
func (s *Store) ReportRecalcProgress(ctx context.Context, jobID string, progress int64, status, errText string, defaultFilled map[string][]TimeRange, now time.Time) (RecalcJob, error) {
	if s == nil || s.db == nil {
		return RecalcJob{}, fmt.Errorf("factor database is not open")
	}
	switch status {
	case RecalcStatusAccepted, RecalcStatusRunning, RecalcStatusSucceeded, RecalcStatusFailed:
	default:
		return RecalcJob{}, fmt.Errorf("engine cannot report recalc status %q", status)
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return RecalcJob{}, fmt.Errorf("job id is required")
	}
	var updated RecalcJob
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := readRecalcJob(tx, jobID)
		if err != nil {
			return err
		}
		if terminalRecalcStatus(current.Status) {
			updated = current
			return nil
		}
		if current.Status != RecalcStatusRunning {
			return fmt.Errorf("%w: recalc job %q is not running", ErrConflict, jobID)
		}
		if status == RecalcStatusAccepted {
			// 归还不改进度，也不清掉前面分块记下的降级说明。
			progress, errText = current.ProgressTime, current.Error
		}
		if progress < current.ProgressTime {
			return fmt.Errorf("%w: recalc job %q progress %d is behind %d", ErrConflict, jobID, progress, current.ProgressTime)
		}
		progress = min(progress, current.EndTime)
		filledJSON := ""
		if defaultFilled != nil {
			if filledJSON, err = encodeDefaultFilled(defaultFilled); err != nil {
				return err
			}
		}
		if err := tx.Exec(`
			UPDATE t_factor_recalc_jobs
			SET c_status = ?, c_progress_time = ?, c_error = ?,
				c_default_filled_json = CASE WHEN ? = '' THEN c_default_filled_json ELSE ? END, c_mtime = ?
			WHERE c_job_id = ?
		`, status, progress, errText, filledJSON, filledJSON, now.UTC(), jobID).Error; err != nil {
			return err
		}
		updated, err = readRecalcJob(tx, jobID)
		return err
	})
	if err != nil {
		return RecalcJob{}, err
	}
	return updated, nil
}

func readRecalcJob(tx *gorm.DB, jobID string) (RecalcJob, error) {
	var row recalcJobRow
	result := tx.Raw(`SELECT `+recalcJobColumns+` FROM t_factor_recalc_jobs WHERE c_job_id = ?`, jobID).Scan(&row)
	if result.Error != nil {
		return RecalcJob{}, result.Error
	}
	if result.RowsAffected == 0 {
		return RecalcJob{}, gorm.ErrRecordNotFound
	}
	return row.job()
}

func terminalRecalcStatus(status string) bool {
	return status == RecalcStatusSucceeded || status == RecalcStatusFailed || status == RecalcStatusCancelled
}
