package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// pullableCondition selects jobs a compute engine may take: accepted ones and
// running ones whose lease has expired (their engine stopped reporting).
const pullableCondition = `(c_status = 'accepted' OR (c_status = 'running' AND c_lease_expires_at <= ?))`

// PullRecalcJob atomically leases the oldest pullable job to engineID. Jobs of
// sets in skipSets are left for a later pull. The returned job carries the new
// lease token; found is false when no job is pullable.
func (s *Store) PullRecalcJob(ctx context.Context, engineID string, now time.Time, ttl time.Duration, skipSets map[string]bool) (RecalcJob, bool, error) {
	if s == nil || s.db == nil {
		return RecalcJob{}, false, fmt.Errorf("factor database is not open")
	}
	engineID = strings.TrimSpace(engineID)
	if engineID == "" || ttl <= 0 {
		return RecalcJob{}, false, fmt.Errorf("engine id and a positive lease ttl are required")
	}
	token, err := newLeaseToken()
	if err != nil {
		return RecalcJob{}, false, err
	}
	nowUnix := now.UTC().Unix()
	var pulled RecalcJob
	found := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []recalcJobRow
		if err := tx.Raw(`SELECT `+recalcJobColumns+` FROM t_factor_recalc_jobs WHERE `+pullableCondition+`
			ORDER BY c_ctime, c_job_id`, nowUnix).Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if skipSets[row.SetID] {
				continue
			}
			progress := max(row.ProgressTime, row.StartTime)
			result := tx.Exec(`
				UPDATE t_factor_recalc_jobs
				SET c_status = 'running', c_engine_id = ?, c_lease_token = ?, c_lease_expires_at = ?,
					c_progress_time = ?, c_mtime = ?
				WHERE c_job_id = ? AND `+pullableCondition,
				engineID, token, nowUnix+int64(ttl/time.Second), progress, now.UTC(), row.JobID, nowUnix)
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

// ReportRecalcProgress records a chunk reported by the engine holding the
// lease and renews the lease. A cancelled or finished job is returned as is so
// the engine can stop; a stale token or a progress regression is ErrConflict.
func (s *Store) ReportRecalcProgress(ctx context.Context, jobID, leaseToken string, progress int64, status, errText string, now time.Time, ttl time.Duration) (RecalcJob, error) {
	if s == nil || s.db == nil {
		return RecalcJob{}, fmt.Errorf("factor database is not open")
	}
	switch status {
	case RecalcStatusRunning, RecalcStatusSucceeded, RecalcStatusFailed:
	default:
		return RecalcJob{}, fmt.Errorf("engine cannot report recalc status %q", status)
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || strings.TrimSpace(leaseToken) == "" || ttl <= 0 {
		return RecalcJob{}, fmt.Errorf("job id, lease token and a positive lease ttl are required")
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
		if current.Status != RecalcStatusRunning || current.LeaseToken != leaseToken {
			return fmt.Errorf("%w: recalc job %q is not leased with this token", ErrConflict, jobID)
		}
		if progress < current.ProgressTime {
			return fmt.Errorf("%w: recalc job %q progress %d is behind %d", ErrConflict, jobID, progress, current.ProgressTime)
		}
		progress = min(progress, current.EndTime)
		expires := now.UTC().Unix() + int64(ttl/time.Second)
		if status != RecalcStatusRunning {
			expires = 0
		}
		if err := tx.Exec(`
			UPDATE t_factor_recalc_jobs
			SET c_status = ?, c_progress_time = ?, c_error = ?, c_lease_expires_at = ?, c_mtime = ?
			WHERE c_job_id = ?
		`, status, progress, errText, expires, now.UTC(), jobID).Error; err != nil {
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

func newLeaseToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate recalc lease token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}
