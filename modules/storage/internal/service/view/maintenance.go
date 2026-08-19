package view

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

func seriesCapacityDetails(offender viewindex.SeriesCapacityResult, maxPeriods, lookbackPeriods, physicalBytes uint64) string {
	details := map[string]any{
		"trigger":                  "SERIES_CAPACITY",
		"subject_id":               offender.SubjectID,
		"frequency":                offender.Freq,
		"series_tag":               offender.SeriesTag,
		"observed_periods":         offender.Rows,
		"max_periods_per_series":   maxPeriods,
		"rebuild_lookback_periods": lookbackPeriods,
		"physical_bytes":           physicalBytes,
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return `{"trigger":"SERIES_CAPACITY"}`
	}
	return string(encoded)
}

// Primary dataset changes are intentionally rejected by this simple A/B
// implementation. A backfill can enumerate the old active index, but it has
// no range-scan API to discover historical rows that exist only in the new
// primary dataset. Activating such a build would silently publish an
// incomplete View, so operators must create a new View for that migration.
var errPrimaryDatasetChangeUnsupported = errors.New("changing primary dataset requires a new View")

type MetadataClient interface {
	ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error)
	GetDataset(context.Context, *pb.GetDatasetReq, ...client.Option) (*pb.GetDatasetRsp, error)
	ListDatasetSubjects(context.Context, *pb.ListDatasetSubjectsReq, ...client.Option) (*pb.ListDatasetSubjectsRsp, error)
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

// TimeSeriesRangeReader is the internal Primary history path used when a View
// has no readable active index. It must enumerate the Primary dataset rather
// than routing a range read back through the View being rebuilt.
type TimeSeriesRangeReader interface {
	ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq, ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error)
}

type MaintenanceOptions struct {
	Metadata     MetadataClient
	Primary      FieldReader
	PrimaryRange TimeSeriesRangeReader
	Interval     time.Duration
	OwnerID      string
	Grace        time.Duration
	// MaxViewFileBytes triggers an A/B rebuild for a
	// finite-retention View. The rebuilt index contains only its keep window;
	// the tRPC cleanup Timer removes the retired physical slot later.
	MaxViewFileBytes    int64
	MaxPeriodsPerSeries uint64
	// RebuildLookback is the minimum wall-clock history every newly built
	// index must cover before activation. Production config supplies this;
	// zero keeps direct unit-test callers backwards compatible.
	RebuildLookback time.Duration
	// RebuildLookbackPeriods specifies the minimum completed bars required for
	// each time-series (subject, frequency, series tag) during Primary history
	// backfill. A frequency-specific value wins over default.
	RebuildLookbackPeriods map[string]uint64
	// RebuildMaxPending and RebuildIdleChecks gate optional size-limit
	// rebuilds. Necessary repairs bypass this capacity gate.
	RebuildMaxPending           uint64
	RebuildIdleChecks           uint32
	RebuildMaxPendingConfigured bool
	RebuildIdleChecksConfigured bool
	Policy                      storageconfig.ViewMaintenancePolicy
	// MaxHistoryScanRows bounds the Primary discovery scan used by a
	// period-based rebuild. It is a safety fuse: the per-series bar target
	// limits writes, while this limit prevents an incomplete/huge catalog from
	// reading millions of rows before discovering that target.
	MaxHistoryScanRows uint64
}

// Keep Primary history requests below the DataNode point-read budget. A
// 10k-row page becomes 100k+ Pebble gets for a wide K-line schema and can
// starve live reads; cursor pagination keeps the scan bounded instead.
// History pages contain keys only; field enrichment is split below the
// PrimaryStore key/field limit. Use the history reader's 10k page ceiling so
// a bounded 1,000-bar rebuild does not spend minutes on avoidable RPC/page
// overhead. Enrichment and index writes remain chunked independently.
const viewBackfillBatchSize = 10000
const capacityMaintenanceRetryInterval = 30 * time.Minute
const defaultRebuildMaxPending uint64 = 32
const defaultRebuildIdleChecks uint32 = 3
const defaultRebuildLookbackPeriods uint64 = 1000
const defaultMaxHistoryScanRows uint64 = 1_000_000
const capacityMaintenanceBuildBacklogThreshold = defaultRebuildMaxPending

// Coverage scans are intentionally decoupled from the cheap physical
// contract check. A large DuckDB View must not be COUNT/MIN/MAX-scanned on
// every maintenance tick.
const coverageRefreshInterval = 5 * time.Minute
const coverageStatTimeout = 5 * time.Second

// viewColumnsExplicitAttr preserves the distinction between a view that
// intentionally exposes no columns and a legacy view whose empty definition
// means "all primary dataset columns".
const viewColumnsExplicitAttr = "moox.columns_explicit"

func (s *Service) StartViewMaintainer(ctx context.Context, opts MaintenanceOptions) (func(), error) {
	var err error
	opts, err = s.normalizeMaintenanceOptions(opts)
	if err != nil {
		return nil, err
	}
	if err := s.maintainOnce(ctx, opts); err != nil {
		return nil, err
	}
	s.setMaintenanceReady(true)
	return s.startViewMaintenanceLoop(ctx, opts), nil
}

