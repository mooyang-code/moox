package view

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

// defaultStaleBuildingAfter is how long a "building" claim may run before a
// subsequent rotate pass treats it as crashed and clears it for restart.
const defaultStaleBuildingAfter = 10 * time.Minute

// BackfillFunc backfills a warming View index from PrimaryStore and reports
// whether the backfill scan completed within this rotate claim. It is the
// Task 7 seam: production wiring in Task 6 uses stubBackfill, which never
// reports completion, so RotateViewIndexes never calls CompleteViewBuild
// until real PrimaryStore backfill windows exist. Tests inject a fake
// BackfillFunc that returns done=true to exercise the full
// warm -> ready -> switch -> grace remove path end to end.
type BackfillFunc func(ctx context.Context, engine viewindex.ViewIndexEngine, item *pb.View, indexID string) (done bool, err error)

// RotationConfig controls RotateViewIndexes lifecycle behavior. All duration
// fields are already parsed; see cmd/server for conversion from the YAML
// string config.
type RotationConfig struct {
	// Enabled is the full kill switch for RotateViewIndexes. When false, the
	// entire rotate pass is a no-op: no schema warming, no capacity warming,
	// no ready switch, no orphan sweep.
	Enabled bool
	// MaxEntries is the capacity threshold for active indexes.
	MaxEntries int64
	// MinReadyEntries is a large-View guard only; small Views can switch as
	// soon as backfill completes even if below this number.
	MinReadyEntries int64
	// OverlapWindow is the late-arrival/disorder safety buffer.
	OverlapWindow time.Duration
	// DefaultBackfillWindow is the fallback backfill window.
	DefaultBackfillWindow time.Duration
	// AllowedLag is the maximum lag between a warming index MaxVersion and
	// now before the index is considered ready, when versions are
	// parseable timestamps.
	AllowedLag time.Duration
	// RemoveGrace is the delay before removing an old active or obsolete
	// warming index after switch/cancel.
	RemoveGrace time.Duration
	// TimeSeriesFreqBackfillWindow gives frequency-aware minimum backfill
	// windows for TimeSeries Views, keyed by dataset freq (e.g. "1m").
	TimeSeriesFreqBackfillWindow map[string]time.Duration
	// RecordDefaultVersionWindow is the default Record View version range
	// when versions are timestamp-like.
	RecordDefaultVersionWindow time.Duration
	// RecordMaxBackfillEntries caps Record View backfill pages.
	RecordMaxBackfillEntries int64
	// StaleBuildingAfter is how long a "building" claim may run before it
	// is treated as crashed/stale. Defaults to 10 minutes when zero.
	StaleBuildingAfter time.Duration
}

// RotationOptions configures a RotationManager.
type RotationOptions struct {
	// Metadata is the View metadata store used for claim/complete and
	// listing Views to rotate.
	Metadata Metadata
	// Engines maps a View's engine name (lower-case, e.g. "duckdb",
	// "bleve") to the ViewIndexEngine implementation that serves it.
	Engines map[string]viewindex.ViewIndexEngine
	// Config controls rotation thresholds and windows.
	Config RotationConfig
	// Now returns the current time; defaults to time.Now.
	Now func() time.Time
	// Backfill backfills a warming index from PrimaryStore. When nil,
	// stubBackfill is used and warming indexes never complete (Task 6
	// default; Task 7 replaces this with a real implementation).
	Backfill BackfillFunc
}

// RotationManager implements the unified op=rotate View index lifecycle:
// schema-preemptive rebuild, capacity rotation, stale/failed warming
// cleanup, ready switch, and orphan sweep. RotateViewIndexes is the only
// entry point; there is no separate cleanup/retry_failed/pending-rebuild
// path.
type RotationManager struct {
	metadata Metadata
	engines  map[string]viewindex.ViewIndexEngine
	cfg      RotationConfig
	now      func() time.Time
	backfill BackfillFunc

	mu       sync.Mutex
	claims   map[string]bool
	removals map[string]time.Time
}

