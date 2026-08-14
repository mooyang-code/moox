package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

// Primary dataset changes are intentionally rejected by this simple A/B
// implementation. A backfill can enumerate the old active index, but it has
// no range-scan API to discover historical rows that exist only in the new
// primary dataset. Activating such a build would silently publish an
// incomplete View, so operators must create a new View for that migration.
var errPrimaryDatasetChangeUnsupported = errors.New("changing primary dataset requires a new View")

type MetadataClient interface {
	ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error)
	GetDataset(context.Context, *pb.GetDatasetReq, ...client.Option) (*pb.GetDatasetRsp, error)
	ListDatasetColumns(context.Context, *pb.ListDatasetColumnsReq, ...client.Option) (*pb.ListDatasetColumnsRsp, error)
	ClaimViewIndexBuild(context.Context, *pb.ClaimViewIndexBuildReq, ...client.Option) (*pb.ClaimViewIndexBuildRsp, error)
	UpdateViewIndexBuild(context.Context, *pb.UpdateViewIndexBuildReq, ...client.Option) (*pb.UpdateViewIndexBuildRsp, error)
	ActivateViewIndex(context.Context, *pb.ActivateViewIndexReq, ...client.Option) (*pb.ActivateViewIndexRsp, error)
	FailViewIndexBuild(context.Context, *pb.FailViewIndexBuildReq, ...client.Option) (*pb.FailViewIndexBuildRsp, error)
}

type rebuildLogMetadataClient interface {
	ListViewRebuildLogs(context.Context, *pb.ListViewRebuildLogsReq, ...client.Option) (*pb.ListViewRebuildLogsRsp, error)
	CreateViewRebuildLog(context.Context, *pb.CreateViewRebuildLogReq, ...client.Option) (*pb.CreateViewRebuildLogRsp, error)
	UpdateViewRebuildLog(context.Context, *pb.UpdateViewRebuildLogReq, ...client.Option) (*pb.UpdateViewRebuildLogRsp, error)
	UpsertSkippedViewRebuildLog(context.Context, *pb.UpsertSkippedViewRebuildLogReq, ...client.Option) (*pb.UpsertSkippedViewRebuildLogRsp, error)
}

type FieldReader interface {
	ReadFields(context.Context, *pb.PrimaryReadFieldsReq, ...client.Option) (*pb.PrimaryReadFieldsRsp, error)
}

type ReconcilerOptions struct {
	Metadata MetadataClient
	Primary  FieldReader
	Interval time.Duration
	OwnerID  string
	Grace    time.Duration
	// MaxViewFileBytes triggers an A/B rebuild for a
	// finite-retention View. The rebuilt index contains only its keep window;
	// SwitchView then removes the old DuckDB file after the grace period.
	MaxViewFileBytes int64
	// RebuildMaxPending and RebuildIdleChecks gate optional size-limit
	// rebuilds. Necessary repairs bypass this capacity gate.
	RebuildMaxPending           uint64
	RebuildIdleChecks           uint32
	RebuildMaxPendingConfigured bool
	RebuildIdleChecksConfigured bool
}

const viewBackfillBatchSize = 10000
const sizeLimitRebuildRetryInterval = 30 * time.Minute
const defaultRebuildMaxPending uint64 = 32
const defaultRebuildIdleChecks uint32 = 3
const sizeLimitBuildBacklogThreshold = defaultRebuildMaxPending

// viewColumnsExplicitAttr preserves the distinction between a view that
// intentionally exposes no columns and a legacy view whose empty definition
// means "all primary dataset columns".
const viewColumnsExplicitAttr = "moox.columns_explicit"

func (s *Service) StartReconciler(ctx context.Context, opts ReconcilerOptions) (func(), error) {
	var err error
	opts, err = s.normalizeReconcilerOptions(opts)
	if err != nil {
		return nil, err
	}
	if err := s.reconcileOnce(ctx, opts); err != nil {
		return nil, err
	}
	s.setReconcileReady(true)
	return s.startReconcilerLoop(ctx, opts), nil
}

