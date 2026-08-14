package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const viewRebuildLogColumns = `
	c_log_id, c_space_id, c_view_id, c_build_id, c_index_id,
	c_trigger_reason, c_result, c_block_reason, c_target_view_revision,
	c_active_view_revision, c_physical_bytes, c_num_pending, c_num_ack_pending,
	c_entries_written, c_started_at, c_finished_at, c_first_checked_at,
	c_last_checked_at, c_skip_count, c_error_summary, c_details_json,
	c_created_at, c_updated_at`

type viewRebuildLogScanner interface {
	Scan(dest ...any) error
}

func (s *Store) viewRebuildLogNow() string {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return now().UTC().Format(time.RFC3339Nano)
}

func validateViewRebuildLog(item *pb.ViewRebuildLog) error {
	if item == nil {
		return errors.New("view rebuild log is required")
	}
	if strings.TrimSpace(item.GetSpaceId()) == "" || strings.TrimSpace(item.GetViewId()) == "" {
		return errors.New("space_id and view_id are required")
	}
	if item.GetTriggerReason() < pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_INITIAL_BUILD || item.GetTriggerReason() > pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_INTERRUPTED_RETRY {
		return errors.New("invalid view rebuild trigger reason")
	}
	if item.GetResult() < pb.ViewRebuildResult_VIEW_REBUILD_RESULT_RUNNING || item.GetResult() > pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SKIPPED {
		return errors.New("invalid view rebuild result")
	}
	return nil
}

