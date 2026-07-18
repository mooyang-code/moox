package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	coreviewindex "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。

func mergeViewIndexState(existing *pb.View, item *pb.View, shapeChanged bool) {
	if existing == nil {
		if item.ViewVersion == 0 {
			item.ViewVersion = 1
		}
		return
	}
	item.ActiveIndexId = existing.GetActiveIndexId()
	item.ActiveViewVersion = existing.GetActiveViewVersion()
	item.ActiveColumns = cloneViewColumns(existing.GetActiveColumns())
	item.ActiveSchemaHash = existing.GetActiveSchemaHash()
	item.IndexedFrom = existing.GetIndexedFrom()
	item.IndexedTo = existing.GetIndexedTo()
	item.ViewVersion = existing.GetViewVersion()
	if item.ViewVersion == 0 {
		item.ViewVersion = 1
	}
	if shapeChanged {
		item.ViewVersion++
	}
}

func viewIndexShapeChanged(existing *pb.View, next *pb.View) bool {
	if existing.GetPrimaryDatasetId() != next.GetPrimaryDatasetId() {
		return true
	}
	if !slices.Equal(existing.GetDatasetIds(), next.GetDatasetIds()) {
		return true
	}
	if !slices.Equal(existing.GetGrainKeys(), next.GetGrainKeys()) {
		return true
	}
	if existing.GetFilterJson() != next.GetFilterJson() {
		return true
	}
	if existing.GetEngine() != next.GetEngine() {
		return true
	}
	return existing.GetRetentionWindow() != next.GetRetentionWindow()
}

func bumpViewVersion(ctx context.Context, tx *sql.Tx, spaceID string, viewID string) error {
	view, err := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{spaceID, viewID}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return err
	}
	if view.ViewVersion == 0 {
		view.ViewVersion = 1
	}
	view.ViewVersion++
	view.Columns = nil
	view.IndexBuild = nil
	raw, err := marshal(view)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE t_views SET c_view_version = ?, c_attrs_json = ?
		WHERE c_space_id = ? AND c_view_id = ?
	`, view.GetViewVersion(), raw, spaceID, viewID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM t_view_index_builds WHERE c_space_id = ? AND c_view_id = ?`, spaceID, viewID); err != nil {
		return err
	}
	return nil
}

func (s *Store) GetViewIndexBuild(ctx context.Context, spaceID string, viewID string) (*pb.ViewIndexBuild, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+viewIndexBuildColumns+`
		FROM t_view_index_builds WHERE c_space_id = ? AND c_view_id = ?`, spaceID, viewID)
	return scanViewIndexBuild(row)
}