// StartReconcilerAsync starts the periodic check without making process
// startup wait for a potentially large historical backfill. Until the first
// successful pass completes, event handlers keep deliveries pending.
func (s *Service) StartReconcilerAsync(ctx context.Context, opts ReconcilerOptions) (func(), error) {
	var err error
	opts, err = s.normalizeReconcilerOptions(opts)
	if err != nil {
		return nil, err
	}
	s.setReconcileReady(false)
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		firstPass := true
		run := func() {
			if err := s.reconcileOnce(loopCtx, opts); err != nil {
				log.Printf("storage view reconcile failed: %v", err)
				return
			}
			if firstPass {
				s.setReconcileReady(true)
				firstPass = false
			}
		}
		run()
		ticker := time.NewTicker(opts.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

func (s *Service) normalizeReconcilerOptions(opts ReconcilerOptions) (ReconcilerOptions, error) {
	if opts.Metadata == nil {
		return opts, errors.New("metadata client is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.RebuildMaxPending == 0 && !opts.RebuildMaxPendingConfigured {
		opts.RebuildMaxPending = defaultRebuildMaxPending
	}
	if opts.RebuildIdleChecks == 0 && !opts.RebuildIdleChecksConfigured {
		opts.RebuildIdleChecks = defaultRebuildIdleChecks
	}
	if periodMetadata, ok := opts.Metadata.(PeriodMetadataClient); ok {
		s.mu.Lock()
		s.periodMetadata = periodMetadata
		s.mu.Unlock()
	}
	s.setMetadataClient(opts.Metadata)
	if strings.TrimSpace(opts.OwnerID) == "" {
		opts.OwnerID = "storage-view"
	}
	return opts, nil
}

func (s *Service) startReconcilerLoop(ctx context.Context, opts ReconcilerOptions) func() {
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(opts.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				if err := s.reconcileOnce(loopCtx, opts); err != nil {
					log.Printf("storage view reconcile failed: %v", err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

// RestoreActiveViews restores only the indexes already declared active by
// Metadata. It never claims or backfills a new index, so callers can run it
// before starting the EventBus consumer without holding up the live stream.
func (s *Service) RestoreActiveViews(ctx context.Context, opts ReconcilerOptions) error {
	if opts.Metadata == nil {
		return errors.New("metadata client is required")
	}
	if strings.TrimSpace(opts.OwnerID) == "" {
		opts.OwnerID = "storage-view"
	}
	if periodMetadata, ok := opts.Metadata.(PeriodMetadataClient); ok {
		s.mu.Lock()
		s.periodMetadata = periodMetadata
		s.mu.Unlock()
	}
	s.setMetadataClient(opts.Metadata)
	auth := s.internalAuth()
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := opts.Metadata.ListViews(ctx, &pb.ListViewsReq{AuthInfo: auth, Status: "active", Page: &pb.Page{Page: pageNo, Size: 100}})
		if err != nil {
			return err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return err
		}
		for _, view := range rsp.GetViews() {
			if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" {
				continue
			}
			if activeID := view.GetActiveIndexId(); activeID != "" {
				engineName := strings.ToLower(strings.TrimSpace(view.GetEngine()))
				engine := s.engines[engineName]
				if engine == nil {
					return fmt.Errorf("view engine %q is unavailable", engineName)
				}
				stats, err := restoreIndexStats(ctx, engine, activeID)
				if err != nil {
					return err
				}
				if !stats.Exists {
					err := fmt.Errorf("active view index %q is missing; refusing to start without a readable active view", activeID)
					s.recordFailedRebuild(ctx, opts, auth, view, pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_ACTIVE_MISSING, err, stats)
					return err
				}
				if err := validatePhysicalViewContract(view, stats); err != nil {
					s.recordFailedRebuild(ctx, opts, auth, view, pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_ACTIVE_INVALID, err, stats)
					return err
				}
				if err := s.AttachActiveViewWithGrace(ctx, view, opts.Grace); err != nil {
					return err
				}
			}
			build := view.GetIndexBuild()
			if build == nil || build.GetIndexId() == "" {
				continue
			}
			if build.GetIndexId() == view.GetActiveIndexId() {
				// Metadata activation is authoritative. A stale READY build
				// query must never delete the active physical index.
				continue
			}
			if build.GetState() == pb.ViewIndexBuild_READY {
				// Attach a READY first-build index before the consumer starts so
				// rows can be routed while the initial reconcile is pending.
				if view.GetActiveIndexId() == "" {
					if err := s.AttachPendingViewBuild(ctx, view); err != nil {
						return err
					}
					if err := s.TrackViewBuild(ctx, view.GetSpaceId(), view.GetViewId(), build.GetBuildId(), opts.OwnerID, opts.Metadata, auth); err != nil {
						return err
					}
					continue
				}
			}
			// An unfinished build is not resumable. A READY build with an
			// existing active index is also stale after restart; the active
			// index remains authoritative and the next build will be recreated.
			if build.GetState() == pb.ViewIndexBuild_PREPARING || build.GetState() == pb.ViewIndexBuild_BUILDING || build.GetState() == pb.ViewIndexBuild_CATCHING_UP || (build.GetState() == pb.ViewIndexBuild_READY && view.GetActiveIndexId() != "") {
				if err := s.failInterruptedBuild(ctx, opts, auth, view, build); err != nil {
					return err
				}
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			return nil
		}
	}
}

func restoreIndexStats(ctx context.Context, engine viewindex.Engine, indexID string) (viewindex.ViewIndexStats, error) {
	if reader, ok := engine.(viewindex.MetadataStatReader); ok {
		return reader.StatMetadata(ctx, indexID)
	}
	return engine.Stat(ctx, indexID)
}

func (s *Service) reconcileOnce(ctx context.Context, opts ReconcilerOptions) error {
	auth := s.internalAuth()
	// Audit persistence is best-effort and must never hold up View state
	// transitions. Retry terminal log updates before processing the next pass.
	s.drainRebuildLogRetries(ctx)
	var firstErr error
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := opts.Metadata.ListViews(ctx, &pb.ListViewsReq{AuthInfo: auth, Status: "active", Page: &pb.Page{Page: pageNo, Size: 100}})
		if err != nil {
			return err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return err
		}
		for _, view := range rsp.GetViews() {
			if err := s.reconcileView(ctx, opts, auth, view); err != nil {
				if errors.Is(err, errActiveContractUnavailable) {
					// Do not let the EventBus consumer ACK a legacy in-flight
					// rebuild whose active contract cannot be recovered. The
					// caller must repair/finish the View rebuild before startup.
					return err
				}
				if view != nil {
					log.Printf("storage view reconcile %s/%s failed: %v", view.GetSpaceId(), view.GetViewId(), err)
				}
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			return firstErr
		}
	}
}

func (s *Service) reconcileView(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" {
		return nil
	}
	var stats viewindex.ViewIndexStats
	var activeInvalidErr error
	if view.GetActiveIndexId() != "" {
		engineName := strings.ToLower(strings.TrimSpace(view.GetEngine()))
		engine := s.engines[engineName]
		if engine == nil {
			return fmt.Errorf("view engine %q is unavailable", engineName)
		}
		var err error
		stats, err = engine.Stat(ctx, view.GetActiveIndexId())
		if err != nil {
			return err
		}
		if stats.Exists {
			if err := validatePhysicalViewContract(view, stats); err != nil {
				// Keep the safety behavior (do not attach a stale physical
				// index), but classify it as a necessary repair. If this is a
				// live runtime we can build a replacement from the still-readable
				// index; on startup RestoreActiveViews will fail closed and leave
				// the actionable failed history row below.
				activeInvalidErr = err
			} else {
				if err := s.AttachActiveViewWithGrace(ctx, view, opts.Grace); err != nil {
					return err
				}
				_, runtime := s.activeIndex(view.GetSpaceId(), view.GetViewId())
				s.cacheActiveIndexStats(runtime, view.GetActiveIndexId(), stats)
				if runtime != nil {
					runtime.mu.Lock()
					primaryChanged := runtime.active == view.GetActiveIndexId() && runtime.activePrimaryDatasetID != "" && view.GetPrimaryDatasetId() != "" && runtime.activePrimaryDatasetID != view.GetPrimaryDatasetId()
					runtime.mu.Unlock()
					if primaryChanged {
						return errPrimaryDatasetChangeUnsupported
					}
				}
			}
		}
	}
	var failedBuild *pb.ViewIndexBuild
	if build := view.GetIndexBuild(); build != nil {
		switch build.GetState() {
		case pb.ViewIndexBuild_FAILED:
			failedBuild = proto.Clone(build).(*pb.ViewIndexBuild)
			cause := errors.New(build.GetError())
			if build.GetError() == "" {
				cause = errors.New("view rebuild failed")
			}
			found, updated, lookupOK := s.finishRunningRebuildLog(ctx, opts, auth, view, build.GetBuildId(), pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, build.GetEntriesWritten(), cause)
			if !lookupOK || (found && !updated) {
				log.Printf("storage view rebuild failure history will be retried for %s/%s/%s", view.GetSpaceId(), view.GetViewId(), build.GetBuildId())
				if !lookupOK {
					s.attachRebuildLogFallback(view, build.GetBuildId(), pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, interruptedRebuildFailureItem(view, build, cause))
				}
			}
			if lookupOK && !found {
				// A failure can be persisted after the original RUNNING log RPC
				// response was lost. Keep an auditable terminal record instead of
				// leaving the old history row permanently in progress.
				s.recordInterruptedRebuildFailure(ctx, opts, auth, view, build, cause)
			}
			s.discardFailedBuild(ctx, view.GetSpaceId(), view.GetViewId(), build.GetIndexId(), build.GetEngine())
			view.IndexBuild = nil
		case pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP:
			// There is deliberately no resumable build cursor. A process exit
			// discards the partial inactive index and the next reconcile starts
			// a complete rebuild.
			if err := s.failInterruptedBuild(ctx, opts, auth, view, build); err != nil {
				return err
			}
			return nil
		case pb.ViewIndexBuild_READY:
			if build.GetTargetViewVersion() != view.GetDesiredViewRevision() {
				staleErr := fmt.Errorf("view build revision %d is stale; desired revision is %d", build.GetTargetViewVersion(), view.GetDesiredViewRevision())
				s.failBuild(ctx, opts, auth, view, build.GetBuildId(), build.GetIndexId(), staleErr)
				return nil
			}
			if err := s.AttachPendingViewBuild(ctx, view); err != nil {
				s.failBuild(ctx, opts, auth, view, build.GetBuildId(), build.GetIndexId(), err)
				return err
			}
			err := s.activateViewBuild(ctx, opts, auth, view, build.GetBuildId(), build.GetIndexId(), build.GetEngine(), build.GetTargetViewVersion(), build.GetSchemaHash(), build.GetColumns())
			if err == nil {
				s.finishRunningRebuildLog(ctx, opts, auth, view, build.GetBuildId(), pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SUCCEEDED, build.GetEntriesWritten(), nil)
			}
			return err
		}
	}
	rebuildNeeded := needsSizeLimitRebuild(view, stats, opts)
	if activeInvalidErr != nil {
		rebuildNeeded = true
	}
	sizeLimitOnly := activeInvalidErr == nil && needsSizeLimitWatermark(view, stats, opts)
	now := time.Now().UTC()
	triggerReason := rebuildTriggerReason(view, stats, sizeLimitOnly)
	if activeInvalidErr != nil {
		triggerReason = pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_ACTIVE_INVALID
	}
	if activeInvalidErr != nil {
		// A stale/corrupt active file must never be exposed. If there is no
		// already-attached runtime from which to backfill, fail closed and leave
		// an audit row rather than activating an empty replacement.
		s.mu.RLock()
		runtime := s.views[viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}]
		s.mu.RUnlock()
		if runtime == nil {
			s.recordFailedRebuild(ctx, opts, auth, view, triggerReason, activeInvalidErr, stats)
			return activeInvalidErr
		}
	}
	maxPending := opts.RebuildMaxPending
	if maxPending == 0 && !opts.RebuildMaxPendingConfigured {
		maxPending = defaultRebuildMaxPending
	}
	idleChecks := opts.RebuildIdleChecks
	if idleChecks == 0 && !opts.RebuildIdleChecksConfigured {
		idleChecks = defaultRebuildIdleChecks
	}
	var pending, ackPending uint64
	gateBlockReason := "consumer_not_idle"
	if sizeLimitOnly {
		var backlogErr error
		pending, ackPending, backlogErr = s.consumerBacklog(ctx)
		if backlogErr != nil {
			gateBlockReason = "consumer_unavailable"
		} else if pending+ackPending > maxPending {
			gateBlockReason = "consumer_backlog"
		} else {
			gateBlockReason = "consumer_idle_checks"
		}
	}
	if sizeLimitOnly && !s.sizeLimitBuildAllowed(view.GetSpaceId(), view.GetViewId(), now) {
		s.recordSkippedRebuild(ctx, opts, auth, view, triggerReason, "cooldown", stats, stats.PhysicalBytes, pending, ackPending)
		return nil
	}
	if sizeLimitOnly && !s.sizeLimitBuildIdleFor(ctx, viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}, maxPending, idleChecks) {
		// A byte-watermark rebuild is optional while the durable consumer is
		// catching up. Backfill competes with live A/B writes for the same
		// DuckDB memory and I/O; starting it under backlog can turn one large
		// system View into cross-Dataset head-of-line blocking.
		s.recordSkippedRebuild(ctx, opts, auth, view, triggerReason, gateBlockReason, stats, stats.PhysicalBytes, pending, ackPending)
		return nil
	}
	if sizeLimitOnly && !s.tryAcquireRebuild() {
		s.recordSkippedRebuild(ctx, opts, auth, view, triggerReason, "another_rebuild_running", stats, stats.PhysicalBytes, pending, ackPending)
		return nil
	}
	if sizeLimitOnly {
		s.resetIdleChecks(viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()})
	}
	if sizeLimitOnly {
		defer s.releaseRebuild()
	}
	// A failed size-limit rebuild must not be retried forever when the
	// watermark is no longer exceeded. This is especially important for a
	// large DuckDB view on a memory-constrained host: the failed inactive
	// index is removed above, while the healthy active index remains usable.
	if !shouldRetryFailedBuildWithCause(view, failedBuild, rebuildNeeded, sizeLimitOnly, now) {
		return nil
	}
	if sizeLimitOnly {
		s.markSizeLimitBuild(view.GetSpaceId(), view.GetViewId(), now)
	}
	columns := view.GetColumns()
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] != "true" {
		var err error
		columns, err = loadDefaultViewColumns(ctx, opts.Metadata, auth, view)
		if err != nil {
			return err
		}
	}
	schema := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: view.GetDesiredViewRevision(),
		PrimaryDatasetID: view.GetPrimaryDatasetId(), Engine: strings.ToLower(view.GetEngine()), Columns: columns,
	}
	schema.SchemaHash = viewindex.HashViewIndexSchema(schema)
	indexID := viewindex.InactiveViewIndexID(view.GetSpaceId(), view.GetViewId(), view.GetActiveIndexId())
	if s.isIndexRetiring(indexID) {
		// Keep the old active index readable until its grace period has elapsed.
		// Reusing the slot would close a reader that already captured the old ID.
		if sizeLimitOnly {
			s.recordSkippedRebuild(ctx, opts, auth, view, triggerReason, "inactive_slot_retiring", stats, stats.PhysicalBytes, pending, ackPending)
		}
		return nil
	}
	buildID := "build-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	claim, err := opts.Metadata.ClaimViewIndexBuild(ctx, &pb.ClaimViewIndexBuildReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID,
		IndexId: indexID, Engine: schema.Engine, TargetViewVersion: schema.ViewVersion,
		OwnerId: opts.OwnerID, SchemaHash: schema.SchemaHash, Columns: columns,
		ExpectedActiveIndexId: view.GetActiveIndexId(),
	})
	if err != nil {
		return err
	}
	if err := requireSuccess(claim.GetRetInfo()); err != nil {
		return err
	}
	buildLog := s.createRunningRebuildLog(ctx, opts, auth, view, buildID, indexID, triggerReason, stats, stats.PhysicalBytes, pending, ackPending)
	finishLog := func(result pb.ViewRebuildResult, entries uint64, cause error) {
		if buildLog != nil {
			s.finishRebuildLog(ctx, opts, auth, buildLog, result, entries, cause)
			return
		}
		found, _, lookupOK := s.finishRunningRebuildLog(ctx, opts, auth, view, buildID, result, entries, cause)
		if lookupOK && !found {
			fallback := &pb.ViewRebuildLog{
				SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID, IndexId: indexID,
				TriggerReason: triggerReason, Result: result, TargetViewRevision: view.GetDesiredViewRevision(),
				ActiveViewRevision: view.GetActiveViewRevision(), EntriesWritten: entries,
				FinishedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorSummary: rebuildErrorSummary(cause),
				DetailsJson: `{"phase":"reconcile_fallback"}`,
			}
			if !s.createRebuildLogFallback(ctx, opts, auth, fallback) {
				s.queueRebuildLogRetry(pendingRebuildLog{opts: opts, auth: auth, view: proto.Clone(view).(*pb.View), buildID: buildID, result: result, entries: entries, cause: cause, fallback: fallback})
			}
		} else if !lookupOK && result == pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED {
			s.attachRebuildLogFallback(view, buildID, result, &pb.ViewRebuildLog{
				SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID, IndexId: indexID,
				TriggerReason: triggerReason, Result: result, TargetViewRevision: view.GetDesiredViewRevision(),
				ActiveViewRevision: view.GetActiveViewRevision(), EntriesWritten: entries,
				FinishedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorSummary: rebuildErrorSummary(cause), DetailsJson: `{"phase":"reconcile_fallback"}`,
			})
		}
	}
	prepared, err := s.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{
		AuthInfo: auth, IndexId: indexID, Engine: schema.Engine,
		Schema: &pb.ViewIndexSchema{
			SpaceId: schema.SpaceID, ViewId: schema.ViewID, ViewVersion: schema.ViewVersion,
			Engine: schema.Engine, Columns: schema.Columns, ViewSchemaHash: schema.SchemaHash, PrimaryDatasetId: schema.PrimaryDatasetID, DatasetIds: append([]string(nil), view.GetDatasetIds()...),
		},
	})
	if err != nil || prepared.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		prepareErr := err
		if prepareErr == nil {
			prepareErr = requireSuccess(prepared.GetRetInfo())
		}
		s.failBuild(ctx, opts, auth, view, buildID, indexID, fmt.Errorf("prepare view index: %w", prepareErr))
		finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, 0, prepareErr)
		return prepareErr
	}
	if err := s.TrackViewBuild(ctx, view.GetSpaceId(), view.GetViewId(), buildID, opts.OwnerID, opts.Metadata, auth); err != nil {
		finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, 0, err)
		return err
	}
	if err := s.updateBuild(ctx, opts, auth, view, buildID, pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING, 0); err != nil {
		s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
		finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, 0, err)
		return err
	}
	if view.GetActiveIndexId() != "" && !stats.Exists {
		// DataNode intentionally has no range scan API. Once the old physical
		// index is gone, a new empty B cannot reconstruct already-ACKed history;
		// refuse to activate it rather than replacing a durable view with a
		// silently incomplete index.
		err := fmt.Errorf("active view index %q is missing and cannot be rebuilt without a range reader", view.GetActiveIndexId())
		s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
		finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, 0, err)
		return err
	}
	if view.GetActiveIndexId() != "" && stats.Exists {
		buildCtx := ctx
		s.mu.RLock()
		runtime := s.views[viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}]
		s.mu.RUnlock()
		if runtime != nil {
			runtime.mu.Lock()
			if runtime.buildContext != nil {
				buildCtx = runtime.buildContext
			}
			runtime.mu.Unlock()
		}
		if err := s.BackfillViewWithReader(buildCtx, view.GetSpaceId(), view.GetViewId(), viewBackfillBatchSize, opts.Primary); err != nil {
			s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
			finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, 0, err)
			return err
		}
	} else {
		// A first build starts empty and is filled by the subscribed change stream.
		// Do not wait for the durable consumer here: rows delivered before B is
		// active are intentionally NAKed by applyDatasetEvent and will be retried
		// after the pointer switch. Waiting for NumPending/NumAckPending would
		// deadlock this first activation because the consumer cannot ACK until B
		// becomes authoritative.
	}
	err = s.catchUpAndActivateViewBuild(ctx, opts, auth, view, buildID, indexID, schema.Engine, schema.ViewVersion, schema.SchemaHash, columns, uint64(stats.EntryCount))
	if err != nil {
		var activation activationRetry
		if errors.As(err, &activation) {
			// Activation retries are still part of the same running build. Do
			// not turn a transient metadata/cache response into a failed log.
			return err
		}
		s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
		finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, uint64(stats.EntryCount), err)
	} else {
		finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SUCCEEDED, uint64(stats.EntryCount), nil)
	}
	return err
}

