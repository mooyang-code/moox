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
	WorkerID string
	Version  string
	RootDir  string
	// 磁盘：总量、可用（worker 按它判断）、已用，以及 worker 配置的可用空间下限。
	DiskTotalBytes uint64
	DiskFreeBytes  uint64
	DiskUsedBytes  uint64
	MinFreeBytes   uint64
	StagingBytes   uint64
	Maintaining    bool
	PacingJSON     string
	RunningRunIDs  []string
	LastHeartbeat  time.Time
	// ObjectStoreJSON 是心跳带来的对象存储摘要（ObjectStoreInfo 的 JSON，不含凭据）；endpoint 为空表示没有配置。
	ObjectStoreJSON string
}

const workerColumns = `c_worker_id, c_version, c_root_dir, c_disk_total_bytes, c_disk_free_bytes, c_disk_used_bytes, c_min_free_bytes,
c_staging_bytes, c_maintaining, c_pacing_json, c_running_json, c_last_heartbeat_at, c_object_store_json`

func scanWorker(row rowScanner) (Worker, error) {
	var w Worker
	var maintaining int
	var running string
	var heartbeat timeValue
	err := row.Scan(&w.WorkerID, &w.Version, &w.RootDir, &w.DiskTotalBytes, &w.DiskFreeBytes, &w.DiskUsedBytes, &w.MinFreeBytes,
		&w.StagingBytes, &maintaining,
		&w.PacingJSON, &running, &heartbeat, &w.ObjectStoreJSON)
	if err != nil {
		return Worker{}, err
	}
	w.Maintaining = maintaining == 1
	w.LastHeartbeat = heartbeat.Time
	if running != "" {
		_ = json.Unmarshal([]byte(running), &w.RunningRunIDs)
	}
	return w, nil
}

// Heartbeat 记下 worker 最近一次心跳的状态。部署里只有一个 worker：表里只留这一行，换过 worker_id 的旧行随之删除。
func (s *Store) Heartbeat(ctx context.Context, w Worker, now time.Time) error {
	running, _ := json.Marshal(w.RunningRunIDs)
	if w.RunningRunIDs == nil {
		running = []byte("[]")
	}
	maintaining := 0
	if w.Maintaining {
		maintaining = 1
	}
	objectStore := w.ObjectStoreJSON
	if objectStore == "" {
		objectStore = "{}"
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM t_archive_workers WHERE c_worker_id != ?`, w.WorkerID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, upsertWorker,
			w.WorkerID, w.Version, w.RootDir, w.DiskTotalBytes, w.DiskFreeBytes, w.DiskUsedBytes, w.MinFreeBytes, w.StagingBytes, maintaining, w.PacingJSON,
			string(running), millisOf(now), objectStore)
		return err
	})
}

// GetWorker 返回 worker 最近一次心跳的状态；ok 为 false 表示还没有 worker 心跳过。
func (s *Store) GetWorker(ctx context.Context) (Worker, bool, error) {
	w, err := scanWorker(s.db.QueryRowContext(ctx, queryWorker))
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, false, nil
	}
	if err != nil {
		return Worker{}, false, err
	}
	return w, true, nil
}

// 以下语句整条写成常量，避免在调用处拼接 SQL。
const (
	queryWorker  = `SELECT ` + workerColumns + ` FROM t_archive_workers LIMIT 1`
	upsertWorker = `INSERT INTO t_archive_workers (c_worker_id, c_version, c_root_dir, c_disk_total_bytes, c_disk_free_bytes,
c_disk_used_bytes, c_min_free_bytes, c_staging_bytes, c_maintaining, c_pacing_json, c_running_json, c_last_heartbeat_at, c_object_store_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (c_worker_id) DO UPDATE SET c_version = excluded.c_version, c_root_dir = excluded.c_root_dir,
c_disk_total_bytes = excluded.c_disk_total_bytes, c_disk_free_bytes = excluded.c_disk_free_bytes, c_disk_used_bytes = excluded.c_disk_used_bytes,
c_min_free_bytes = excluded.c_min_free_bytes, c_staging_bytes = excluded.c_staging_bytes, c_maintaining = excluded.c_maintaining,
c_pacing_json = excluded.c_pacing_json, c_running_json = excluded.c_running_json, c_last_heartbeat_at = excluded.c_last_heartbeat_at,
c_object_store_json = excluded.c_object_store_json`
)