func (s *Store) ClaimViewIndexBuild(ctx context.Context, req *pb.ClaimViewIndexBuildReq) (*pb.ViewIndexBuild, bool, error) {
	if err := validateClaimViewIndexBuild(req); err != nil {
		return nil, false, err
	}
	previous, previousErr := s.GetViewIndexBuild(ctx, req.GetSpaceId(), req.GetViewId())
	if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
		return nil, false, previousErr
	}
	now := s.nowUTC()
	nowText := now.Format(sqliteBuildTimestampLayout)
	leaseText := now.Add(buildLeaseTTL(req.GetLeaseTtlSeconds())).Format(sqliteBuildTimestampLayout)
	columnsJSON, err := marshalJSON(req.GetColumns())
	if err != nil {
		return nil, false, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO t_view_index_builds (`+viewIndexBuildColumns+`)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, '', '', 0, ?, ?, ?, ?, '', ''
		FROM t_views
		WHERE c_space_id = ? AND c_view_id = ? AND c_view_version = ? AND c_active_index_id = ?
		ON CONFLICT(c_space_id, c_view_id) DO UPDATE SET
			c_build_id = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_build_id ELSE excluded.c_build_id END,
			c_index_id = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_index_id ELSE excluded.c_index_id END,
			c_engine = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_engine ELSE excluded.c_engine END,
			c_target_view_version = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_target_view_version ELSE excluded.c_target_view_version END,
			c_state = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_state ELSE excluded.c_state END,
			c_owner_id = excluded.c_owner_id,
			c_lease_expires_at = excluded.c_lease_expires_at,
			c_cursor_json = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_cursor_json ELSE '' END,
			c_snapshot_end = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_snapshot_end ELSE excluded.c_snapshot_end END,
			c_coverage_start = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_coverage_start ELSE '' END,
			c_coverage_end = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_coverage_end ELSE '' END,
			c_entries_written = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_entries_written ELSE 0 END,
			c_schema_hash = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_schema_hash ELSE excluded.c_schema_hash END,
			c_columns_json = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_columns_json ELSE excluded.c_columns_json END,
			c_started_at = CASE WHEN t_view_index_builds.c_build_id = excluded.c_build_id THEN t_view_index_builds.c_started_at ELSE excluded.c_started_at END,
			c_updated_at = excluded.c_updated_at,
			c_finished_at = '',
			c_error = ''
		WHERE (t_view_index_builds.c_target_view_version < excluded.c_target_view_version)
		   OR (t_view_index_builds.c_state = ?)
		   OR (t_view_index_builds.c_build_id = excluded.c_build_id AND t_view_index_builds.c_lease_expires_at <= ?)
	`, req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetIndexId(), req.GetEngine(), req.GetTargetViewVersion(), pb.ViewIndexBuild_PREPARING,
		req.GetOwnerId(), leaseText, req.GetSnapshotEnd(), req.GetSchemaHash(), columnsJSON, nowText, nowText,
		req.GetSpaceId(), req.GetViewId(), req.GetTargetViewVersion(), req.GetExpectedActiveIndexId(), pb.ViewIndexBuild_FAILED, nowText)
	if err != nil {
		return nil, false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if rows == 0 {
		return nil, false, ErrViewIndexBuildConflict
	}
	build, err := s.GetViewIndexBuild(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return nil, false, err
	}
	resumed := previous != nil && previous.GetBuildId() == req.GetBuildId()
	return build, resumed, nil
}

func (s *Store) UpdateViewIndexBuild(ctx context.Context, req *pb.UpdateViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	if err := validateUpdateViewIndexBuild(req); err != nil {
		return nil, err
	}
	now := s.nowUTC()
	nowText := now.Format(sqliteBuildTimestampLayout)
	leaseText := now.Add(buildLeaseTTL(req.GetLeaseTtlSeconds())).Format(sqliteBuildTimestampLayout)
	finishedAt := ""
	if req.GetNextState() == pb.ViewIndexBuild_READY || req.GetNextState() == pb.ViewIndexBuild_FAILED {
		finishedAt = nowText
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE t_view_index_builds SET
			c_state = ?, c_lease_expires_at = ?,
			c_cursor_json = CASE WHEN ? <> '' THEN ? ELSE c_cursor_json END,
			c_snapshot_end = CASE WHEN ? <> '' THEN ? ELSE c_snapshot_end END,
			c_coverage_start = CASE WHEN ? <> '' THEN ? ELSE c_coverage_start END,
			c_coverage_end = CASE WHEN ? <> '' THEN ? ELSE c_coverage_end END,
			c_entries_written = CASE WHEN ? > 0 THEN ? ELSE c_entries_written END,
			c_updated_at = ?,
			c_finished_at = CASE WHEN ? <> '' THEN ? ELSE c_finished_at END
		WHERE c_space_id = ? AND c_view_id = ? AND c_build_id = ? AND c_owner_id = ?
		  AND c_state = ? AND c_lease_expires_at >= ?
	`, req.GetNextState(), leaseText,
		req.GetCursorJson(), req.GetCursorJson(), req.GetSnapshotEnd(), req.GetSnapshotEnd(),
		req.GetCoverageStart(), req.GetCoverageStart(), req.GetCoverageEnd(), req.GetCoverageEnd(),
		req.GetEntriesWritten(), req.GetEntriesWritten(), nowText, finishedAt, finishedAt,
		req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetOwnerId(), req.GetExpectedState(), nowText)
	if err != nil {
		return nil, err
	}
	if err := requireChangedRow(res); err != nil {
		return nil, err
	}
	return s.GetViewIndexBuild(ctx, req.GetSpaceId(), req.GetViewId())
}