func (s *Service) sizeLimitBuildIdle(ctx context.Context) bool {
	return s.sizeLimitBuildIdleFor(ctx, viewRef{}, sizeLimitBuildBacklogThreshold, 1)
}

func (s *Service) sizeLimitBuildIdleFor(ctx context.Context, ref viewRef, maxPending uint64, requiredChecks uint32) bool {
	pending, ackPending, err := s.consumerBacklog(ctx)
	if err != nil {
		// Fail closed for optional capacity work. Schema/contract repairs do not
		// use this gate and continue to reconcile normally.
		s.resetIdleChecks(ref)
		return false
	}
	if pending+ackPending > maxPending {
		s.resetIdleChecks(ref)
		return false
	}
	s.rebuildMu.Lock()
	if s.idleChecks == nil {
		s.idleChecks = make(map[viewRef]uint32)
	}
	s.idleChecks[ref]++
	checks := s.idleChecks[ref]
	s.rebuildMu.Unlock()
	return checks >= requiredChecks
}

func (s *Service) consumerBacklog(ctx context.Context) (uint64, uint64, error) {
	s.mu.RLock()
	stateReader := s.consumerState
	boundReader := s.consumerBound
	s.mu.RUnlock()
	if stateReader == nil {
		return 0, 0, errors.New("storage view consumer is not bound")
	}
	if boundReader != nil && !boundReader() {
		return 0, 0, errors.New("storage view consumer subscription is not bound")
	}
	stateCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	state, err := stateReader(stateCtx)
	if err != nil {
		return 0, 0, err
	}
	return state.NumPending, uint64(state.NumAckPending), nil
}