// NewRotationManager creates a RotationManager from the given options.
func NewRotationManager(opts RotationOptions) *RotationManager {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	engines := opts.Engines
	if engines == nil {
		engines = map[string]viewindex.ViewIndexEngine{}
	}
	cfg := opts.Config
	if cfg.StaleBuildingAfter <= 0 {
		cfg.StaleBuildingAfter = defaultStaleBuildingAfter
	}
	return &RotationManager{
		metadata: opts.Metadata,
		engines:  engines,
		cfg:      cfg,
		now:      now,
		backfill: opts.Backfill,
		claims:   make(map[string]bool),
		removals: make(map[string]time.Time),
	}
}

// stubBackfill is the Task 6 placeholder backfill hook. It never reports
// completion, so production RotateViewIndexes never calls CompleteViewBuild
// until Task 7 wires real PrimaryStore backfill.
func stubBackfill(context.Context, viewindex.ViewIndexEngine, *pb.View, string) (bool, error) {
	return false, nil
}

// RotateViewIndexes is the single scheduler entry for the View index
// lifecycle. When spaceID is empty it rotates Views across all spaces.
func (r *RotationManager) RotateViewIndexes(ctx context.Context, spaceID string) (int, error) {
	if r == nil || r.metadata == nil {
		return 0, errors.New("rotation manager requires metadata")
	}
	if !r.cfg.Enabled {
		return 0, nil
	}
	views, err := r.listViewsForRotate(ctx, spaceID)
	if err != nil {
		return 0, err
	}
	var rotated int
	var rotateErr error
	for _, item := range views {
		changed, err := r.rotateView(ctx, item)
		if err != nil {
			rotateErr = errors.Join(rotateErr, fmt.Errorf("rotate view %s/%s: %w", item.GetSpaceId(), item.GetViewId(), err))
			continue
		}
		if changed {
			rotated++
		}
	}
	// Re-list after rotating so the referenced-index set used by sweep
	// reflects this pass's active_result/building_result transitions
	// (e.g. a just-cleared building slot must become eligible for grace
	// removal in the same rotate pass instead of lagging one pass behind).
	sweepViews := views
	if fresh, err := r.listViewsForRotate(ctx, spaceID); err == nil {
		sweepViews = fresh
	}
	r.sweep(ctx, spaceID, sweepViews)
	return rotated, rotateErr
}

// rotateView applies the rotate decision order to a single View.
func (r *RotationManager) rotateView(ctx context.Context, item *pb.View) (bool, error) {
	engine, engineKey, ok := r.engineFor(item)
	if !ok {
		return false, nil
	}
	if !r.claimView(item) {
		return false, nil
	}
	defer r.releaseView(item)

	now := r.now()
	switch {
	case r.buildingObsoleteOrStale(item, now):
		return r.clearObsoleteBuilding(ctx, item, engineKey)
	case item.GetViewVersion() > item.GetActiveViewVersion() && !viewindex.BuildingIndexWritable(item):
		return r.startWarming(ctx, item, engine, engineKey)
	case viewindex.BuildingIndexWritable(item):
		return r.progressWarming(ctx, item, engine, engineKey)
	case item.GetViewVersion() == item.GetActiveViewVersion():
		return r.maybeStartCapacityWarming(ctx, item, engine, engineKey)
	default:
		return false, nil
	}
}

