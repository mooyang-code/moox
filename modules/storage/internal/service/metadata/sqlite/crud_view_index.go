package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	coreviewindex "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const viewIndexBuildColumns = `
	c_space_id, c_view_id, c_build_id, c_index_id, c_engine,
	c_target_view_version, c_state, c_owner_id, c_new_slot, c_status,
	c_started_at, c_backfilled_rows, c_safe_error, c_updated_at`

func mergeViewIndexState(existing *pb.View, item *pb.View, shapeChanged bool) {
	if existing == nil {
		if item.DesiredViewRevision == 0 {
			item.DesiredViewRevision = 1
		}
		if item.ActiveSlot == "" {
			item.ActiveSlot = "slot-a"
		}
		return
	}
	item.ActiveIndexId = existing.GetActiveIndexId()
	item.ActiveViewRevision = existing.GetActiveViewRevision()
	item.ActiveColumns = cloneViewColumns(existing.GetActiveColumns())
	item.ActiveViewSchemaHash = existing.GetActiveViewSchemaHash()
	item.ActiveSlot = existing.GetActiveSlot()
	item.IndexedFrom = existing.GetIndexedFrom()
	item.IndexedTo = existing.GetIndexedTo()
	item.DesiredViewRevision = existing.GetDesiredViewRevision()
	if item.DesiredViewRevision == 0 {
		item.DesiredViewRevision = 1
	}
	if shapeChanged {
		item.DesiredViewRevision++
	}
}

func viewIndexShapeChanged(existing *pb.View, next *pb.View) bool {
	return existing.GetPrimaryDatasetId() != next.GetPrimaryDatasetId() ||
		!slices.Equal(existing.GetDatasetIds(), next.GetDatasetIds()) ||
		!slices.Equal(existing.GetGrainKeys(), next.GetGrainKeys()) ||
		existing.GetFilterJson() != next.GetFilterJson() ||
		existing.GetEngine() != next.GetEngine() ||
		existing.GetKeepDuration() != next.GetKeepDuration()
}

func bumpViewVersion(ctx context.Context, tx *sql.Tx, spaceID, viewID string) error {
	view, err := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{spaceID, viewID}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return err
	}
	if view.DesiredViewRevision == 0 {
		view.DesiredViewRevision = 1
	}
	view.DesiredViewRevision++
	view.Columns = nil
	view.IndexBuild = nil
	raw, err := marshal(view)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE t_views SET c_desired_view_revision = ?, c_attrs_json = ?
		WHERE c_space_id = ? AND c_view_id = ?
	`, view.GetDesiredViewRevision(), raw, spaceID, viewID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM t_view_index_builds WHERE c_space_id = ? AND c_view_id = ? AND c_status = 'failed'`, spaceID, viewID)
	return err
}

func (s *Store) GetViewIndexBuild(ctx context.Context, spaceID, viewID string) (*pb.ViewIndexBuild, error) {
	return scanViewIndexBuild(s.db.QueryRowContext(ctx, `SELECT `+viewIndexBuildColumns+`
		FROM t_view_index_builds WHERE c_space_id = ? AND c_view_id = ?`, spaceID, viewID))
}

func (s *Store) ClaimViewIndexBuild(ctx context.Context, req *pb.ClaimViewIndexBuildReq) (*pb.ViewIndexBuild, bool, error) {
	if err := validateClaimViewIndexBuild(req); err != nil {
		return nil, false, err
	}
	previous, previousErr := s.GetViewIndexBuild(ctx, req.GetSpaceId(), req.GetViewId())
	if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
		return nil, false, previousErr
	}
	ref, _ := coreviewindex.ParseViewIndexID(req.GetIndexId())
	now := s.nowUTC().Format(sqliteBuildTimestampLayout)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO t_view_index_builds (`+viewIndexBuildColumns+`)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, 'building', ?, 0, '', ?
		FROM t_views
		WHERE c_space_id = ? AND c_view_id = ? AND c_desired_view_revision = ? AND c_active_index_id = ?
		ON CONFLICT(c_space_id, c_view_id) DO UPDATE SET
			c_build_id = excluded.c_build_id,
			c_index_id = excluded.c_index_id,
			c_engine = excluded.c_engine,
			c_target_view_version = excluded.c_target_view_version,
			c_state = excluded.c_state,
			c_owner_id = excluded.c_owner_id,
			c_new_slot = excluded.c_new_slot,
			c_status = 'building',
			c_started_at = excluded.c_started_at,
			c_backfilled_rows = 0,
			c_safe_error = '',
			c_updated_at = excluded.c_updated_at
		WHERE t_view_index_builds.c_status = 'failed'
		   OR t_view_index_builds.c_target_view_version < excluded.c_target_view_version
		   OR t_view_index_builds.c_build_id = excluded.c_build_id
	`, req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetIndexId(), strings.ToLower(req.GetEngine()),
		req.GetTargetViewVersion(), pb.ViewIndexBuild_PREPARING, req.GetOwnerId(), string(ref.Slot), now, now,
		req.GetSpaceId(), req.GetViewId(), req.GetTargetViewVersion(), req.GetExpectedActiveIndexId())
	if err != nil {
		return nil, false, err
	}
	if err := requireChangedRow(res); err != nil {
		return nil, false, err
	}
	build, err := s.GetViewIndexBuild(ctx, req.GetSpaceId(), req.GetViewId())
	return build, previous != nil && previous.GetBuildId() == req.GetBuildId(), err
}