func (s *Service) resetIdleChecks(ref viewRef) {
	s.rebuildMu.Lock()
	if s.idleChecks != nil {
		delete(s.idleChecks, ref)
	}
	s.rebuildMu.Unlock()
}

func (s *Service) tryAcquireRebuild() bool {
	s.rebuildMu.Lock()
	defer s.rebuildMu.Unlock()
	if s.rebuildRunning {
		return false
	}
	s.rebuildRunning = true
	return true
}

func (s *Service) releaseRebuild() {
	s.rebuildMu.Lock()
	s.rebuildRunning = false
	s.rebuildMu.Unlock()
}

func (s *Service) sizeLimitBuildAllowed(spaceID, viewID string, now time.Time) bool {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return true
	}
	runtime.mu.Lock()
	last := runtime.lastSizeLimitBuildAt
	runtime.mu.Unlock()
	return last.IsZero() || !now.Before(last.Add(sizeLimitRebuildRetryInterval))
}

func (s *Service) markSizeLimitBuild(spaceID, viewID string, now time.Time) {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	runtime.lastSizeLimitBuildAt = now
	runtime.mu.Unlock()
}

func shouldRetryFailedBuild(view *pb.View, failedBuild *pb.ViewIndexBuild, sizeLimitExceeded bool, now time.Time) bool {
	return shouldRetryFailedBuildWithCause(view, failedBuild, sizeLimitExceeded, sizeLimitExceeded, now)
}