// StartViewMaintainerAsync starts the periodic check without making process
// startup wait for a potentially large historical backfill. Until the first
// successful pass completes, event handlers keep deliveries pending.
func (s *Service) StartViewMaintainerAsync(ctx context.Context, opts MaintenanceOptions) (func(), error) {
	var err error
	opts, err = s.normalizeMaintenanceOptions(opts)
	if err != nil {
		return nil, err
	}
	s.setMaintenanceReady(false)
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		firstPass := true
		run := func() {
			if err := s.maintainOnce(loopCtx, opts); err != nil {
				log.Printf("storage view maintenance failed: %v", err)
				return
			}
			if firstPass {
				s.setMaintenanceReady(true)
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

func (s *Service) normalizeMaintenanceOptions(opts MaintenanceOptions) (MaintenanceOptions, error) {
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
	if opts.MaxHistoryScanRows == 0 {
		opts.MaxHistoryScanRows = defaultMaxHistoryScanRows
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

func (s *Service) startViewMaintenanceLoop(ctx context.Context, opts MaintenanceOptions) func() {
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
				if err := s.maintainOnce(loopCtx, opts); err != nil {
					log.Printf("storage view maintenance failed: %v", err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

// RestoreActiveViews restores only the indexes already declared active by
// Metadata. It never claims or backfills a new index, so callers can run it
// before starting the EventBus consumer without holding up the live stream.
func (s *Service) RestoreActiveViews(ctx context.Context, opts MaintenanceOptions) error {
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
		// Keep the primary K-line View ahead of derived/factor Views during a
		// cold rebuild. Maintenance is deliberately single-flight; prioritizing
		// the business source prevents a slow factor backfill from delaying the
		// data View that downstream calculations depend on.
		sort.SliceStable(rsp.GetViews(), func(i, j int) bool {
			return viewMaintenancePriority(rsp.GetViews()[i]) < viewMaintenancePriority(rsp.GetViews()[j])
		})
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
				// rows can be routed while the initial maintenance is pending.
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

func validateRebuildLookback(stats viewindex.ViewIndexStats, lookback time.Duration) error {
	if lookback <= 0 {
		return nil
	}
	if !stats.Exists {
		return errors.New("rebuilt View index does not exist")
	}
	if strings.TrimSpace(stats.IndexedFrom) == "" || strings.TrimSpace(stats.IndexedTo) == "" {
		return fmt.Errorf("%w: rebuilt View index has no coverage; minimum lookback is %s", errRebuildLookbackInsufficient, lookback)
	}
	from, err := time.Parse(time.RFC3339Nano, stats.IndexedFrom)
	if err != nil {
		return fmt.Errorf("invalid rebuilt View indexed_from %q: %w", stats.IndexedFrom, err)
	}
	to, err := time.Parse(time.RFC3339Nano, stats.IndexedTo)
	if err != nil {
		return fmt.Errorf("invalid rebuilt View indexed_to %q: %w", stats.IndexedTo, err)
	}
	// Compare against the same minute-aligned boundary used by backfill. A
	// 1-minute bar at the boundary must count as covering the requested window,
	// rather than failing because validation happened a few seconds later.
	cutoff := time.Now().UTC().Add(-lookback).Truncate(time.Minute)
	if from.After(cutoff) {
		return fmt.Errorf("%w: rebuilt View coverage starts at %s, before %s is required", errRebuildLookbackInsufficient, from.Format(time.RFC3339Nano), lookback)
	}
	if to.Before(from) {
		return fmt.Errorf("rebuilt View coverage is inverted: %s > %s", from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
	}
	return nil
}

var errRebuildLookbackInsufficient = errors.New("rebuilt View lookback is insufficient")

func isPrimaryHistoryIndexNotReady(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "primary history index is still being materialized")
}

func (s *Service) validatePendingBuildCoverage(ctx context.Context, indexID, engineName string, lookback time.Duration) error {
	// Bleve indexes are record-oriented and do not expose a time-series
	// coverage watermark. The rebuild lookback contract applies to temporal
	// Views; record Views are complete when their schema/index is ready.
	if strings.EqualFold(strings.TrimSpace(engineName), "bleve") {
		return nil
	}
	engine := s.engines[strings.ToLower(strings.TrimSpace(engineName))]
	if engine == nil {
		return fmt.Errorf("view engine %q is unavailable", engineName)
	}
	stats, err := restoreIndexStats(ctx, engine, indexID)
	if err != nil {
		return fmt.Errorf("stat rebuilt View index %q coverage: %w", indexID, err)
	}
	return validateRebuildLookback(stats, lookback)
}

func (s *Service) maintainOnce(ctx context.Context, opts MaintenanceOptions) error {
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
		// Rebuild the business K-line View before lower-priority system and
		// derived Views during a cold start. All Views still get processed in
		// this pass; ordering only prevents a large metrics rebuild from
		// monopolizing the PrimaryStore while the user-facing K-line is empty.
		sort.SliceStable(rsp.GetViews(), func(i, j int) bool {
			return viewMaintenancePriority(rsp.GetViews()[i]) < viewMaintenancePriority(rsp.GetViews()[j])
		})
		for _, view := range rsp.GetViews() {
			if err := s.maintainView(ctx, opts, auth, view); err != nil {
				if errors.Is(err, errActiveContractUnavailable) {
					// Do not let the EventBus consumer ACK a legacy in-flight
					// rebuild whose active contract cannot be recovered. The
					// caller must repair/finish the View rebuild before startup.
					return err
				}
				if view != nil {
					log.Printf("storage view maintenance %s/%s failed: %v", view.GetSpaceId(), view.GetViewId(), err)
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

func viewMaintenancePriority(view *pb.View) int {
	if view == nil {
		return 100
	}
	switch view.GetViewId() {
	case "binance_spot_kline_1m_view":
		return 0
	case "binance_spot_kline_1m_factor_v":
		return 10
	default:
		return 50
	}
}

func (s *Service) maintainView(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" {
		return nil
	}
	if opts.Policy.MaxPeriodsPerSeries > 0 {
		resolved := opts.Policy.ResolvePolicy(view.GetSpaceId(), view.GetViewId())
		opts.MaxViewFileBytes = resolved.MaxViewFileBytes
		opts.MaxPeriodsPerSeries = resolved.MaxPeriodsPerSeries
		opts.RebuildLookbackPeriods = map[string]uint64{"default": resolved.RebuildLookbackPeriods}
	}
	// Time-series Views use a completed-bar budget rather than wall-clock
	// retention. This keeps weekends/holidays from shortening the rebuild. The
	// duration fallback remains for legacy Views whose frequency is unknown.
	lookbackPeriods := rebuildLookbackPeriodsForView(view, opts.RebuildLookbackPeriods)
	if lookbackPeriods > 0 {
		opts.RebuildLookback = 0
	} else {
		// A View's retention remains the fallback for legacy definitions that do
		// not expose a frequency in filter_json.
		opts.RebuildLookback = rebuildLookbackForView(view, opts.RebuildLookback)
	}
	var stats viewindex.ViewIndexStats
	var activeInvalidErr error
	var capacityOffender viewindex.SeriesCapacityResult
	var capacityDetails string
	if view.GetActiveIndexId() != "" {
		engineName := strings.ToLower(strings.TrimSpace(view.GetEngine()))
		engine := s.engines[engineName]
		if engine == nil {
			return fmt.Errorf("view engine %q is unavailable", engineName)
		}
		var err error
		// Reconciliation runs on every interval.  A full DuckDB Stat performs
		// COUNT/MIN/MAX scans over the entire View and can time out on a large
		// index, making a healthy active index look invalid and starting a
		// needless A/B rebuild.  Use the metadata-only contract check here;
		// the physical size is still available for the watermark gate.
		var metadataOnly bool
		stats, metadataOnly, err = maintainIndexStats(ctx, engine, view.GetActiveIndexId())
		if err != nil {
			return err
		}
		// Metadata-only stats deliberately omit coverage fields. Preserve the
		// last full coverage snapshot instead of replacing it with empty values;
		// otherwise TotalMode=NONE queries lose their completeness information
		// and retention checks are silently disabled.
		_, runtime := s.activeIndex(view.GetSpaceId(), view.GetViewId())
		if metadataOnly {
			if cached, ok := cachedActiveIndexStats(runtime, view.GetActiveIndexId()); ok {
				if stats.EntryCount == 0 {
					stats.EntryCount = cached.EntryCount
				}
				if stats.IndexedFrom == "" {
					stats.IndexedFrom = cached.IndexedFrom
				}
				if stats.IndexedTo == "" {
					stats.IndexedTo = cached.IndexedTo
				}
			}
			if shouldRefreshCoverage(runtime) {
				coverageCtx, cancel := context.WithTimeout(ctx, coverageStatTimeout)
				fullStats, fullErr := engine.Stat(coverageCtx, view.GetActiveIndexId())
				cancel()
				markCoverageRefresh(runtime)
				if fullErr == nil && fullStats.Exists {
					stats = fullStats
					metadataOnly = false
					markCoverageRefresh(runtime)
				} else if fullErr != nil {
					log.Printf("storage view coverage refresh deferred space=%s view=%s: %v", view.GetSpaceId(), view.GetViewId(), fullErr)
				}
			}
		}
		if stats.Exists {
			if lookbackPeriods > 0 && opts.MaxPeriodsPerSeries > 0 {
				if reader, ok := engine.(viewindex.SeriesCapacityReader); ok {
					capacityOffender, err = reader.SeriesCapacity(ctx, view.GetActiveIndexId(), opts.MaxPeriodsPerSeries)
					if err != nil {
						return fmt.Errorf("check View series capacity: %w", err)
					}
					if capacityOffender.Exceeded {
						capacityDetails = seriesCapacityDetails(capacityOffender, opts.MaxPeriodsPerSeries, lookbackPeriods, stats.PhysicalBytes)
					}
				}
			}
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
				if !metadataOnly || stats.IndexedFrom != "" || stats.IndexedTo != "" || stats.EntryCount != 0 {
					s.cacheActiveIndexStats(runtime, view.GetActiveIndexId(), stats)
				}
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
			if view.GetActiveIndexId() == "" && build.GetState() != pb.ViewIndexBuild_PREPARING {
				if err := s.validatePendingBuildCoverage(ctx, build.GetIndexId(), build.GetEngine(), opts.RebuildLookback); err != nil {
					if errors.Is(err, errRebuildLookbackInsufficient) {
						log.Printf("storage view initial rebuild is still priming space=%s view=%s: %v", view.GetSpaceId(), view.GetViewId(), err)
						return nil
					}
					return err
				}
				if err := s.catchUpAndActivateViewBuild(ctx, opts, auth, view, build.GetBuildId(), build.GetIndexId(), build.GetEngine(), build.GetTargetViewVersion(), build.GetSchemaHash(), build.GetColumns(), build.GetEntriesWritten()); err != nil {
					return err
				}
				return nil
			}
			// There is deliberately no resumable build cursor. A process exit
			// discards the partial inactive index and the next maintenance starts
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
			if opts.RebuildLookback > 0 {
				if err := s.validatePendingBuildCoverage(ctx, build.GetIndexId(), build.GetEngine(), opts.RebuildLookback); err != nil {
					if errors.Is(err, errRebuildLookbackInsufficient) && view.GetActiveIndexId() == "" {
						log.Printf("storage view READY rebuild is still priming space=%s view=%s: %v", view.GetSpaceId(), view.GetViewId(), err)
						return nil
					}
					s.failBuild(ctx, opts, auth, view, build.GetBuildId(), build.GetIndexId(), err)
					return err
				}
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
	coverageRepair := needsLookbackRepair(view, stats, opts.RebuildLookback)
	seriesCapacityExceeded := capacityOffender.Exceeded
	rebuildNeeded := needsCapacityMaintenanceRebuild(view, stats, opts) || coverageRepair || seriesCapacityExceeded
	if activeInvalidErr != nil {
		rebuildNeeded = true
	}
	manualRequested := manualRebuildRequested(view)
	// Coverage repair is mandatory: it must run even while an unrelated
	// partition has backlog. The size watermark rebuild remains optional and
	// continues to use the idle/backlog gate below.
	capacityMaintenanceOnly := activeInvalidErr == nil && !coverageRepair && !manualRequested && (seriesCapacityExceeded || needsCapacityMaintenanceWatermark(view, stats, opts))
	now := time.Now().UTC()
	triggerReason := rebuildTriggerReason(view, stats, capacityMaintenanceOnly, seriesCapacityExceeded)
	if seriesCapacityExceeded {
		log.Printf("storage view series capacity exceeded space=%s view=%s subject=%s freq=%s series_tag=%s rows=%d limit=%d", view.GetSpaceId(), view.GetViewId(), capacityOffender.SubjectID, capacityOffender.Freq, capacityOffender.SeriesTag, capacityOffender.Rows, opts.MaxPeriodsPerSeries)
	}
	if coverageRepair && !manualRequested {
		triggerReason = pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_COVERAGE_REPAIR
	}
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
	if capacityMaintenanceOnly {
		var backlogErr error
		pending, ackPending, backlogErr = s.consumerBacklogForView(ctx, viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()})
		if backlogErr != nil {
			gateBlockReason = "consumer_unavailable"
		} else if pending+ackPending > maxPending {
			gateBlockReason = "consumer_backlog"
		} else {
			gateBlockReason = "consumer_idle_checks"
		}
	}
	if capacityMaintenanceOnly && !s.capacityMaintenanceBuildAllowed(view.GetSpaceId(), view.GetViewId(), now) {
		s.recordSkippedRebuild(ctx, opts, auth, view, triggerReason, "cooldown", stats, stats.PhysicalBytes, pending, ackPending, capacityDetails)
		return nil
	}
	if capacityMaintenanceOnly && !s.capacityMaintenanceBuildIdleFor(ctx, viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}, maxPending, idleChecks) {
		// A byte-watermark rebuild is optional while the durable consumer is
		// catching up. Backfill competes with live A/B writes for the same
		// DuckDB memory and I/O; starting it under backlog can turn one large
		// system View into cross-Dataset head-of-line blocking.
		s.recordSkippedRebuild(ctx, opts, auth, view, triggerReason, gateBlockReason, stats, stats.PhysicalBytes, pending, ackPending, capacityDetails)
		return nil
	}
	if capacityMaintenanceOnly && !s.tryAcquireRebuild() {
		s.recordSkippedRebuild(ctx, opts, auth, view, triggerReason, "another_rebuild_running", stats, stats.PhysicalBytes, pending, ackPending, capacityDetails)
		return nil
	}
	if capacityMaintenanceOnly {
		s.resetIdleChecks(viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()})
	}
	if capacityMaintenanceOnly {
		defer s.releaseRebuild()
	}
	// A failed size-limit rebuild must not be retried forever when the
	// watermark is no longer exceeded. This is especially important for a
	// large DuckDB view on a memory-constrained host: the failed inactive
	// index is removed above, while the healthy active index remains usable.
	if !shouldRetryFailedBuildWithCause(view, failedBuild, rebuildNeeded, capacityMaintenanceOnly, now) {
		return nil
	}
	if capacityMaintenanceOnly {
		s.markCapacityMaintenanceBuild(view.GetSpaceId(), view.GetViewId(), now)
	}
	columns := view.GetColumns()
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] != "true" {
		var err error
		columns, err = loadDefaultViewColumns(ctx, opts.Metadata, auth, view)
		if err != nil {
			return err
		}
	}
	// PrepareViewIndex may need the full catalog contract while it attaches a
	// first index. Keep metadata fields (notably filter_json/frequency)
	// available to Primary history backfill instead of reconstructing a reduced
	// View from the physical schema alone.
	s.mu.Lock()
	s.catalogViews[viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}] = proto.Clone(view).(*pb.View)
	s.mu.Unlock()
	schema := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: view.GetDesiredViewRevision(),
		PrimaryDatasetID: view.GetPrimaryDatasetId(), Engine: strings.ToLower(view.GetEngine()), Columns: columns,
	}
	schema.SchemaHash = viewindex.HashViewIndexSchema(schema)
	indexID := viewindex.InactiveViewIndexID(view.GetSpaceId(), view.GetViewId(), view.GetActiveIndexId())
	if s.isIndexRetiring(indexID) {
		// Keep the old active index readable until its grace period has elapsed.
		// Reusing the slot would close a reader that already captured the old ID.
		if capacityMaintenanceOnly {
			s.recordSkippedRebuild(ctx, opts, auth, view, triggerReason, "inactive_slot_retiring", stats, stats.PhysicalBytes, pending, ackPending, capacityDetails)
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
	buildLog := s.createRunningRebuildLog(ctx, opts, auth, view, buildID, indexID, triggerReason, stats, stats.PhysicalBytes, pending, ackPending, capacityDetails)
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
				DetailsJson: appendRebuildPhase(capacityDetails, "maintenance_fallback"),
			}
			if !s.createRebuildLogFallback(ctx, opts, auth, fallback) {
				s.queueRebuildLogRetry(pendingRebuildLog{opts: opts, auth: auth, view: proto.Clone(view).(*pb.View), buildID: buildID, result: result, entries: entries, cause: cause, fallback: fallback})
			}
		} else if !lookupOK {
			s.attachRebuildLogFallback(view, buildID, result, &pb.ViewRebuildLog{
				SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID, IndexId: indexID,
				TriggerReason: triggerReason, Result: result, TargetViewRevision: view.GetDesiredViewRevision(),
				ActiveViewRevision: view.GetActiveViewRevision(), EntriesWritten: entries,
				FinishedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorSummary: rebuildErrorSummary(cause), DetailsJson: appendRebuildPhase(capacityDetails, "maintenance_fallback"),
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
	s.updateRunningRebuildLogPhase(ctx, opts, auth, view, buildID, buildLog, "prepare")
	if err := s.TrackViewBuild(ctx, view.GetSpaceId(), view.GetViewId(), buildID, opts.OwnerID, opts.Metadata, auth); err != nil {
		finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, 0, err)
		return err
	}
	if err := s.updateBuild(ctx, opts, auth, view, buildID, pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING, 0); err != nil {
		s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
		finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, 0, err)
		return err
	}
	var entriesWritten uint64
	if view.GetActiveIndexId() != "" && !stats.Exists && opts.PrimaryRange == nil {
		err := fmt.Errorf("active view index %q is missing and cannot be rebuilt without a range reader", view.GetActiveIndexId())
		s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
		finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, 0, err)
		return err
	}
	primaryHistoryRequired := lookbackPeriods > 0 && !strings.EqualFold(strings.TrimSpace(schema.Engine), "bleve")
	if (view.GetActiveIndexId() != "" && stats.Exists) || opts.PrimaryRange != nil || primaryHistoryRequired {
		s.updateRunningRebuildLogPhase(ctx, opts, auth, view, buildID, buildLog, "backfill")
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
		var backfillErr error
		entriesWritten, backfillErr = s.backfillViewWithReader(buildCtx, view.GetSpaceId(), view.GetViewId(), viewBackfillBatchSize, opts.Primary, opts.PrimaryRange, opts.RebuildLookback, lookbackPeriods, opts.MaxHistoryScanRows)
		if backfillErr != nil {
			s.failBuild(ctx, opts, auth, view, buildID, indexID, backfillErr)
			if isPrimaryHistoryIndexNotReady(backfillErr) {
				// The derived Primary history index is materialized asynchronously
				// on first use. Keep this build out of the active path, but do not
				// turn a normal warm-up wait into a failed maintenance/health cycle.
				finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SKIPPED, entriesWritten, backfillErr)
				return nil
			}
			finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, entriesWritten, backfillErr)
			return backfillErr
		}
		s.updateRunningRebuildLogPhase(ctx, opts, auth, view, buildID, buildLog, "catch_up")
	}
	if opts.RebuildLookback > 0 {
		if err := s.validatePendingBuildCoverage(ctx, indexID, schema.Engine, opts.RebuildLookback); err != nil {
			if view.GetActiveIndexId() == "" && errors.Is(err, errRebuildLookbackInsufficient) {
				log.Printf("storage view initial rebuild is priming space=%s view=%s: %v", view.GetSpaceId(), view.GetViewId(), err)
				return nil
			}
			s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
			finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, entriesWritten, err)
			return err
		}
	}
	s.updateRunningRebuildLogPhase(ctx, opts, auth, view, buildID, buildLog, "activate")
	err = s.catchUpAndActivateViewBuild(ctx, opts, auth, view, buildID, indexID, schema.Engine, schema.ViewVersion, schema.SchemaHash, columns, entriesWritten)
	if err != nil {
		var activation activationRetry
		if errors.As(err, &activation) {
			// Activation retries are still part of the same running build. Do
			// not turn a transient metadata/cache response into a failed log.
			return err
		}
		s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
		finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED, entriesWritten, err)
	} else {
		finishLog(pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SUCCEEDED, entriesWritten, nil)
	}
	return err
}

func rebuildLookbackForView(view *pb.View, fallback time.Duration) time.Duration {
	if view == nil {
		return fallback
	}
	raw := strings.TrimSpace(view.GetKeepDuration())
	if raw == "" || raw == "0" {
		return fallback
	}
	lookback, err := time.ParseDuration(raw)
	if err != nil || lookback <= 0 {
		return fallback
	}
	return lookback
}

func viewFrequencyValue(view *pb.View) string {
	if view == nil || strings.TrimSpace(view.GetFilterJson()) == "" {
		return ""
	}
	var filter struct {
		Frequency string `json:"freq"`
	}
	if err := json.Unmarshal([]byte(view.GetFilterJson()), &filter); err != nil {
		return ""
	}
	return strings.TrimSpace(filter.Frequency)
}

func rebuildLookbackPeriodsForView(view *pb.View, configured map[string]uint64) uint64 {
	frequency := strings.ToLower(viewFrequencyValue(view))
	if frequency == "" {
		// A period budget is meaningful only for a frequency-bound time-series
		// View. Record/metrics Views without a freq filter continue to use their
		// wall-clock retention lookback; forcing the default 1,000 bars here
		// would make every such rebuild fail with "frequency is required".
		return 0
	}
	if len(configured) == 0 {
		return defaultRebuildLookbackPeriods
	}
	if periods := configured[frequency]; periods > 0 {
		return periods
	}
	if periods := configured["default"]; periods > 0 {
		return periods
	}
	return defaultRebuildLookbackPeriods
}

func maintainIndexStats(ctx context.Context, engine viewindex.Engine, indexID string) (viewindex.ViewIndexStats, bool, error) {
	if reader, ok := engine.(viewindex.MetadataStatReader); ok {
		stats, err := reader.StatMetadata(ctx, indexID)
		return stats, true, err
	}
	stats, err := engine.Stat(ctx, indexID)
	return stats, false, err
}

func shouldRefreshCoverage(runtime *viewRuntime) bool {
	if runtime == nil {
		return true
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.statsRefreshedAt.IsZero() || time.Since(runtime.statsRefreshedAt) >= coverageRefreshInterval
}

func markCoverageRefresh(runtime *viewRuntime) {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	runtime.statsRefreshedAt = time.Now().UTC()
	runtime.mu.Unlock()
}

func (s *Service) capacityMaintenanceBuildIdle(ctx context.Context) bool {
	return s.capacityMaintenanceBuildIdleFor(ctx, viewRef{}, capacityMaintenanceBuildBacklogThreshold, 1)
}

func (s *Service) capacityMaintenanceBuildIdleFor(ctx context.Context, ref viewRef, maxPending uint64, requiredChecks uint32) bool {
	pending, ackPending, err := s.consumerBacklogForView(ctx, ref)
	if err != nil {
		// Fail closed for optional capacity work. Schema/contract repairs do not
		// use this gate and continue to maintenance normally.
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
	return s.consumerBacklogForView(ctx, viewRef{})
}

func (s *Service) consumerBacklogForView(ctx context.Context, ref viewRef) (uint64, uint64, error) {
	s.mu.RLock()
	stateReader := s.consumerState
	boundReader := s.consumerBound
	partitionStates := s.consumerStates
	partitionBounds := s.consumerBounds
	partitionByDataset := s.consumerPartitionByDataset
	view := s.catalogViews[ref]
	s.mu.RUnlock()
	partitionIDs := make(map[string]struct{})
	if ref != (viewRef{}) && view != nil {
		datasetIDs := append([]string(nil), view.GetDatasetIds()...)
		if primary := strings.TrimSpace(view.GetPrimaryDatasetId()); primary != "" {
			datasetIDs = append(datasetIDs, primary)
		}
		// A View may project columns from secondary Datasets even when those
		// Datasets are not listed in DatasetIds. Include their origin prefix so
		// every partition that can deliver live changes participates in the
		// rebuild gate.
		for _, column := range view.GetColumns() {
			if datasetID, _, ok := strings.Cut(strings.TrimSpace(column.GetOriginId()), "."); ok && datasetID != "" {
				datasetIDs = append(datasetIDs, datasetID)
			}
		}
		for _, datasetID := range datasetIDs {
			if partitionID := partitionByDataset[datasetRef{spaceID: ref.spaceID, datasetID: strings.TrimSpace(datasetID)}]; partitionID != "" {
				partitionIDs[partitionID] = struct{}{}
			}
		}
	}
	if len(partitionIDs) != 0 && len(partitionStates) != 0 {
		stateCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var pending, ackPending uint64
		for partitionID := range partitionIDs {
			bound := partitionBounds[partitionID]
			if bound == nil || !bound() {
				return 0, 0, fmt.Errorf("storage view consumer partition %q is not bound", partitionID)
			}
			reader := partitionStates[partitionID]
			if reader == nil {
				return 0, 0, fmt.Errorf("storage view consumer partition %q is unavailable", partitionID)
			}
			state, err := reader(stateCtx)
			if err != nil {
				return 0, 0, fmt.Errorf("consumer partition %q: %w", partitionID, err)
			}
			if s.metrics != nil {
				// The durable is already exposed by the partition metric state;
				// update pending values without introducing dataset labels.
				s.metrics.ObserveConsumerPartitionBacklog(partitionID, state.NumPending, uint64(state.NumAckPending))
			}
			pending += state.NumPending
			ackPending += uint64(state.NumAckPending)
		}
		return pending, ackPending, nil
	}
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

func (s *Service) capacityMaintenanceBuildAllowed(spaceID, viewID string, now time.Time) bool {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return true
	}
	runtime.mu.Lock()
	last := runtime.lastCapacityMaintenanceBuildAt
	runtime.mu.Unlock()
	return last.IsZero() || !now.Before(last.Add(capacityMaintenanceRetryInterval))
}

func (s *Service) markCapacityMaintenanceBuild(spaceID, viewID string, now time.Time) {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	runtime.lastCapacityMaintenanceBuildAt = now
	runtime.mu.Unlock()
}

func shouldRetryFailedBuild(view *pb.View, failedBuild *pb.ViewIndexBuild, capacityMaintenanceExceeded bool, now time.Time) bool {
	return shouldRetryFailedBuildWithCause(view, failedBuild, capacityMaintenanceExceeded, capacityMaintenanceExceeded, now)
}

func shouldRetryFailedBuildWithCause(view *pb.View, failedBuild *pb.ViewIndexBuild, rebuildNeeded, capacityMaintenanceOnly bool, now time.Time) bool {
	if failedBuild == nil {
		return rebuildNeeded
	}
	if view != nil && view.GetDesiredViewRevision() != view.GetActiveViewRevision() {
		// A bounded Primary discovery scan can fail because the dataset has
		// more history than the safety fuse. Do not immediately repeat the same
		// million-row scan every minute; an operator can request a new revision
		// after fixing the catalog/filter, while the failed build remains
		// auditable in Metadata during the cooldown.
		if strings.Contains(strings.ToLower(failedBuild.GetError()), "history scan exceeded") {
			updatedAt, err := time.Parse(time.RFC3339Nano, failedBuild.GetUpdatedAt())
			if err != nil || updatedAt.IsZero() {
				return false
			}
			return !now.Before(updatedAt.Add(capacityMaintenanceRetryInterval))
		}
		return true
	}
	if !rebuildNeeded {
		return false
	}
	// Missing active files and coverage/revision repairs must be retried on the
	// next reconciliation. The cooldown is only a guard for repeated physical
	// watermark rebuilds, which are intentionally optional while A remains
	// healthy.
	if !capacityMaintenanceOnly {
		return true
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, failedBuild.GetUpdatedAt())
	if err != nil || updatedAt.IsZero() {
		return true
	}
	return !now.Before(updatedAt.Add(capacityMaintenanceRetryInterval))
}

type resumeBuildRetry struct{ cause error }

func (e resumeBuildRetry) Error() string { return e.cause.Error() }
func (e resumeBuildRetry) Unwrap() error { return e.cause }

type activationRetry struct{ cause error }

func (e activationRetry) Error() string { return e.cause.Error() }
func (e activationRetry) Unwrap() error { return e.cause }

func (s *Service) catchUpAndActivateViewBuild(
	ctx context.Context,
	opts MaintenanceOptions,
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

func (s *Service) resumeViewBuild(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View, previous *pb.ViewIndexBuild) error {
	lookbackPeriods := rebuildLookbackPeriodsForView(view, opts.RebuildLookbackPeriods)
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
	s.updateRunningRebuildLogPhase(ctx, opts, auth, view, build.GetBuildId(), nil, "backfill")
	var entriesWritten uint64
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
	needsPrimaryHistory := opts.PrimaryRange != nil || (lookbackPeriods > 0 && !strings.EqualFold(strings.TrimSpace(previous.GetEngine()), "bleve"))
	if activePhysicalExists || needsPrimaryHistory {
		if err := s.updateBuild(ctx, opts, auth, view, build.GetBuildId(), pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP, entriesWritten); err != nil {
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
		var backfillErr error
		entriesWritten, backfillErr = s.backfillViewWithReader(buildCtx, view.GetSpaceId(), view.GetViewId(), viewBackfillBatchSize, opts.Primary, opts.PrimaryRange, opts.RebuildLookback, lookbackPeriods, opts.MaxHistoryScanRows)
		if backfillErr != nil {
			return backfillErr
		}
		if err := s.updateBuild(ctx, opts, auth, view, build.GetBuildId(), pb.ViewIndexBuild_CATCHING_UP, pb.ViewIndexBuild_READY, entriesWritten); err != nil {
			return err
		}
	} else {
		if err := s.updateBuild(ctx, opts, auth, view, build.GetBuildId(), pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_READY, entriesWritten); err != nil {
			return err
		}
	}
	if opts.RebuildLookback > 0 {
		if err := s.validatePendingBuildCoverage(ctx, build.GetIndexId(), build.GetEngine(), opts.RebuildLookback); err != nil {
			return err
		}
	}
	s.updateRunningRebuildLogPhase(ctx, opts, auth, view, build.GetBuildId(), nil, "activate")
	return s.activateViewBuild(ctx, opts, auth, view, build.GetBuildId(), build.GetIndexId(), build.GetEngine(), build.GetTargetViewVersion(), build.GetSchemaHash(), build.GetColumns())
}

// activateViewBuild makes the metadata commit and the in-memory pointer switch
// one short View operation. A live writer cannot fail/remove the replacement
// between those two transitions. Any non-success response is read back before
// deciding whether the build is still pending; an ambiguous response must not
// discard an index that metadata may already have made active.
func (s *Service) activateViewBuild(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View, buildID, indexID, engineName string, revision uint64, schemaHash string, columns []*pb.ViewColumn) error {
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
	var err error
	if runtime.active != "" {
		_, _, err = s.switchViewLocked(context.WithoutCancel(ctx), runtime)
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

func (s *Service) updateBuild(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View, buildID string, from, to pb.ViewIndexBuild_State, rows uint64) error {
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
func (s *Service) failInterruptedBuild(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View, build *pb.ViewIndexBuild) error {
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

func (s *Service) failBuild(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View, buildID, indexID string, cause error) bool {
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
	if needsActiveOrRevisionRebuild(view, stats) {
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

func needsActiveOrRevisionRebuild(view *pb.View, stats viewindex.ViewIndexStats) bool {
	if view == nil {
		return false
	}
	return view.GetActiveIndexId() == "" || view.GetDesiredViewRevision() > view.GetActiveViewRevision() || !stats.Exists
}

// needsLookbackRepair reports a known-short time-series active index. Empty
// coverage is deliberately not treated as a repair here: it can mean that a
// large index has not completed its periodic full Stat yet. Once persisted
// bounds are available, a short active index must be rebuilt from Primary.
func needsLookbackRepair(view *pb.View, stats viewindex.ViewIndexStats, lookback time.Duration) bool {
	if view == nil || lookback <= 0 || !stats.Exists || stats.IndexedFrom == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(view.GetEngine()), "bleve") {
		return false
	}
	return errors.Is(validateRebuildLookback(stats, lookback), errRebuildLookbackInsufficient)
}

func needsCapacityMaintenanceRebuild(view *pb.View, stats viewindex.ViewIndexStats, opts MaintenanceOptions) bool {
	// Period-based Views intentionally retain a fixed number of completed bars
	// per series. Their global indexed_from/indexed_to span can legitimately
	// exceed 2*keep_duration when symbols have different listing histories or
	// trading gaps; treating that span as a retention watermark would launch a
	// coverage rebuild on every maintenance tick. Keep missing/definition
	// repairs, but leave the global span check to duration-based Views.
	periodBased := rebuildLookbackPeriodsForView(view, opts.RebuildLookbackPeriods) > 0
	if needsActiveOrRevisionRebuild(view, stats) || (!periodBased && needsRebuild(view, stats)) {
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

func needsCapacityMaintenanceWatermark(view *pb.View, stats viewindex.ViewIndexStats, opts MaintenanceOptions) bool {
	if view == nil || !stats.Exists || view.GetKeepDuration() == "" || view.GetKeepDuration() == "0" {
		return false
	}
	keep, err := time.ParseDuration(view.GetKeepDuration())
	if err != nil || keep <= 0 || opts.MaxViewFileBytes <= 0 {
		return false
	}
	if needsActiveOrRevisionRebuild(view, stats) {
		return false
	}
	periodBased := rebuildLookbackPeriodsForView(view, opts.RebuildLookbackPeriods) > 0
	if !periodBased && needsRebuild(view, stats) {
		return false
	}
	return stats.PhysicalBytes >= uint64(opts.MaxViewFileBytes)
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