func scanViewRebuildLog(row viewRebuildLogScanner) (*pb.ViewRebuildLog, error) {
	item := &pb.ViewRebuildLog{}
	var reason, result int32
	if err := row.Scan(
		&item.LogId, &item.SpaceId, &item.ViewId, &item.BuildId, &item.IndexId,
		&reason, &result, &item.BlockReason, &item.TargetViewRevision,
		&item.ActiveViewRevision, &item.PhysicalBytes, &item.NumPending,
		&item.NumAckPending, &item.EntriesWritten, &item.StartedAt,
		&item.FinishedAt, &item.FirstCheckedAt, &item.LastCheckedAt,
		&item.SkipCount, &item.ErrorSummary, &item.DetailsJson,
		&item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	item.TriggerReason = pb.ViewRebuildTriggerReason(reason)
	item.Result = pb.ViewRebuildResult(result)
	return item, nil
}

func (s *Store) getViewRebuildLog(ctx context.Context, db queryRower, logID int64) (*pb.ViewRebuildLog, error) {
	return scanViewRebuildLog(db.QueryRowContext(ctx,
		`SELECT `+viewRebuildLogColumns+` FROM t_view_rebuild_logs WHERE c_log_id = ?`, logID))
}

func (s *Store) CreateViewRebuildLog(ctx context.Context, item *pb.ViewRebuildLog) (*pb.ViewRebuildLog, error) {
	if err := validateViewRebuildLog(item); err != nil {
		return nil, err
	}
	createdAt := item.GetCreatedAt()
	if createdAt == "" {
		createdAt = s.viewRebuildLogNow()
	}
	updatedAt := item.GetUpdatedAt()
	if updatedAt == "" {
		updatedAt = createdAt
	}
	details := defaultJSON(item.GetDetailsJson())
	skipCount := item.GetSkipCount()
	if item.GetResult() == pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SKIPPED && skipCount == 0 {
		skipCount = 1
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO t_view_rebuild_logs (
			c_space_id, c_view_id, c_build_id, c_index_id, c_trigger_reason,
			c_result, c_block_reason, c_target_view_revision, c_active_view_revision,
			c_physical_bytes, c_num_pending, c_num_ack_pending, c_entries_written,
			c_started_at, c_finished_at, c_first_checked_at, c_last_checked_at,
			c_skip_count, c_error_summary, c_details_json, c_created_at, c_updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.GetSpaceId(), item.GetViewId(), item.GetBuildId(), item.GetIndexId(), item.GetTriggerReason(),
		item.GetResult(), item.GetBlockReason(), item.GetTargetViewRevision(), item.GetActiveViewRevision(),
		item.GetPhysicalBytes(), item.GetNumPending(), item.GetNumAckPending(), item.GetEntriesWritten(),
		item.GetStartedAt(), item.GetFinishedAt(), item.GetFirstCheckedAt(), item.GetLastCheckedAt(),
		skipCount, item.GetErrorSummary(), details, createdAt, updatedAt)
	if err != nil {
		return nil, err
	}
	logID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.getViewRebuildLog(ctx, s.db, logID)
}

func (s *Store) UpdateViewRebuildLog(ctx context.Context, item *pb.ViewRebuildLog) (*pb.ViewRebuildLog, error) {
	if err := validateViewRebuildLog(item); err != nil {
		return nil, err
	}
	if item.GetLogId() == 0 {
		return nil, errors.New("log_id is required")
	}
	updatedAt := item.GetUpdatedAt()
	if updatedAt == "" {
		updatedAt = s.viewRebuildLogNow()
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE t_view_rebuild_logs SET
			c_result = ?, c_block_reason = ?,
			c_target_view_revision = ?, c_active_view_revision = ?, c_physical_bytes = ?,
			c_num_pending = ?, c_num_ack_pending = ?, c_entries_written = ?,
			c_started_at = ?, c_finished_at = ?, c_first_checked_at = ?,
			c_last_checked_at = ?, c_skip_count = ?, c_error_summary = ?,
			c_details_json = ?, c_updated_at = ?
		WHERE c_log_id = ?
		  AND c_space_id = ? AND c_view_id = ? AND c_build_id = ? AND c_index_id = ?
		  AND c_result = ?
	`, item.GetResult(), item.GetBlockReason(), item.GetTargetViewRevision(), item.GetActiveViewRevision(), item.GetPhysicalBytes(),
		item.GetNumPending(), item.GetNumAckPending(), item.GetEntriesWritten(), item.GetStartedAt(), item.GetFinishedAt(),
		item.GetFirstCheckedAt(), item.GetLastCheckedAt(), item.GetSkipCount(), item.GetErrorSummary(), defaultJSON(item.GetDetailsJson()), updatedAt,
		item.GetLogId(), item.GetSpaceId(), item.GetViewId(), item.GetBuildId(), item.GetIndexId(), pb.ViewRebuildResult_VIEW_REBUILD_RESULT_RUNNING)
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if affected == 0 {
		return nil, fmt.Errorf("view rebuild log %d not found", item.GetLogId())
	}
	return s.getViewRebuildLog(ctx, s.db, item.GetLogId())
}

func (s *Store) UpsertSkippedViewRebuildLog(ctx context.Context, item *pb.ViewRebuildLog) (*pb.ViewRebuildLog, error) {
	if err := validateViewRebuildLog(item); err != nil {
		return nil, err
	}
	if item.GetResult() != pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SKIPPED {
		return nil, errors.New("skipped rebuild log must use skipped result")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var logID int64
	var latestResult, latestReason int32
	var latestBlockReason string
	err = tx.QueryRowContext(ctx, `
		SELECT c_log_id, c_trigger_reason, c_result, c_block_reason
		FROM t_view_rebuild_logs
		WHERE c_space_id = ? AND c_view_id = ?
		ORDER BY c_log_id DESC LIMIT 1
	`, item.GetSpaceId(), item.GetViewId()).Scan(&logID, &latestReason, &latestResult, &latestBlockReason)
	if err == nil && (pb.ViewRebuildTriggerReason(latestReason) != item.GetTriggerReason() || pb.ViewRebuildResult(latestResult) != item.GetResult() || latestBlockReason != item.GetBlockReason()) {
		// Only merge a skip into the immediately preceding log. A successful or
		// failed build closes the prior skip streak; a later skip starts a new
		// audit row even when its reason is identical.
		err = sql.ErrNoRows
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	now := s.viewRebuildLogNow()
	if errors.Is(err, sql.ErrNoRows) {
		if item.FirstCheckedAt == "" {
			item.FirstCheckedAt = now
		}
		if item.LastCheckedAt == "" {
			item.LastCheckedAt = now
		}
		if item.SkipCount == 0 {
			item.SkipCount = 1
		}
		if item.CreatedAt == "" {
			item.CreatedAt = now
		}
		if item.UpdatedAt == "" {
			item.UpdatedAt = now
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO t_view_rebuild_logs (
				c_space_id, c_view_id, c_build_id, c_index_id, c_trigger_reason,
				c_result, c_block_reason, c_target_view_revision, c_active_view_revision,
				c_physical_bytes, c_num_pending, c_num_ack_pending, c_entries_written,
				c_started_at, c_finished_at, c_first_checked_at, c_last_checked_at,
				c_skip_count, c_error_summary, c_details_json, c_created_at, c_updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, item.GetSpaceId(), item.GetViewId(), item.GetBuildId(), item.GetIndexId(), item.GetTriggerReason(), item.GetResult(), item.GetBlockReason(),
			item.GetTargetViewRevision(), item.GetActiveViewRevision(), item.GetPhysicalBytes(), item.GetNumPending(), item.GetNumAckPending(), item.GetEntriesWritten(),
			item.GetStartedAt(), item.GetFinishedAt(), item.GetFirstCheckedAt(), item.GetLastCheckedAt(), item.GetSkipCount(), item.GetErrorSummary(), defaultJSON(item.GetDetailsJson()), item.GetCreatedAt(), item.GetUpdatedAt())
		if err != nil {
			return nil, err
		}
		logID, err = result.LastInsertId()
		if err != nil {
			return nil, err
		}
	} else {
		var oldSkip uint64
		if err := tx.QueryRowContext(ctx, `SELECT c_skip_count FROM t_view_rebuild_logs WHERE c_log_id = ?`, logID).Scan(&oldSkip); err != nil {
			return nil, err
		}
		item.LogId = logID
		item.SkipCount = oldSkip + 1
		if item.FirstCheckedAt == "" {
			if err := tx.QueryRowContext(ctx, `SELECT c_first_checked_at FROM t_view_rebuild_logs WHERE c_log_id = ?`, logID).Scan(&item.FirstCheckedAt); err != nil {
				return nil, err
			}
		}
		item.LastCheckedAt = now
		item.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `
			UPDATE t_view_rebuild_logs SET c_build_id = ?, c_index_id = ?, c_target_view_revision = ?,
				c_active_view_revision = ?, c_physical_bytes = ?, c_num_pending = ?, c_num_ack_pending = ?,
				c_entries_written = ?, c_last_checked_at = ?, c_skip_count = ?, c_error_summary = ?,
				c_details_json = ?, c_updated_at = ? WHERE c_log_id = ?
		`, item.GetBuildId(), item.GetIndexId(), item.GetTargetViewRevision(), item.GetActiveViewRevision(), item.GetPhysicalBytes(),
			item.GetNumPending(), item.GetNumAckPending(), item.GetEntriesWritten(), item.GetLastCheckedAt(), item.GetSkipCount(), item.GetErrorSummary(), defaultJSON(item.GetDetailsJson()), item.GetUpdatedAt(), logID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getViewRebuildLog(ctx, s.db, logID)
}

func (s *Store) ListViewRebuildLogs(ctx context.Context, spaceID string, viewID string, result pb.ViewRebuildResult, page *pb.Page) ([]*pb.ViewRebuildLog, *pb.PageResult, error) {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(viewID) == "" {
		return nil, nil, errors.New("space_id and view_id are required")
	}
	if result < pb.ViewRebuildResult_VIEW_REBUILD_RESULT_UNSPECIFIED || result > pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SKIPPED {
		return nil, nil, errors.New("invalid view rebuild result")
	}
	where := ` WHERE c_space_id = ? AND c_view_id = ?`
	args := []any{spaceID, viewID}
	if result != pb.ViewRebuildResult_VIEW_REBUILD_RESULT_UNSPECIFIED {
		where += ` AND c_result = ?`
		args = append(args, result)
	}
	pageNo, size, offset := normalizePage(page)
	queryArgs := append(append([]any{}, args...), int(size), offset)
	rows, err := s.queryDB(ctx).QueryContext(ctx, `SELECT `+viewRebuildLogColumns+` FROM t_view_rebuild_logs`+where+` ORDER BY c_created_at DESC, c_log_id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := make([]*pb.ViewRebuildLog, 0)
	for rows.Next() {
		item, err := scanViewRebuildLog(rows)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	total, err := countRows(ctx, s.queryDB(ctx), `SELECT COUNT(1) FROM t_view_rebuild_logs`+where, args)
	if err != nil {
		return nil, nil, err
	}
	return items, &pb.PageResult{Page: pageNo, Size: size, Total: total, HasMore: uint64(offset)+uint64(len(items)) < uint64(total)}, nil
}