func shouldRetryFailedBuildWithCause(view *pb.View, failedBuild *pb.ViewIndexBuild, rebuildNeeded, sizeLimitOnly bool, now time.Time) bool {
	if failedBuild == nil {
		return rebuildNeeded
	}
	if view != nil && view.GetDesiredViewRevision() != view.GetActiveViewRevision() {
		return true
	}
	if !rebuildNeeded {
		return false
	}
	// Missing active files and coverage/revision repairs must be retried on the
	// next reconciliation. The cooldown is only a guard for repeated physical
	// watermark rebuilds, which are intentionally optional while A remains
	// healthy.
	if !sizeLimitOnly {
		return true
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, failedBuild.GetUpdatedAt())
	if err != nil || updatedAt.IsZero() {
		return true
	}
	return !now.Before(updatedAt.Add(sizeLimitRebuildRetryInterval))
}

type resumeBuildRetry struct{ cause error }

func (e resumeBuildRetry) Error() string { return e.cause.Error() }
func (e resumeBuildRetry) Unwrap() error { return e.cause }

type activationRetry struct{ cause error }

func (e activationRetry) Error() string { return e.cause.Error() }
func (e activationRetry) Unwrap() error { return e.cause }

func (s *Service) catchUpAndActivateViewBuild(
	ctx context.Context,
	opts ReconcilerOptions,
	auth *pb.AuthInfo,
	view *pb.View,
	buildID, indexID, engine string,
	revision uint64,
	schemaHash string,
	columns []*pb.ViewColumn,
	entries uint64,
) error {
	if err := s.updateBuild(ctx, opts, auth, view, buildID, pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP, entries); err != nil {
		return err
	}
	if err := s.updateBuild(ctx, opts, auth, view, buildID, pb.ViewIndexBuild_CATCHING_UP, pb.ViewIndexBuild_READY, entries); err != nil {
		return err
	}
	if err := s.MarkViewBuildReady(view.GetSpaceId(), view.GetViewId()); err != nil {
		return err
	}
	return s.activateViewBuild(ctx, opts, auth, view, buildID, indexID, engine, revision, schemaHash, columns)
}