func (s *Store) ActivateViewIndex(ctx context.Context, req *pb.ActivateViewIndexReq) (*pb.View, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" || req.GetBuildId() == "" || req.GetOwnerId() == "" {
		return nil, errors.New("space_id, view_id, build_id and owner_id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	nowText := s.nowUTC().Format(sqliteBuildTimestampLayout)
	build, err := scanViewIndexBuild(tx.QueryRowContext(ctx, `SELECT `+viewIndexBuildColumns+`
		FROM t_view_index_builds
		WHERE c_space_id = ? AND c_view_id = ? AND c_build_id = ? AND c_owner_id = ? AND c_state = ?
		  AND c_lease_expires_at >= ?`,
		req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetOwnerId(), pb.ViewIndexBuild_READY, nowText))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrViewIndexBuildConflict
	}
	if err != nil {
		return nil, err
	}
	view, err := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{req.GetSpaceId(), req.GetViewId()}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, err
	}
	if view.GetViewVersion() != build.GetTargetViewVersion() {
		return nil, ErrViewIndexBuildConflict
	}
	view.ActiveIndexId = build.GetIndexId()
	view.ActiveViewVersion = build.GetTargetViewVersion()
	view.ActiveColumns = cloneViewColumns(build.GetColumns())
	view.ActiveSchemaHash = build.GetSchemaHash()
	view.IndexedFrom = build.GetCoverageStart()
	view.IndexedTo = build.GetCoverageEnd()
	view.Columns = nil
	view.IndexBuild = nil
	raw, err := marshal(view)
	if err != nil {
		return nil, err
	}
	activeColumns, err := marshalJSON(view.GetActiveColumns())
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE t_views SET c_active_index_id = ?, c_active_view_version = ?,
			c_active_columns_json = ?, c_active_schema_hash = ?,
		c_indexed_from = ?, c_indexed_to = ?, c_attrs_json = ?
		WHERE c_space_id = ? AND c_view_id = ? AND c_view_version = ?
	`, view.GetActiveIndexId(), view.GetActiveViewVersion(), activeColumns, view.GetActiveSchemaHash(),
		view.GetIndexedFrom(), view.GetIndexedTo(), raw, view.GetSpaceId(), view.GetViewId(), build.GetTargetViewVersion())
	if err != nil {
		return nil, err
	}
	if err := requireChangedRow(res); err != nil {
		return nil, err
	}
	res, err = tx.ExecContext(ctx, `DELETE FROM t_view_index_builds
		WHERE c_space_id = ? AND c_view_id = ? AND c_build_id = ? AND c_owner_id = ? AND c_state = ?`,
		req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetOwnerId(), pb.ViewIndexBuild_READY)
	if err != nil {
		return nil, err
	}
	if err := requireChangedRow(res); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetView(ctx, req.GetSpaceId(), req.GetViewId())
}

func (s *Store) FailViewIndexBuild(ctx context.Context, req *pb.FailViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" || req.GetBuildId() == "" || req.GetOwnerId() == "" {
		return nil, errors.New("space_id, view_id, build_id and owner_id are required")
	}
	nowText := s.nowUTC().Format(sqliteBuildTimestampLayout)
	message := strings.TrimSpace(req.GetError())
	if message == "" {
		message = "view index build failed"
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE t_view_index_builds SET c_state = ?, c_error = ?, c_updated_at = ?, c_finished_at = ?
		WHERE c_space_id = ? AND c_view_id = ? AND c_build_id = ? AND c_owner_id = ?
		  AND c_state IN (?, ?, ?) AND c_lease_expires_at >= ?
	`, pb.ViewIndexBuild_FAILED, message, nowText, nowText, req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetOwnerId(),
		pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP, nowText)
	if err != nil {
		return nil, err
	}
	if err := requireChangedRow(res); err != nil {
		return nil, err
	}
	return s.GetViewIndexBuild(ctx, req.GetSpaceId(), req.GetViewId())
}