// startWarming runs the mandatory warming start sequence: Prepare the
// inactive a/b slot, then conditionally claim it via BeginViewBuild, then
// try to progress the warming (backfill + ready check + Complete) within
// the same claim when possible.
func (r *RotationManager) startWarming(ctx context.Context, item *pb.View, engine viewindex.ViewIndexEngine, engineKey string) (bool, error) {
	targetVersion := item.GetViewVersion()
	if targetVersion == 0 {
		targetVersion = 1
	}
	indexID := viewindex.InactiveViewIndexID(item.GetSpaceId(), item.GetViewId(), item.GetActiveResult())
	columns, err := r.viewColumns(ctx, item)
	if err != nil {
		return false, err
	}
	schema := viewindex.ViewIndexSchema{
		SpaceID:     item.GetSpaceId(),
		ViewID:      item.GetViewId(),
		ViewVersion: targetVersion,
		Engine:      engineKey,
		Columns:     columns,
	}
	schema.SchemaHash = viewindex.HashViewIndexSchema(schema)
	if err := engine.Prepare(ctx, indexID, schema); err != nil {
		return false, fmt.Errorf("prepare warming index %s: %w", indexID, err)
	}
	claimed, err := r.metadata.BeginViewBuild(ctx, item.GetSpaceId(), item.GetViewId(), targetVersion, indexID)
	if err != nil {
		// Another view_builder replica won the conditional claim race; not
		// a hard failure for this rotate pass.
		return false, nil
	}
	return r.progressWarming(ctx, claimed, engine, engineKey)
}

// maybeStartCapacityWarming starts warming when the active index has grown
// past max_entries and no schema gap or valid warming already exists.
func (r *RotationManager) maybeStartCapacityWarming(ctx context.Context, item *pb.View, engine viewindex.ViewIndexEngine, engineKey string) (bool, error) {
	if r.cfg.MaxEntries <= 0 || item.GetActiveResult() == "" {
		return false, nil
	}
	stat, err := engine.Stat(ctx, item.GetActiveResult())
	if err != nil {
		return false, nil // active stats unavailable; do not force a rotation on error
	}
	if stat.EntryCount <= r.cfg.MaxEntries {
		return false, nil
	}
	return r.startWarming(ctx, item, engine, engineKey)
}

// progressWarming runs the backfill hook, checks readiness, and switches
// the pointer via CompleteViewBuild when ready. The old active index keeps
// serving reads until CompleteViewBuild succeeds (zero read gap); it is
// queued for grace removal only after the switch.
func (r *RotationManager) progressWarming(ctx context.Context, item *pb.View, engine viewindex.ViewIndexEngine, engineKey string) (bool, error) {
	indexID := item.GetBuildingResult()
	if indexID == "" {
		return false, nil
	}
	backfill := r.backfill
	if backfill == nil {
		backfill = stubBackfill
	}
	done, err := backfill(ctx, engine, item, indexID)
	if err != nil {
		return false, err
	}
	if !done {
		return false, nil
	}
	stat, err := engine.Stat(ctx, indexID)
	if err != nil {
		return false, err
	}
	ready, err := r.isReady(ctx, item, engine, stat)
	if err != nil {
		return false, err
	}
	if !ready {
		return false, nil
	}
	targetVersion := item.GetBuildingViewVersion()
	oldActive := item.GetActiveResult()
	if err := r.metadata.CompleteViewBuild(ctx, item.GetSpaceId(), item.GetViewId(), targetVersion, indexID); err != nil {
		return false, err
	}
	if oldActive != "" && oldActive != indexID {
		r.queueRemove(engineKey, oldActive)
	}
	return true, nil
}

// isReady implements the plan's switch readiness rule: prefer backfill
// scan-complete; min_ready_entries is a large-View guard only, so small
// Views (or first builds with no prior active) can switch as soon as
// backfill completes.
func (r *RotationManager) isReady(ctx context.Context, item *pb.View, engine viewindex.ViewIndexEngine, stat viewindex.ViewIndexStats) (bool, error) {
	if !stat.Exists {
		return false, nil
	}
	threshold := r.cfg.MinReadyEntries
	if activeResult := item.GetActiveResult(); activeResult != "" {
		activeStat, err := engine.Stat(ctx, activeResult)
		if err == nil && activeStat.Exists && activeStat.EntryCount < threshold {
			threshold = activeStat.EntryCount
		}
	} else {
		threshold = 0
	}
	if stat.EntryCount < threshold {
		return false, nil
	}
	if r.cfg.AllowedLag > 0 {
		if maxVersion, ok := parseVersionTime(stat.MaxVersion); ok {
			if r.now().UTC().Sub(maxVersion.UTC()) > r.cfg.AllowedLag {
				return false, nil
			}
		}
	}
	return true, nil
}