func (s *Service) resumeViewBuild(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, previous *pb.ViewIndexBuild) error {
	claim, err := opts.Metadata.ClaimViewIndexBuild(ctx, &pb.ClaimViewIndexBuildReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: previous.GetBuildId(),
		IndexId: previous.GetIndexId(), Engine: previous.GetEngine(), TargetViewVersion: previous.GetTargetViewVersion(),
		OwnerId: opts.OwnerID, SchemaHash: previous.GetSchemaHash(), Columns: previous.GetColumns(),
		ExpectedActiveIndexId: view.GetActiveIndexId(),
	})
	if err != nil {
		return resumeBuildRetry{cause: err}
	}
	if err := requireSuccess(claim.GetRetInfo()); err != nil {
		return resumeBuildRetry{cause: err}
	}
	build := claim.GetBuild()
	if build == nil {
		return errors.New("resumed view build metadata is missing")
	}
	if err := s.AttachPendingViewBuild(ctx, view); err != nil {
		return err
	}
	if err := s.TrackViewBuild(ctx, view.GetSpaceId(), view.GetViewId(), build.GetBuildId(), opts.OwnerID, opts.Metadata, auth); err != nil {
		return err
	}
	if err := s.updateBuild(ctx, opts, auth, view, build.GetBuildId(), pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING, build.GetEntriesWritten()); err != nil {
		return err
	}
	activePhysicalExists := view.GetActiveIndexId() != ""
	if activePhysicalExists {
		activeEngine, err := s.engineFor(view.GetActiveIndexId())
		if err != nil {
			return err
		}
		activeStats, err := activeEngine.Stat(ctx, view.GetActiveIndexId())
		if err != nil {
			return err
		}
		activePhysicalExists = activeStats.Exists
	}
	if !activePhysicalExists {
		// A recovered build without a physical active index also resumes from
		// the change stream; historical enumeration is intentionally absent.
		if err := s.updateBuild(ctx, opts, auth, view, build.GetBuildId(), pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_READY, build.GetEntriesWritten()); err != nil {
			return err
		}
	} else {
		if err := s.updateBuild(ctx, opts, auth, view, build.GetBuildId(), pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP, build.GetEntriesWritten()); err != nil {
			return err
		}
		buildCtx := ctx
		s.mu.RLock()
		runtime := s.views[viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}]
		s.mu.RUnlock()
		if runtime != nil {
			runtime.mu.Lock()
			if runtime.buildContext != nil {
				buildCtx = runtime.buildContext
			}
			runtime.mu.Unlock()
		}
		if err := s.BackfillViewWithReader(buildCtx, view.GetSpaceId(), view.GetViewId(), viewBackfillBatchSize, opts.Primary); err != nil {
			return err
		}
		if err := s.updateBuild(ctx, opts, auth, view, build.GetBuildId(), pb.ViewIndexBuild_CATCHING_UP, pb.ViewIndexBuild_READY, build.GetEntriesWritten()); err != nil {
			return err
		}
	}
	return s.activateViewBuild(ctx, opts, auth, view, build.GetBuildId(), build.GetIndexId(), build.GetEngine(), build.GetTargetViewVersion(), build.GetSchemaHash(), build.GetColumns())
}

// activateViewBuild makes the metadata commit and the in-memory pointer switch
// one short View operation. A live writer cannot fail/remove the replacement
// between those two transitions. Any non-success response is read back before
// deciding whether the build is still pending; an ambiguous response must not
// discard an index that metadata may already have made active.
func (s *Service) activateViewBuild(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, buildID, indexID, engineName string, revision uint64, schemaHash string, columns []*pb.ViewColumn) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}]
	s.mu.RUnlock()
	if runtime == nil {
		return activationRetry{cause: errors.New("view runtime is not prepared")}
	}
	runtime.mu.Lock()
	if runtime.next != indexID || runtime.buildFailed {
		runtime.mu.Unlock()
		return activationRetry{cause: errViewBuildFailed}
	}
	runtime.status = "ready"
	activated, callErr := opts.Metadata.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID, OwnerId: opts.OwnerID,
	})
	retErr := callErr
	if retErr == nil {
		retErr = requireSuccess(activated.GetRetInfo())
	}
	committed := activated.GetView()
	if retErr != nil {
		readback, readErr := s.readActiveView(ctx, opts.Metadata, auth, view.GetSpaceId(), view.GetViewId())
		if readErr == nil && readback != nil && readback.GetActiveIndexId() == indexID {
			committed = readback
			retErr = nil
		} else {
			if readErr != nil {
				retErr = errors.Join(retErr, readErr)
			}
			runtime.mu.Unlock()
			return activationRetry{cause: retErr}
		}
	}
	if committed == nil || committed.GetActiveIndexId() != indexID {
		readback, readErr := s.readActiveView(ctx, opts.Metadata, auth, view.GetSpaceId(), view.GetViewId())
		if readErr != nil {
			runtime.mu.Unlock()
			return activationRetry{cause: readErr}
		}
		committed = readback
	}
	if committed == nil || committed.GetActiveIndexId() != indexID {
		runtime.mu.Unlock()
		return activationRetry{cause: fmt.Errorf("metadata activation did not expose index %q", indexID)}
	}
	var oldID string
	var oldGeneration uint64
	var err error
	if runtime.active != "" {
		oldID, oldGeneration, err = s.switchViewLocked(runtime)
	} else {
		activePrimary := committed.GetPrimaryDatasetId()
		if raw := committed.GetAttributes()[activePrimaryDatasetAttr]; raw != "" {
			activePrimary = raw
		}
		s.mu.RLock()
		sch := s.schemas[indexID]
		engine := s.indexEngine[indexID]
		s.mu.RUnlock()
		if engine == "" {
			engine = engineName
		}
		s.attachActiveViewLocked(activatedViewMetadata(committed, view, indexID, engine, revision, schemaHash, columns), runtime, sch, columns, activePrimary, engine)
	}
	runtime.mu.Unlock()
	s.mu.Lock()
	s.catalogViews[viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}] = proto.Clone(committed).(*pb.View)
	s.mu.Unlock()
	if err != nil {
		return activationRetry{cause: err}
	}
	s.scheduleOldIndexRemoval(ctx, oldID, oldGeneration, opts.Grace)
	return nil
}

