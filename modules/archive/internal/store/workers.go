package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Worker 是 worker 最近一次心跳的状态。
type Worker struct {
	WorkerID       string
	InstanceID     string
	Version        string
	RootDir        string
	DiskTotalBytes uint64
	DiskFreeBytes  uint64
	StagingBytes   uint64
	Maintaining    bool
	PacingJSON     string
	RunningRunIDs  []string
	LeaseUntil     time.Time
	LastHeartbeat  time.Time
}

// Active 报告租约在 now 时是否有效。
func (w Worker) Active(now time.Time) bool { return w.LeaseUntil.After(now) }

const workerColumns = `c_worker_id, c_instance_id, c_version, c_root_dir, c_disk_total_bytes, c_disk_free_bytes, c_staging_bytes, c_maintaining,
c_pacing_json, c_running_json, c_lease_until, c_last_heartbeat_at`

func scanWorker(row rowScanner) (Worker, error) {
	var w Worker
	var maintaining int
	var running string
	var lease, heartbeat timeValue
	err := row.Scan(&w.WorkerID, &w.InstanceID, &w.Version, &w.RootDir, &w.DiskTotalBytes, &w.DiskFreeBytes, &w.StagingBytes, &maintaining,
		&w.PacingJSON, &running, &lease, &heartbeat)
	if err != nil {
		return Worker{}, err
	}
	w.Maintaining = maintaining == 1
	w.LeaseUntil, w.LastHeartbeat = lease.Time, heartbeat.Time
	if running != "" {
		_ = json.Unmarshal([]byte(running), &w.RunningRunIDs)
	}
	return w, nil
}

// HeartbeatResult 是一次心跳的结果。
type HeartbeatResult struct {
	// Active：本实例持有活动租约。
	Active bool
	// Conflict：同一 worker_id 已有另一个存活实例，调用方不能领取运行。
	Conflict bool
}

// Heartbeat 刷新 worker 状态与租约。同一时刻只有一个 worker 持有活动租约：
// 已有其他 worker_id 的租约未过期时本 worker 不活动；同一 worker_id 的另一个实例租约未过期时返回冲突；旧租约过期后新实例可以接管。
func (s *Store) Heartbeat(ctx context.Context, w Worker, now time.Time, ttl time.Duration) (HeartbeatResult, error) {
	var result HeartbeatResult
	err := s.tx(ctx, func(tx *sql.Tx) error {
		own, err := scanWorker(tx.QueryRowContext(ctx, `SELECT `+workerColumns+` FROM t_archive_workers WHERE c_worker_id = ?`, w.WorkerID))
		hasOwn := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if hasOwn && own.Active(now) && own.InstanceID != w.InstanceID {
			result.Conflict = true
			return nil
		}
		var otherActive int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_workers WHERE c_worker_id != ? AND c_lease_until > ?`, w.WorkerID, fmtTime(now)).Scan(&otherActive); err != nil {
			return err
		}
		running, _ := json.Marshal(w.RunningRunIDs)
		if w.RunningRunIDs == nil {
			running = []byte("[]")
		}
		maintaining := 0
		if w.Maintaining {
			maintaining = 1
		}
		// 其他 worker 持有活动租约时，本 worker 只刷新状态，不续租（租约过期后才能接管）。
		var lease any
		if otherActive == 0 {
			lease = fmtTime(now.Add(ttl))
			result.Active = true
		} else if hasOwn {
			lease = nullTime(own.LeaseUntil)
		}
		if hasOwn {
			_, err = tx.ExecContext(ctx, `UPDATE t_archive_workers SET c_instance_id = ?, c_version = ?, c_root_dir = ?, c_disk_total_bytes = ?, c_disk_free_bytes = ?,
c_staging_bytes = ?, c_maintaining = ?, c_pacing_json = ?, c_running_json = ?, c_lease_until = ?, c_last_heartbeat_at = ? WHERE c_worker_id = ?`,
				w.InstanceID, w.Version, w.RootDir, w.DiskTotalBytes, w.DiskFreeBytes, w.StagingBytes, maintaining, w.PacingJSON, string(running), lease, fmtTime(now), w.WorkerID)
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO t_archive_workers (c_worker_id, c_instance_id, c_version, c_root_dir, c_disk_total_bytes, c_disk_free_bytes,
c_staging_bytes, c_maintaining, c_pacing_json, c_running_json, c_lease_until, c_last_heartbeat_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			w.WorkerID, w.InstanceID, w.Version, w.RootDir, w.DiskTotalBytes, w.DiskFreeBytes, w.StagingBytes, maintaining, w.PacingJSON, string(running), lease, fmtTime(now))
		return err
	})
	return result, err
}

// IsActiveWorker 报告 worker_id/instance_id 当前是否持有未过期的活动租约。
func (s *Store) IsActiveWorker(ctx context.Context, workerID, instanceID string, now time.Time) (bool, error) {
	w, err := scanWorker(s.db.QueryRowContext(ctx, `SELECT `+workerColumns+` FROM t_archive_workers WHERE c_worker_id = ?`, workerID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return w.Active(now) && w.InstanceID == instanceID, nil
}

// ListWorkers 返回全部 worker，活动租约的在前。
func (s *Store) ListWorkers(ctx context.Context) ([]Worker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+workerColumns+` FROM t_archive_workers ORDER BY c_lease_until DESC, c_worker_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Worker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// PartitionWorkers 返回任务分区登记过的 worker_id（用于总览提示）。
func (s *Store) PartitionWorkers(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c_space_id, c_dataset_id, MIN(c_worker_id) FROM t_archive_partitions GROUP BY c_space_id, c_dataset_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var space, dataset, worker string
		if err := rows.Scan(&space, &dataset, &worker); err != nil {
			return nil, err
		}
		out[space+"/"+dataset] = worker
	}
	return out, rows.Err()
}