func validateClaimViewIndexBuild(req *pb.ClaimViewIndexBuildReq) error {
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" || req.GetBuildId() == "" || req.GetIndexId() == "" ||
		req.GetEngine() == "" || req.GetTargetViewVersion() == 0 || req.GetOwnerId() == "" || req.GetSchemaHash() == "" {
		return errors.New("space_id, view_id, build_id, index_id, engine, target_view_version, owner_id and schema_hash are required")
	}
	ref, err := coreviewindex.ParseViewIndexID(req.GetIndexId())
	if err != nil {
		return err
	}
	if ref.SpaceID != req.GetSpaceId() || ref.ViewID != req.GetViewId() {
		return errors.New("index_id does not match space_id/view_id")
	}
	switch strings.ToLower(strings.TrimSpace(req.GetEngine())) {
	case "duckdb", "bleve":
	default:
		return errors.New("unsupported view index engine " + req.GetEngine())
	}
	want := coreviewindex.InactiveViewIndexID(req.GetSpaceId(), req.GetViewId(), req.GetExpectedActiveIndexId())
	if req.GetIndexId() != want {
		return fmt.Errorf("index_id must be inactive slot %s", want)
	}
	return nil
}

func validateUpdateViewIndexBuild(req *pb.UpdateViewIndexBuildReq) error {
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" || req.GetBuildId() == "" || req.GetOwnerId() == "" {
		return errors.New("space_id, view_id, build_id and owner_id are required")
	}
	if !validViewIndexBuildTransition(req.GetExpectedState(), req.GetNextState()) {
		return fmt.Errorf("invalid view index build transition %s -> %s", req.GetExpectedState(), req.GetNextState())
	}
	return nil
}

func validViewIndexBuildTransition(from pb.ViewIndexBuild_State, to pb.ViewIndexBuild_State) bool {
	switch from {
	case pb.ViewIndexBuild_PREPARING:
		return to == pb.ViewIndexBuild_BUILDING
	case pb.ViewIndexBuild_BUILDING:
		return to == pb.ViewIndexBuild_BUILDING || to == pb.ViewIndexBuild_CATCHING_UP
	case pb.ViewIndexBuild_CATCHING_UP:
		return to == pb.ViewIndexBuild_CATCHING_UP || to == pb.ViewIndexBuild_READY
	default:
		return false
	}
}

func scanViewIndexBuild(row rowScanner) (*pb.ViewIndexBuild, error) {
	build := &pb.ViewIndexBuild{}
	var state int32
	var columnsJSON string
	if err := row.Scan(
		&build.SpaceId, &build.ViewId, &build.BuildId, &build.IndexId, &build.Engine,
		&build.TargetViewVersion, &state, &build.OwnerId, &build.LeaseExpiresAt,
		&build.CursorJson, &build.SnapshotEnd, &build.CoverageStart, &build.CoverageEnd,
		&build.EntriesWritten, &build.SchemaHash, &columnsJSON, &build.StartedAt,
		&build.UpdatedAt, &build.FinishedAt, &build.Error,
	); err != nil {
		return nil, err
	}
	build.State = pb.ViewIndexBuild_State(state)
	if err := json.Unmarshal([]byte(columnsJSON), &build.Columns); err != nil {
		return nil, err
	}
	return build, nil
}