func (s *Service) readActiveView(ctx context.Context, metadata MetadataClient, auth *pb.AuthInfo, spaceID, viewID string) (*pb.View, error) {
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := metadata.ListViews(ctx, &pb.ListViewsReq{AuthInfo: auth, SpaceId: spaceID, Status: "active", Page: &pb.Page{Page: pageNo, Size: 100}})
		if err != nil {
			return nil, err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, candidate := range rsp.GetViews() {
			if candidate != nil && candidate.GetSpaceId() == spaceID && candidate.GetViewId() == viewID {
				return candidate, nil
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			return nil, fmt.Errorf("active view %s/%s was not found", spaceID, viewID)
		}
	}
}

// validatePhysicalViewContract prevents a same-named stale/corrupt index from
// being attached after restart. Existence alone is not enough: the metadata
// revision and schema hash are the authority for the active pointer.
func validatePhysicalViewContract(view *pb.View, stats viewindex.ViewIndexStats) error {
	if view == nil {
		return errors.New("active view metadata is required")
	}
	if expected := view.GetActiveViewRevision(); expected > 0 && stats.ViewVersion != expected {
		return fmt.Errorf("active view index contract mismatch: metadata revision=%d physical revision=%d", expected, stats.ViewVersion)
	}
	if expected := strings.TrimSpace(view.GetActiveViewSchemaHash()); expected != "" && expected != strings.TrimSpace(stats.SchemaHash) {
		return fmt.Errorf("active view index contract mismatch: metadata schema_hash=%q physical schema_hash=%q", expected, stats.SchemaHash)
	}
	return nil
}

func activatedViewMetadata(response, source *pb.View, indexID, engine string, revision uint64, schemaHash string, columns []*pb.ViewColumn) *pb.View {
	var result *pb.View
	if response != nil {
		result = proto.Clone(response).(*pb.View)
	} else if source != nil {
		result = proto.Clone(source).(*pb.View)
	} else {
		result = &pb.View{}
	}
	if result.GetActiveIndexId() == "" {
		result.ActiveIndexId = indexID
	}
	if result.GetEngine() == "" {
		result.Engine = engine
	}
	if result.GetActiveViewRevision() == 0 {
		result.ActiveViewRevision = revision
	}
	if result.GetActiveViewSchemaHash() == "" {
		result.ActiveViewSchemaHash = schemaHash
	}
	if len(result.GetActiveColumns()) == 0 && result.GetAttributes()[viewColumnsExplicitAttr] != "true" {
		result.ActiveColumns = columns
	}
	return result
}

func loadDefaultViewColumns(ctx context.Context, metadata MetadataClient, auth *pb.AuthInfo, view *pb.View) ([]*pb.ViewColumn, error) {
	var columns []*pb.ViewColumn
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := metadata.ListDatasetColumns(ctx, &pb.ListDatasetColumnsReq{
			AuthInfo: auth, SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId(),
			Page: &pb.Page{Page: pageNo, Size: 1000},
		})
		if err != nil {
			return nil, err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, column := range rsp.GetColumns() {
			if column == nil || (column.GetStatus() != "" && column.GetStatus() != "active") {
				continue
			}
			columns = append(columns, &pb.ViewColumn{
				SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), ColumnName: column.GetColumnName(),
				OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
				OriginId:   view.GetPrimaryDatasetId() + "." + column.GetColumnName(),
				ValueType:  column.GetValueType(),
			})
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetColumns()) == 0 {
			return columns, nil
		}
	}
}

func (s *Service) updateBuild(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, buildID string, from, to pb.ViewIndexBuild_State, rows uint64) error {
	rsp, err := opts.Metadata.UpdateViewIndexBuild(ctx, &pb.UpdateViewIndexBuildReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID,
		OwnerId: opts.OwnerID, ExpectedState: from, NextState: to, EntriesWritten: rows,
	})
	if err != nil {
		return err
	}
	return requireSuccess(rsp.GetRetInfo())
}

