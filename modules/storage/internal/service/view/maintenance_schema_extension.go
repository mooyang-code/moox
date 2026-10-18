package view

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func (s *Service) extendActiveFactorResultSchema(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View, engine viewindex.Engine) (bool, error) {
	return s.extendFactorResultSchema(ctx, opts, auth, view, engine, false)
}

// recoverFactorResultSchemaExtension handles the durable window where DuckDB
// committed an append-only revision but the Metadata CAS did not. It accepts
// only physical revisions that can be a prefix of the current desired schema;
// ExtendColumns performs the authoritative physical-column validation.
func (s *Service) recoverFactorResultSchemaExtension(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View, engine viewindex.Engine, stats viewindex.ViewIndexStats) (bool, error) {
	current, next, _, ok, err := factorResultSchemaExtensionTarget(ctx, opts, auth, view, engine)
	if err != nil || !ok {
		return false, err
	}
	if !stats.Exists || stats.ViewVersion <= current.ViewVersion || stats.ViewVersion > next.ViewVersion {
		return false, nil
	}
	if stats.ViewVersion == next.ViewVersion && strings.TrimSpace(stats.SchemaHash) != next.SchemaHash {
		return false, nil
	}
	recovered, err := s.extendFactorResultSchema(ctx, opts, auth, view, engine, true)
	if errors.Is(err, viewindex.ErrSchemaExtensionConflict) {
		return false, nil
	}
	return recovered, err
}

func (s *Service) extendFactorResultSchema(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View, engine viewindex.Engine, allowDetached bool) (bool, error) {
	current, next, extender, ok, err := factorResultSchemaExtensionTarget(ctx, opts, auth, view, engine)
	if err != nil || !ok {
		return false, err
	}
	if opts.Metadata == nil {
		return false, errors.New("Metadata client is required to commit an active View schema extension")
	}
	desiredColumns := next.Columns
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	s.mu.RLock()
	runtime := s.views[viewKey]
	s.mu.RUnlock()
	if runtime == nil {
		if !allowDetached {
			return false, errors.New("active View runtime is not attached for schema extension")
		}
	} else {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		if runtime.active != view.GetActiveIndexId() || runtime.next != "" {
			return false, nil
		}
	}
	release, err := s.indexWriteGate(view.GetActiveIndexId()).lock(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	if err := extender.ExtendColumns(ctx, view.GetActiveIndexId(), current, next); err != nil {
		return false, fmt.Errorf("extend active factor result View columns: %w", err)
	}
	s.nextIndexRevision(view.GetActiveIndexId())
	rsp, err := opts.Metadata.CommitViewSchemaExtension(ctx, &pb.CommitViewSchemaExtensionReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), ActiveIndexId: view.GetActiveIndexId(),
		ExpectedActiveRevision: view.GetActiveViewRevision(), ExpectedDesiredRevision: view.GetDesiredViewRevision(),
		ExpectedActiveSchemaHash: view.GetActiveViewSchemaHash(), Columns: desiredColumns, ViewSchemaHash: next.SchemaHash,
	})
	if err != nil {
		return false, fmt.Errorf("commit active factor result View schema: %w", err)
	}
	if rsp == nil {
		return false, errors.New("Metadata returned an empty active View schema extension response")
	}
	if err := requireSuccess(rsp.GetRetInfo()); err != nil {
		return false, fmt.Errorf("commit active factor result View schema: %w", err)
	}
	committed := rsp.GetView()
	if committed == nil || committed.GetActiveIndexId() != view.GetActiveIndexId() ||
		committed.GetActiveViewRevision() != next.ViewVersion || committed.GetActiveViewSchemaHash() != next.SchemaHash ||
		!proto.Equal(&pb.View{ActiveColumns: committed.GetActiveColumns()}, &pb.View{ActiveColumns: desiredColumns}) {
		return false, errors.New("Metadata returned a mismatched active View schema extension contract")
	}
	stats, _, err := maintainIndexStats(ctx, engine, view.GetActiveIndexId())
	if err != nil {
		return false, fmt.Errorf("verify extended active View index: %w", err)
	}
	if err := validatePhysicalViewContract(committed, stats); err != nil {
		return false, err
	}
	if runtime == nil {
		s.mu.Lock()
		runtime = s.views[viewKey]
		if runtime == nil {
			runtime = newViewRuntime()
			s.views[viewKey] = runtime
		}
		s.mu.Unlock()
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		if runtime.active != "" && runtime.active != committed.GetActiveIndexId() || runtime.next != "" {
			return false, errors.New("active View runtime changed during schema extension recovery")
		}
	}
	activePrimary := strings.TrimSpace(committed.GetAttributes()[activePrimaryDatasetAttr])
	if activePrimary == "" {
		activePrimary = committed.GetDatasetId()
	}
	s.attachActiveViewLocked(committed, runtime, next, desiredColumns, activePrimary, "duckdb")
	runtime.statsIndexID = view.GetActiveIndexId()
	runtime.stats = stats
	runtime.publishReadStateLocked()
	return true, nil
}

func factorResultSchemaExtensionTarget(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View, engine viewindex.Engine) (viewindex.ViewIndexSchema, viewindex.ViewIndexSchema, viewindex.SchemaExtender, bool, error) {
	if view == nil || engine == nil || view.GetIndexBuild() != nil ||
		view.GetActiveIndexId() == "" || view.GetActiveViewRevision() == 0 ||
		view.GetDesiredViewRevision() <= view.GetActiveViewRevision() ||
		!strings.EqualFold(strings.TrimSpace(view.GetAttributes()["primary_dataset_role"]), "factor_result") ||
		!strings.EqualFold(strings.TrimSpace(view.GetEngine()), "duckdb") || engine.Engine() != "duckdb" {
		return viewindex.ViewIndexSchema{}, viewindex.ViewIndexSchema{}, nil, false, nil
	}
	extender, ok := engine.(viewindex.SchemaExtender)
	if !ok {
		return viewindex.ViewIndexSchema{}, viewindex.ViewIndexSchema{}, nil, false, nil
	}
	desiredColumns := view.GetColumns()
	if len(desiredColumns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] != "true" {
		var err error
		desiredColumns, err = loadDefaultViewColumns(ctx, opts.Metadata, auth, view)
		if err != nil {
			return viewindex.ViewIndexSchema{}, viewindex.ViewIndexSchema{}, nil, false, err
		}
	}
	activeColumns := view.GetActiveColumns()
	if !viewindex.IsAppendOnlyViewColumns(activeColumns, desiredColumns) {
		return viewindex.ViewIndexSchema{}, viewindex.ViewIndexSchema{}, nil, false, nil
	}
	primaryDatasetID := strings.TrimSpace(view.GetAttributes()[activePrimaryDatasetAttr])
	if primaryDatasetID == "" {
		primaryDatasetID = view.GetDatasetId()
	}
	current := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), PrimaryDatasetID: primaryDatasetID,
		ViewVersion: view.GetActiveViewRevision(), Engine: "duckdb", Columns: activeColumns,
		SchemaHash: view.GetActiveViewSchemaHash(),
	}
	next := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), PrimaryDatasetID: primaryDatasetID,
		ViewVersion: view.GetDesiredViewRevision(), Engine: "duckdb", Columns: desiredColumns,
	}
	next.SchemaHash = viewindex.HashViewIndexSchema(next)
	if current.SchemaHash == "" || viewindex.HashViewIndexSchema(current) != current.SchemaHash {
		return viewindex.ViewIndexSchema{}, viewindex.ViewIndexSchema{}, nil, false, nil
	}
	return current, next, extender, true, nil
}