func (s *Store) UpdateViewIndexBuild(ctx context.Context, req *pb.UpdateViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	if err := validateUpdateViewIndexBuild(req); err != nil {
		return nil, err
	}
	status := "building"
	if req.GetNextState() == pb.ViewIndexBuild_READY {
		status = "ready"
	}
	now := s.nowUTC().Format(sqliteBuildTimestampLayout)
	res, err := s.db.ExecContext(ctx, `
		UPDATE t_view_index_builds SET
			c_state = ?, c_status = ?,
			c_backfilled_rows = CASE WHEN ? > 0 THEN ? ELSE c_backfilled_rows END,
			c_updated_at = ?
		WHERE c_space_id = ? AND c_view_id = ? AND c_build_id = ? AND c_owner_id = ? AND c_state = ?
	`, req.GetNextState(), status, req.GetEntriesWritten(), req.GetEntriesWritten(), now,
		req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetOwnerId(), req.GetExpectedState())
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
	build, err := scanViewIndexBuild(tx.QueryRowContext(ctx, `SELECT `+viewIndexBuildColumns+`
		FROM t_view_index_builds
		WHERE c_space_id = ? AND c_view_id = ? AND c_build_id = ? AND c_owner_id = ? AND c_status = 'ready'`,
		req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetOwnerId()))
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
	if view.GetDesiredViewRevision() != build.GetTargetViewVersion() {
		return nil, ErrViewIndexBuildConflict
	}
	columns, err := queryMessages(ctx, tx, `SELECT c_attrs_json FROM t_view_columns WHERE c_space_id = ? AND c_view_id = ? ORDER BY c_sort_order, c_column_name`,
		[]any{view.GetSpaceId(), view.GetViewId()}, func() *pb.ViewColumn { return &pb.ViewColumn{} })
	if err != nil {
		return nil, err
	}
	ref, err := coreviewindex.ParseViewIndexID(build.GetIndexId())
	if err != nil {
		return nil, err
	}
	view.ActiveIndexId = build.GetIndexId()
	view.ActiveViewRevision = build.GetTargetViewVersion()
	view.ActiveColumns = cloneViewColumns(columns)
	view.ActiveSlot = string(ref.Slot)
	view.ActiveViewSchemaHash = coreviewindex.HashViewIndexSchema(coreviewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), PrimaryDatasetID: view.GetPrimaryDatasetId(), ViewVersion: view.GetActiveViewRevision(), Engine: view.GetEngine(), Columns: columns,
	})
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
		UPDATE t_views SET c_active_index_id = ?, c_active_view_revision = ?,
			c_active_columns_json = ?, c_active_view_schema_hash = ?, c_active_slot = ?, c_attrs_json = ?
		WHERE c_space_id = ? AND c_view_id = ? AND c_desired_view_revision = ?
	`, view.GetActiveIndexId(), view.GetActiveViewRevision(), activeColumns, view.GetActiveViewSchemaHash(), view.GetActiveSlot(), raw,
		view.GetSpaceId(), view.GetViewId(), build.GetTargetViewVersion())
	if err != nil {
		return nil, err
	}
	if err := requireChangedRow(res); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM t_view_index_builds WHERE c_space_id = ? AND c_view_id = ?`, view.GetSpaceId(), view.GetViewId()); err != nil {
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
	message := strings.TrimSpace(req.GetError())
	if message == "" {
		message = "view index build failed"
	}
	now := s.nowUTC().Format(sqliteBuildTimestampLayout)
	res, err := s.db.ExecContext(ctx, `
		UPDATE t_view_index_builds SET c_state = ?, c_status = 'failed', c_safe_error = ?, c_updated_at = ?
		WHERE c_space_id = ? AND c_view_id = ? AND c_build_id = ? AND c_owner_id = ? AND c_status IN ('building', 'ready')
	`, pb.ViewIndexBuild_FAILED, message, now, req.GetSpaceId(), req.GetViewId(), req.GetBuildId(), req.GetOwnerId())
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
		req.GetEngine() == "" || req.GetTargetViewVersion() == 0 || req.GetOwnerId() == "" {
		return errors.New("space_id, view_id, build_id, index_id, engine, target_view_version and owner_id are required")
	}
	ref, err := coreviewindex.ParseViewIndexID(req.GetIndexId())
	if err != nil {
		return err
	}
	if ref.SpaceID != req.GetSpaceId() || ref.ViewID != req.GetViewId() {
		return errors.New("index_id does not match space_id/view_id")
	}
	if engine := strings.ToLower(strings.TrimSpace(req.GetEngine())); engine != "duckdb" && engine != "bleve" {
		return fmt.Errorf("unsupported view index engine %q", req.GetEngine())
	}
	if want := coreviewindex.InactiveViewIndexID(req.GetSpaceId(), req.GetViewId(), req.GetExpectedActiveIndexId()); req.GetIndexId() != want {
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

func validViewIndexBuildTransition(from, to pb.ViewIndexBuild_State) bool {
	switch from {
	case pb.ViewIndexBuild_PREPARING:
		return to == pb.ViewIndexBuild_BUILDING
	case pb.ViewIndexBuild_BUILDING:
		return to == pb.ViewIndexBuild_BUILDING || to == pb.ViewIndexBuild_CATCHING_UP || to == pb.ViewIndexBuild_READY
	case pb.ViewIndexBuild_CATCHING_UP:
		return to == pb.ViewIndexBuild_CATCHING_UP || to == pb.ViewIndexBuild_READY
	default:
		return false
	}
}

func scanViewIndexBuild(row rowScanner) (*pb.ViewIndexBuild, error) {
	build := &pb.ViewIndexBuild{}
	var state int32
	var newSlot, status, safeError string
	if err := row.Scan(
		&build.SpaceId, &build.ViewId, &build.BuildId, &build.IndexId, &build.Engine,
		&build.TargetViewVersion, &state, &build.OwnerId, &newSlot, &status,
		&build.StartedAt, &build.EntriesWritten, &safeError, &build.UpdatedAt,
	); err != nil {
		return nil, err
	}
	build.State = pb.ViewIndexBuild_State(state)
	build.Error = safeError
	if status == "ready" {
		build.State = pb.ViewIndexBuild_READY
	} else if status == "failed" {
		build.State = pb.ViewIndexBuild_FAILED
	}
	return build, nil
}
