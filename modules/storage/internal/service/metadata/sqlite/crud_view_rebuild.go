package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// RequestViewRebuild advances only the desired View revision. The active
// index and its coverage remain untouched until the View Maintainer has prepared,
// backfilled, and atomically activated the replacement index.
func (s *Store) RequestViewRebuild(ctx context.Context, spaceID, viewID string) (*pb.View, error) {
	if spaceID == "" || viewID == "" {
		return nil, errors.New("space_id and view_id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	view, err := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{spaceID, viewID}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, err
	}
	// Dataset/field mutations bump the authoritative revision column without
	// rewriting the denormalized protobuf JSON. Always use the columns inside
	// the same transaction so a manual request cannot silently lose that bump.
	var desiredRevision, activeRevision uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT c_desired_view_revision, c_active_view_revision
		FROM t_views WHERE c_space_id = ? AND c_view_id = ?
	`, spaceID, viewID).Scan(&desiredRevision, &activeRevision); err != nil {
		return nil, err
	}
	view.DesiredViewRevision = desiredRevision
	view.ActiveViewRevision = activeRevision
	revision := desiredRevision
	if revision == 0 {
		revision = 1
	}
	previousRevision := revision
	// A manual request is idempotent while the revision-scoped build is still
	// active. The browser may retry after a lost HTTP response; incrementing
	// here would stale the in-flight build and start another full Primary scan.
	manualRevision := strings.TrimSpace(view.GetAttributes()[coremetadata.ManualRebuildRevisionAttribute])
	if previousRevision > view.GetActiveViewRevision() && manualRevision == strconv.FormatUint(previousRevision, 10) {
		var activeBuilds, failedBuilds int
		err := tx.QueryRowContext(ctx, `
			SELECT
				COALESCE(SUM(CASE WHEN c_status IN ('building', 'ready') THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN c_status = 'failed' THEN 1 ELSE 0 END), 0)
			FROM t_view_index_builds WHERE c_space_id = ? AND c_view_id = ?
		`, spaceID, viewID).Scan(&activeBuilds, &failedBuilds)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		// No build row means the first request may have committed while the
		// View Maintainer has not claimed it yet. Treat that retry as idempotent;
		// only a recorded failed build permits an explicit retry to advance the
		// revision again.
		if activeBuilds > 0 || failedBuilds == 0 {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return s.GetView(ctx, spaceID, viewID)
		}
	}
	revision++
	view.DesiredViewRevision = revision
	if view.Attributes == nil {
		view.Attributes = make(map[string]string)
	}
	view.Attributes[coremetadata.ManualRebuildRevisionAttribute] = strconv.FormatUint(revision, 10)
	view.Attributes[coremetadata.ManualRebuildRevisionAttribute+"_requested_at"] = s.nowUTC().Format(sqliteBuildTimestampLayout)
	view.Columns = nil
	view.IndexBuild = nil
	raw, err := marshal(view)
	if err != nil {
		return nil, err
	}
	now := s.nowUTC().Format(sqliteBuildTimestampLayout)
	result, err := tx.ExecContext(ctx, `
		UPDATE t_views SET c_desired_view_revision = ?, c_attrs_json = ?, c_mtime = ?
		WHERE c_space_id = ? AND c_view_id = ? AND c_desired_view_revision = ?
	`, revision, raw, now, spaceID, viewID, previousRevision)
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if affected != 1 {
		// Another request won the revision race. Return its current state rather
		// than creating a second build or reporting a misleading failure.
		_ = tx.Rollback()
		return s.GetView(ctx, spaceID, viewID)
	}
	// A failed build for the previous revision must not prevent the requested
	// revision from being claimed by the View Maintainer.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM t_view_index_builds
		WHERE c_space_id = ? AND c_view_id = ? AND c_status = 'failed'
	`, spaceID, viewID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetView(ctx, spaceID, viewID)
}