// failInterruptedBuild keeps the consumer in its pending state when a
// restart/response loss prevents us from durably discarding an inactive build.
// A later reconciliation may retry the CAS, but it must not declare the View
// ready while the same non-active build is still present in Metadata.
func (s *Service) failInterruptedBuild(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, build *pb.ViewIndexBuild) error {
	if build == nil {
		return nil
	}
	cause := errors.New("discard interrupted view build")
	if s.failBuild(ctx, opts, auth, view, build.GetBuildId(), build.GetIndexId(), cause) {
		found, updated, lookupOK := s.finishRunningRebuildLog(ctx, opts, auth, view, build.GetBuildId(), pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, build.GetEntriesWritten(), cause)
		if !lookupOK || (found && !updated) {
			log.Printf("storage view interrupted failure history will be retried for %s/%s/%s", view.GetSpaceId(), view.GetViewId(), build.GetBuildId())
			if !lookupOK {
				s.attachRebuildLogFallback(view, build.GetBuildId(), pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, interruptedRebuildFailureItem(view, build, cause))
			}
		}
		if lookupOK && !found {
			s.recordInterruptedRebuildFailure(ctx, opts, auth, view, build, cause)
		}
		return nil
	}
	current, err := s.readActiveView(ctx, opts.Metadata, auth, view.GetSpaceId(), view.GetViewId())
	if err != nil {
		return err
	}
	if current.GetActiveIndexId() == build.GetIndexId() {
		return nil
	}
	if currentBuild := current.GetIndexBuild(); currentBuild != nil && currentBuild.GetBuildId() == build.GetBuildId() {
		return fmt.Errorf("view build %q could not be durably discarded", build.GetBuildId())
	}
	found, updated, lookupOK := s.finishRunningRebuildLog(ctx, opts, auth, view, build.GetBuildId(), pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, build.GetEntriesWritten(), cause)
	if !lookupOK || (found && !updated) {
		log.Printf("storage view interrupted failure history will be retried for %s/%s/%s", view.GetSpaceId(), view.GetViewId(), build.GetBuildId())
		if !lookupOK {
			s.attachRebuildLogFallback(view, build.GetBuildId(), pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, interruptedRebuildFailureItem(view, build, cause))
		}
	}
	if lookupOK && !found {
		s.recordInterruptedRebuildFailure(ctx, opts, auth, view, build, cause)
	}
	return nil
}

func (s *Service) failBuild(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, buildID, indexID string, cause error) bool {
	if active, err := s.readActiveView(ctx, opts.Metadata, auth, view.GetSpaceId(), view.GetViewId()); err == nil && active.GetActiveIndexId() == indexID {
		// Metadata has already committed this build. Never remove its physical
		// index merely because a stale/ambiguous failure response arrived.
		return false
	}
	message := "view build failed"
	if cause != nil {
		message = cause.Error()
	}
	rsp, err := opts.Metadata.FailViewIndexBuild(ctx, &pb.FailViewIndexBuildReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(),
		BuildId: buildID, OwnerId: opts.OwnerID, Error: message,
	})
	retErr := error(nil)
	if err == nil {
		retErr = requireSuccess(rsp.GetRetInfo())
	} else {
		retErr = err
	}
	if retErr != nil {
		log.Printf("storage view failed to mark build %s/%s as failed: %v", view.GetSpaceId(), view.GetViewId(), retErr)
		return false
	}
	if active, err := s.readActiveView(ctx, opts.Metadata, auth, view.GetSpaceId(), view.GetViewId()); err == nil && active.GetActiveIndexId() == indexID {
		return false
	}
	s.discardFailedBuild(ctx, view.GetSpaceId(), view.GetViewId(), indexID, view.GetEngine())
	return true
}

func (s *Service) discardFailedBuild(ctx context.Context, spaceID, viewID, indexID string, engineOverride ...string) {
	if indexID == "" {
		return
	}
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	expectedGeneration := s.indexGenerationOf(indexID)
	if runtime != nil {
		runtime.mu.Lock()
		if runtime.next == indexID {
			runtime.next = ""
			runtime.status = "active"
		} else if runtime.active == indexID {
			runtime.active = ""
			runtime.status = "failed"
		}
		runtime.mu.Unlock()
	}
	s.removeFailedBuildAtGeneration(ctx, indexID, expectedGeneration, engineOverride...)
}

func needsRebuild(view *pb.View, stats viewindex.ViewIndexStats) bool {
	if view == nil {
		return false
	}
	if view.GetActiveIndexId() == "" || view.GetDesiredViewRevision() > view.GetActiveViewRevision() {
		return true
	}
	if !stats.Exists {
		return true
	}
	keep, err := time.ParseDuration(view.GetKeepDuration())
	if err != nil || keep <= 0 || stats.IndexedFrom == "" || stats.IndexedTo == "" {
		return false
	}
	from, err := time.Parse(time.RFC3339Nano, stats.IndexedFrom)
	if err != nil {
		return false
	}
	to, err := time.Parse(time.RFC3339Nano, stats.IndexedTo)
	return err == nil && to.Sub(from) > 2*keep
}

func needsSizeLimitRebuild(view *pb.View, stats viewindex.ViewIndexStats, opts ReconcilerOptions) bool {
	if needsRebuild(view, stats) {
		return true
	}
	if view == nil || view.GetKeepDuration() == "" || view.GetKeepDuration() == "0" {
		// A permanent View cannot become smaller through an A/B rebuild, so do
		// not continuously rebuild it just because a byte watermark is crossed.
		return false
	}
	keep, err := time.ParseDuration(view.GetKeepDuration())
	if err != nil || keep <= 0 {
		return false
	}
	if opts.MaxViewFileBytes > 0 && stats.PhysicalBytes >= uint64(opts.MaxViewFileBytes) {
		return true
	}
	return false
}

func needsSizeLimitWatermark(view *pb.View, stats viewindex.ViewIndexStats, opts ReconcilerOptions) bool {
	if view == nil || !stats.Exists || view.GetKeepDuration() == "" || view.GetKeepDuration() == "0" {
		return false
	}
	keep, err := time.ParseDuration(view.GetKeepDuration())
	if err != nil || keep <= 0 || opts.MaxViewFileBytes <= 0 {
		return false
	}
	return stats.PhysicalBytes >= uint64(opts.MaxViewFileBytes) && !needsRebuild(view, stats)
}

func (s *Service) internalAuth() *pb.AuthInfo {
	const appID = "storage-view"
	return &pb.AuthInfo{AppId: appID, AppKey: datanode.ServiceAuthKey(s.authSecret, appID)}
}

func requireSuccess(ret *pb.RetInfo) error {
	if ret == nil {
		return errors.New("empty ret_info")
	}
	if ret.GetCode() != pb.ErrorCode_SUCCESS {
		return errors.New(ret.GetMsg())
	}
	return nil
}