// buildingObsoleteOrStale reports whether the View's building pointer is
// failed, targets a version the View has since moved past, or has been
// building for too long (crash recovery).
func (r *RotationManager) buildingObsoleteOrStale(item *pb.View, now time.Time) bool {
	if item.GetBuildingResult() == "" {
		return false
	}
	switch item.GetBuildStatus() {
	case "failed":
		return true
	case "building":
		if item.GetBuildingViewVersion() != item.GetViewVersion() {
			return true
		}
		return r.buildingTooOld(item, now)
	default:
		return false
	}
}

func (r *RotationManager) buildingTooOld(item *pb.View, now time.Time) bool {
	startedAt := strings.TrimSpace(item.GetBuildStartedAt())
	if startedAt == "" {
		return false
	}
	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return false
	}
	threshold := r.cfg.StaleBuildingAfter
	if threshold <= 0 {
		threshold = defaultStaleBuildingAfter
	}
	return now.UTC().Sub(started.UTC()) >= threshold
}

// clearObsoleteBuilding clears a stale/failed/obsolete building pointer and
// queues its physical index for grace removal.
func (r *RotationManager) clearObsoleteBuilding(ctx context.Context, item *pb.View, engineKey string) (bool, error) {
	staleIndexID := item.GetBuildingResult()
	cleared := proto.Clone(item).(*pb.View)
	cleared.BuildingResult = ""
	cleared.BuildingViewVersion = 0
	cleared.BuildStatus = settledBuildStatus(item)
	cleared.BuildError = ""
	cleared.BuildFinishedAt = r.now().UTC().Format(time.RFC3339Nano)
	if _, err := r.metadata.UpsertView(ctx, cleared); err != nil {
		return false, err
	}
	r.queueRemove(engineKey, staleIndexID)
	return true, nil
}

func settledBuildStatus(item *pb.View) string {
	if item.GetActiveResult() != "" {
		return "active"
	}
	return "pending"
}

// engineFor resolves the ViewIndexEngine implementation for a View based on
// its engine name (defaulting to duckdb, matching TimeSeries Views).
func (r *RotationManager) engineFor(item *pb.View) (viewindex.ViewIndexEngine, string, bool) {
	key := strings.ToLower(strings.TrimSpace(item.GetEngine()))
	if key == "" {
		key = "duckdb"
	}
	engine, ok := r.engines[key]
	return engine, key, ok
}

func (r *RotationManager) viewColumns(ctx context.Context, item *pb.View) ([]*pb.ViewColumn, error) {
	columns, _, err := r.metadata.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), &pb.Page{Size: 10000})
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		columns = item.GetColumns()
	}
	return columns, nil
}

func (r *RotationManager) claimView(item *pb.View) bool {
	key := rotationClaimKey(item)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claims == nil {
		r.claims = make(map[string]bool)
	}
	if r.claims[key] {
		return false
	}
	r.claims[key] = true
	return true
}

func (r *RotationManager) releaseView(item *pb.View) {
	key := rotationClaimKey(item)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.claims, key)
}

func rotationClaimKey(item *pb.View) string {
	return item.GetSpaceId() + "|" + item.GetViewId()
}

// queueRemove schedules a physical index for removal after remove_grace.
// The removal itself happens on a later sweep once it is no longer
// referenced by any View's active_result/building_result.
func (r *RotationManager) queueRemove(engineKey string, indexID string) {
	if indexID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.removals == nil {
		r.removals = make(map[string]time.Time)
	}
	key := engineKey + "|" + indexID
	if _, exists := r.removals[key]; exists {
		return
	}
	r.removals[key] = r.now().Add(r.cfg.RemoveGrace)
}

