package staging

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// 上传失败后的退避：第 n 次失败后等 min(1h, 1m × 2^(n−1))。
const (
	uploadRetryBase = time.Minute
	uploadRetryMax  = time.Hour
)

// UploadRetryDelay 返回第 attempts 次失败之后的等待时间。
func UploadRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := uploadRetryBase
	for i := 1; i < attempts && delay < uploadRetryMax; i++ {
		delay *= 2
	}
	return min(delay, uploadRetryMax)
}

// PendingUploads 返回数据集里待上传的分区：有文件，且远端不是已校验的当前版本；跳过还在退避中的（remote_retry_at > now）。
// 按导出时间升序。“待上传”不另设标记，和 mgr 的“未复制”是同一个口径，重导出改了 sha256 就自动成为待上传。
func (s *Store) PendingUploads(ctx context.Context, spaceID, datasetID string, now time.Time) ([]PartitionState, error) {
	rows, err := s.db.QueryContext(ctx, queryPendingUploads, spaceID, datasetID, nanos(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PartitionState
	for rows.Next() {
		p, err := scanPartition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkUploaded 记下上传并校验通过的远端副本，清掉失败次数、错误与重试时刻，并置待上报。条件是文件 sha256 仍等于 fileSHA：
// 上传期间分区被重导出时不记成已上传，返回 false。
func (s *Store) MarkUploaded(ctx context.Context, spaceID, datasetID, key, fileSHA string, remote Remote) (bool, error) {
	result, err := s.db.ExecContext(ctx, markUploaded, remote.Key, fileSHA, remote.ETag, nullNanos(remote.UploadedAt), nullNanos(remote.VerifiedAt),
		spaceID, datasetID, key, fileSHA)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// MarkUploadFailed 记下一次上传失败：失败次数加 1、写错误、按退避设下次重试时刻，并置待上报。文件 sha256 已变（被重导出）时不动。
func (s *Store) MarkUploadFailed(ctx context.Context, spaceID, datasetID, key, fileSHA, reason string, now time.Time) error {
	var attempts int
	err := s.db.QueryRowContext(ctx, queryUploadAttempts, spaceID, datasetID, key, fileSHA).Scan(&attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	attempts++
	_, err = s.db.ExecContext(ctx, markUploadFailed, attempts, reason, nanos(now.Add(UploadRetryDelay(attempts))), spaceID, datasetID, key, fileSHA)
	return err
}

// MarkRemoteVerified 更新维护抽查通过的时间，并置待上报。
func (s *Store) MarkRemoteVerified(ctx context.Context, spaceID, datasetID, key string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, markRemoteVerified, nanos(at), spaceID, datasetID, key)
	return err
}

// ResetRemote 清空远端副本字段并写原因（远端丢失或内容不符），分区回到待上传，下一次运行从本地重传；置待上报。
func (s *Store) ResetRemote(ctx context.Context, spaceID, datasetID, key, reason string) error {
	_, err := s.db.ExecContext(ctx, resetRemote, reason, spaceID, datasetID, key)
	return err
}

// ReplicatedPartition 是一个已复制的分区及其所属数据集。
type ReplicatedPartition struct {
	SpaceID   string
	DatasetID string
	Partition PartitionState
}

// ReplicatedPartitions 按 remote_verified_at 升序返回全部数据集里已复制的分区，最多 limit 个（维护抽查用）。
func (s *Store) ReplicatedPartitions(ctx context.Context, limit int) ([]ReplicatedPartition, error) {
	rows, err := s.db.QueryContext(ctx, queryReplicated, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReplicatedPartition
	for rows.Next() {
		p, space, dataset, err := scanPartitionWith(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, ReplicatedPartition{SpaceID: space, DatasetID: dataset, Partition: p})
	}
	return out, rows.Err()
}

// MetaRemoteSHA 返回已上传到对象存储的 dataset.json 的 sha256；没有时为空。
func (s *Store) MetaRemoteSHA(ctx context.Context, spaceID, datasetID string) (string, error) {
	var sha string
	err := s.db.QueryRowContext(ctx, queryMetaRemoteSHA, spaceID, datasetID).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return sha, err
}

// SetMetaRemoteSHA 记下已上传的 dataset.json 的 sha256。
func (s *Store) SetMetaRemoteSHA(ctx context.Context, spaceID, datasetID, sha string) error {
	result, err := s.db.ExecContext(ctx, setMetaRemoteSHA, sha, spaceID, datasetID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("数据集 %s/%s 没有任务状态", spaceID, datasetID)
	}
	return nil
}

// 以下语句整条写成常量，避免在调用处拼接 SQL。
const (
	queryPendingUploads = `SELECT ` + partitionColumns + ` FROM partition_state WHERE space_id = ? AND dataset_id = ? AND file_path != ''
AND NOT (remote_sha256 = file_sha256 AND remote_verified_at IS NOT NULL)
AND (remote_retry_at IS NULL OR remote_retry_at <= ?)
ORDER BY exported_at, partition_key`
	markUploaded = `UPDATE partition_state SET remote_key = ?, remote_sha256 = ?, remote_etag = ?, remote_uploaded_at = ?, remote_verified_at = ?,
    remote_error = '', remote_attempts = 0, remote_retry_at = NULL, report_pending = 1
WHERE space_id = ? AND dataset_id = ? AND partition_key = ? AND file_sha256 = ?`
	markUploadFailed = `UPDATE partition_state SET remote_attempts = ?, remote_error = ?, remote_retry_at = ?, report_pending = 1
WHERE space_id = ? AND dataset_id = ? AND partition_key = ? AND file_sha256 = ?`
	resetRemote = `UPDATE partition_state SET remote_key = '', remote_sha256 = '', remote_etag = '', remote_uploaded_at = NULL, remote_verified_at = NULL,
    remote_error = ?, remote_attempts = 0, remote_retry_at = NULL, report_pending = 1
WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`
	queryUploadAttempts = `SELECT remote_attempts FROM partition_state WHERE space_id = ? AND dataset_id = ? AND partition_key = ? AND file_sha256 = ?`
	markRemoteVerified  = `UPDATE partition_state SET remote_verified_at = ?, report_pending = 1 WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`
	queryMetaRemoteSHA  = `SELECT meta_remote_sha256 FROM task_state WHERE space_id = ? AND dataset_id = ?`
	setMetaRemoteSHA    = `UPDATE task_state SET meta_remote_sha256 = ? WHERE space_id = ? AND dataset_id = ?`
	queryReplicated     = `SELECT space_id, dataset_id, ` + partitionColumns + ` FROM partition_state
WHERE file_path != '' AND remote_sha256 = file_sha256 AND remote_verified_at IS NOT NULL
ORDER BY remote_verified_at, space_id, dataset_id, partition_key LIMIT ?`
)