// sweep processes the grace-removal queue and, when the caller rotated the
// full view set (spaceID == ""), discovers and queues physical indexes that
// are not referenced by any View (non a/b leftovers, dropped building).
func (r *RotationManager) sweep(ctx context.Context, spaceID string, views []*pb.View) {
	referenced := make(map[string]map[string]bool)
	for _, item := range views {
		_, engineKey, ok := r.engineFor(item)
		if !ok {
			continue
		}
		set := referenced[engineKey]
		if set == nil {
			set = make(map[string]bool)
			referenced[engineKey] = set
		}
		if item.GetActiveResult() != "" {
			set[item.GetActiveResult()] = true
		}
		if item.GetBuildingResult() != "" {
			set[item.GetBuildingResult()] = true
		}
	}
	r.processDueRemovals(ctx, referenced)
	if spaceID == "" {
		r.sweepUnreferenced(ctx, referenced)
	}
}

func (r *RotationManager) processDueRemovals(ctx context.Context, referenced map[string]map[string]bool) {
	now := r.now()
	r.mu.Lock()
	due := make([]string, 0, len(r.removals))
	for key, at := range r.removals {
		if !now.Before(at) {
			due = append(due, key)
		}
	}
	r.mu.Unlock()
	for _, key := range due {
		engineKey, indexID, ok := splitRemovalKey(key)
		if !ok {
			r.mu.Lock()
			delete(r.removals, key)
			r.mu.Unlock()
			continue
		}
		if referenced[engineKey][indexID] {
			r.mu.Lock()
			delete(r.removals, key)
			r.mu.Unlock()
			continue
		}
		engine := r.engines[engineKey]
		if engine == nil {
			r.mu.Lock()
			delete(r.removals, key)
			r.mu.Unlock()
			continue
		}
		if err := engine.Remove(ctx, indexID); err != nil {
			continue // keep queued; retry on a later rotate pass
		}
		r.mu.Lock()
		delete(r.removals, key)
		r.mu.Unlock()
	}
}

// viewIndexLister is an optional capability implemented by engines that can
// list their physical index IDs (DuckDB's ViewStore.ListResultTables). It is
// used only to discover orphaned physical indexes for the sweep step.
type viewIndexLister interface {
	ListResultTables(ctx context.Context) ([]string, error)
}

func (r *RotationManager) sweepUnreferenced(ctx context.Context, referenced map[string]map[string]bool) {
	for engineKey, engine := range r.engines {
		lister, ok := engine.(viewIndexLister)
		if !ok {
			continue
		}
		tables, err := lister.ListResultTables(ctx)
		if err != nil {
			continue
		}
		for _, table := range tables {
			if referenced[engineKey][table] {
				continue
			}
			r.queueRemove(engineKey, table)
		}
	}
}

func splitRemovalKey(key string) (engineKey string, indexID string, ok bool) {
	idx := strings.Index(key, "|")
	if idx < 0 {
		return "", "", false
	}
	return key[:idx], key[idx+1:], true
}

func (r *RotationManager) listViewsForRotate(ctx context.Context, spaceID string) ([]*pb.View, error) {
	if spaceID != "" {
		return r.listActiveViews(ctx, spaceID)
	}
	const pageSize = uint32(1000)
	var out []*pb.View
	for pageNo := uint32(1); ; pageNo++ {
		spaces, page, err := r.metadata.ListSpaces(ctx, "", &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		for _, space := range spaces {
			views, err := r.listActiveViews(ctx, space.GetSpaceId())
			if err != nil {
				return nil, err
			}
			out = append(out, views...)
		}
		if page == nil || !page.GetHasMore() {
			return out, nil
		}
	}
}

func (r *RotationManager) listActiveViews(ctx context.Context, spaceID string) ([]*pb.View, error) {
	const pageSize = uint32(1000)
	var out []*pb.View
	for pageNo := uint32(1); ; pageNo++ {
		views, page, err := r.metadata.ListViews(ctx, spaceID, "", "active", &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, views...)
		if page == nil || !page.GetHasMore() {
			return out, nil
		}
	}
}

// parseVersionTime tries to parse a version/timestamp string using the
// timestamp layouts used across TimeSeries data_time and Record version
// fields. It reports ok=false for non-timestamp Record versions, in which
// case callers must skip lag-based readiness checks.
func parseVersionTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
