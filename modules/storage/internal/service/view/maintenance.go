package view

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/serviceauth"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"trpc.group/trpc-go/trpc-go/client"

	"trpc.group/trpc-go/trpc-go/log"
)

// MetadataClient 是 storage-view 读 View 声明用的元数据接口：View 的列由数据集列推出，随 View 一起返回；
// 已建好的索引状态只在索引文件里，不写回元数据。
type MetadataClient interface {
	ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error)
}

type FieldReader interface {
	ReadRows(context.Context, *pb.PrimaryReadRowsReq, ...client.Option) (*pb.PrimaryReadRowsRsp, error)
}

// TimeSeriesRangeReader 读取 Primary 的时序事实（PrimaryStore 对所有调用方都由 Pebble 回答），首次建索引用它按页取每个序列最近 N 根。
type TimeSeriesRangeReader interface {
	ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq, ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error)
}

type MaintenanceOptions struct {
	Metadata     MetadataClient
	PrimaryRange TimeSeriesRangeReader
	Interval     time.Duration
	// MaxViewFileBytes 是索引文件大小的告警线：超过只告警，不动数据。
	MaxViewFileBytes int64
	// Bars 是时序 View 每个序列保留的最新根数；Primary 不够时有多少建多少。
	Bars uint64
	// BackfillPageSize 和 BackfillRequestInterval 限制首次建索引回填 Primary 的请求突发。
	// 0 表示不限制，线上由配置给默认值。
	BackfillPageSize        uint32
	BackfillRequestInterval time.Duration
}

// viewBackfillBatchSize 是没有配置 backfill_page_size 时回填每页读取的行数。
// Primary 历史读取一页就带回字段值，Pebble 还会按单页值数上限截断，宽表的实际页更小；
// 线上用 backfill_page_size 把页调小，减轻回填对 Pebble 点读的突发压力。索引写入另按 256 行分批。
const viewBackfillBatchSize = 10000

// initialViewRevision 是索引建成时的修订号，之后每次加列加 1。
const initialViewRevision uint64 = 1

// Coverage scans are intentionally decoupled from the cheap physical
// contract check. A large DuckDB View must not be COUNT/MIN/MAX-scanned on
// every maintenance tick.
const coverageRefreshInterval = 5 * time.Minute
const coverageStatTimeout = 5 * time.Second

func (s *Service) StartViewMaintainer(ctx context.Context, opts MaintenanceOptions) (func(), error) {
	var err error
	opts, err = s.normalizeMaintenanceOptions(opts)
	if err != nil {
		return nil, err
	}
	if err := s.maintainOnce(ctx, opts); err != nil {
		return nil, err
	}
	return s.startViewMaintenanceLoop(ctx, opts), nil
}

// StartViewMaintainerAsync 启动周期维护，不让进程启动等待首次建索引的大批量回填。
func (s *Service) StartViewMaintainerAsync(ctx context.Context, opts MaintenanceOptions) (func(), error) {
	var err error
	opts, err = s.normalizeMaintenanceOptions(opts)
	if err != nil {
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		run := func() {
			// 停服时上下文被取消，这一轮没做完不算错误；逐个 View 的失败已经各记了一条，这里的汇总只按警告记。
			if err := s.maintainOnce(loopCtx, opts); err != nil && loopCtx.Err() == nil {
				log.Warnf("storage view maintenance failed: %v", err)
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
	if opts.BackfillPageSize == 0 {
		opts.BackfillPageSize = viewBackfillBatchSize
	}
	s.SetSeriesBars(opts.Bars)
	s.mu.Lock()
	s.maintenance = opts
	s.mu.Unlock()
	s.setMetadataClient(opts.Metadata)
	return opts, nil
}

// observeViewFileSize 更新超过文件大小告警线的 View：只告警，不动数据。
func (s *Service) observeViewFileSize(view *pb.View, indexID string, stats viewindex.ViewIndexStats, opts MaintenanceOptions) {
	if view == nil {
		return
	}
	ref := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	exceeded := stats.Exists && opts.MaxViewFileBytes > 0 && stats.PhysicalBytes >= uint64(opts.MaxViewFileBytes)
	s.fileOverLimitMu.Lock()
	if s.fileOverLimit == nil {
		s.fileOverLimit = make(map[viewRef]struct{})
	}
	_, wasExceeded := s.fileOverLimit[ref]
	if exceeded {
		s.fileOverLimit[ref] = struct{}{}
	} else {
		delete(s.fileOverLimit, ref)
	}
	count := len(s.fileOverLimit)
	s.fileOverLimitMu.Unlock()
	s.metrics.SetViewFileOverLimitCount(int64(count))
	if exceeded && !wasExceeded {
		log.Warnf("storage view 索引文件超过告警线 space=%s view=%s index=%s 文件=%d 告警线=%d", view.GetSpaceId(), view.GetViewId(), indexID, stats.PhysicalBytes, opts.MaxViewFileBytes)
	}
}

// retainFileOverLimit 去掉已删除或停用的 View。
func (s *Service) retainFileOverLimit(active map[observability.ViewKey]struct{}) {
	s.fileOverLimitMu.Lock()
	for ref := range s.fileOverLimit {
		if _, ok := active[observability.ViewKey{SpaceID: ref.spaceID, ViewID: ref.viewID}]; !ok {
			delete(s.fileOverLimit, ref)
		}
	}
	count := len(s.fileOverLimit)
	s.fileOverLimitMu.Unlock()
	s.metrics.SetViewFileOverLimitCount(int64(count))
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
				if err := s.maintainOnce(loopCtx, opts); err != nil && loopCtx.Err() == nil {
					log.Warnf("storage view maintenance failed: %v", err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

// RestoreActiveViews 只挂接已经建好的索引文件，不建新索引，可以在连 EventBus 之前运行，不拖住实时流。
// 没有文件、没建完（没记下跟随进度）或属于另一个数据集（删除后同名重建）的 View 不挂接，由维护流程重新建。
func (s *Service) RestoreActiveViews(ctx context.Context, opts MaintenanceOptions) error {
	if opts.Metadata == nil {
		return errors.New("metadata client is required")
	}
	s.mu.Lock()
	// 断档重建沿用维护参数（回填页大小、请求间隔、保留根数）：跟随器可能先于维护循环启动，维护也可能被禁用。
	s.maintenance = opts
	s.mu.Unlock()
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
		sortViewsForMaintenance(rsp.GetViews())
		for _, view := range rsp.GetViews() {
			if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" {
				continue
			}
			engineName := strings.ToLower(strings.TrimSpace(view.GetEngine()))
			engine := s.engines[engineName]
			if engine == nil {
				return fmt.Errorf("view engine %q is unavailable", engineName)
			}
			stats, err := restoreIndexStats(ctx, engine, viewindex.ViewIndexID(view.GetSpaceId(), view.GetViewId()))
			if err != nil {
				return err
			}
			if !indexBuiltFor(view, stats) {
				continue
			}
			if err := s.AttachActiveView(ctx, view, stats); err != nil {
				return err
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

// indexBuiltFor 报告索引文件是否是这个 View 建好的索引：文件存在、属于 View 当前的数据集、记下了跟随进度
// （建索引的最后一步才记，没记的是半成品）。
func indexBuiltFor(view *pb.View, stats viewindex.ViewIndexStats) bool {
	return stats.Exists && stats.DatasetID == view.GetDatasetId() && stats.Follow.Cursor.StoreID != ""
}

func isPrimaryHistoryIndexNotReady(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "primary history index is still being materialized")
}

func (s *Service) maintainOnce(ctx context.Context, opts MaintenanceOptions) error {
	auth := s.internalAuth()
	var firstErr error
	activeViews := make(map[observability.ViewKey]struct{})
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := opts.Metadata.ListViews(ctx, &pb.ListViewsReq{AuthInfo: auth, Status: "active", Page: &pb.Page{Page: pageNo, Size: 100}})
		if err != nil {
			return err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return err
		}
		for _, view := range rsp.GetViews() {
			if view != nil && view.GetSpaceId() != "" && view.GetViewId() != "" {
				activeViews[observability.ViewKey{SpaceID: view.GetSpaceId(), ViewID: view.GetViewId()}] = struct{}{}
			}
		}
		// 冷启动时先建业务 K 线 View：所有 View 这一轮都会处理，顺序只避免大的派生 View 先占住 Primary。
		sortViewsForMaintenance(rsp.GetViews())
		for _, view := range rsp.GetViews() {
			// 停服时不再处理剩下的 View，也不为每个 View 各记一条“上下文已取消”。
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.maintainView(ctx, opts, auth, view); err != nil {
				if view != nil && ctx.Err() == nil {
					log.Errorf("storage view maintenance %s/%s failed: %v", view.GetSpaceId(), view.GetViewId(), err)
				}
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			s.retainFileOverLimit(activeViews)
			// 完整列出了活动 View：删除或停用的 View 不再导出输出水位等指标序列，否则它们会一直带着冻结的水位上报，
			// Monitor 永远认为它迟到。
			s.metrics.RetainViews(activeViews)
			// 已删除或停用的 View 停掉跟随器。
			s.followManager().Retain(func(spaceID, viewID string) bool {
				_, ok := activeViews[observability.ViewKey{SpaceID: spaceID, ViewID: viewID}]
				return ok
			})
			return firstErr
		}
	}
}

// defaultViewMaintenancePriority 是 View 没有声明 maintenance_priority 时的维护顺序。
const defaultViewMaintenancePriority = 50

// viewMaintenancePriority 读取 View 属性 maintenance_priority（整数，越小越先维护）；缺失或非法取默认值。
func viewMaintenancePriority(view *pb.View) int {
	if view == nil {
		return 100
	}
	priority, err := strconv.Atoi(strings.TrimSpace(view.GetAttributes()["maintenance_priority"]))
	if err != nil {
		return defaultViewMaintenancePriority
	}
	return priority
}

func sortViewsForMaintenance(views []*pb.View) {
	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]
		if viewMaintenancePriority(left) != viewMaintenancePriority(right) {
			return viewMaintenancePriority(left) < viewMaintenancePriority(right)
		}
		if left.GetSpaceId() != right.GetSpaceId() {
			return left.GetSpaceId() < right.GetSpaceId()
		}
		return left.GetViewId() < right.GetViewId()
	})
}

// maintainView 处理一个 View：没有建好的索引时建索引；有索引时挂接并刷新覆盖统计。加列由每分钟的定时任务处理。
func (s *Service) maintainView(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" {
		return nil
	}
	if opts.Metadata != nil {
		s.setMetadataClient(opts.Metadata)
	}
	active, runtime := s.activeIndex(view.GetSpaceId(), view.GetViewId())
	if runtime != nil && runtime.needsRebuild() {
		// 原地重建由跟随器做（期间文件可能暂时不存在），不在这里另建；跟随器被停过（View 停用后再启用）时重新起它，接着重建。
		s.ensureFollower(viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()})
		return nil
	}
	engineName := strings.ToLower(strings.TrimSpace(view.GetEngine()))
	engine := s.engines[engineName]
	if engine == nil {
		return fmt.Errorf("view engine %q is unavailable", engineName)
	}
	indexID := viewindex.ViewIndexID(view.GetSpaceId(), view.GetViewId())
	// 维护每轮都会跑：DuckDB 的完整 Stat 要对全表做 COUNT/MIN/MAX，这里只做元数据级的检查，文件大小也在其中。
	stats, metadataOnly, err := maintainIndexStats(ctx, engine, indexID)
	if err != nil {
		return err
	}
	// 本进程刚建好、还没有跟随器记进度的索引（没起跟随时）同样算建好。
	if !indexBuiltFor(view, stats) && !(active == indexID && stats.Exists && stats.DatasetID == view.GetDatasetId()) {
		return s.buildInitialIndex(ctx, opts, auth, view)
	}
	s.observeViewFileSize(view, indexID, stats, opts)
	// 元数据级统计不含覆盖字段：保留上一次完整统计，不能用空值覆盖，否则 TotalMode=NONE 的查询会丢掉完整性信息。
	if metadataOnly {
		if cached, ok := cachedActiveIndexStats(runtime, indexID); ok {
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
			fullStats, fullErr := engine.Stat(coverageCtx, indexID)
			cancel()
			markCoverageRefresh(runtime)
			if fullErr == nil && fullStats.Exists {
				stats = fullStats
				metadataOnly = false
			} else if fullErr != nil {
				log.Warnf("storage view coverage refresh deferred space=%s view=%s: %v", view.GetSpaceId(), view.GetViewId(), fullErr)
			}
		}
	}
	if err := s.AttachActiveView(ctx, view, stats); err != nil {
		return err
	}
	_, runtime = s.activeIndex(view.GetSpaceId(), view.GetViewId())
	if !metadataOnly || stats.IndexedFrom != "" || stats.IndexedTo != "" || stats.EntryCount != 0 {
		s.cacheActiveIndexStats(runtime, indexID, stats)
	}
	return nil
}

// viewBars returns the number of bars a View keeps per series: the policy's
// bars for a time-series View, zero for a record View that keeps every row.
func viewBars(view *pb.View, opts MaintenanceOptions) uint64 {
	if view.GetFreq() == "" {
		return 0
	}
	if opts.Bars > 0 {
		return opts.Bars
	}
	return storagepolicy.DefaultViewBars
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

// observeActivatedViewWatermark 按刚建成的索引刷新新鲜度指标。查询有界且尽力而为：指标探测失败不影响建索引。
func (s *Service) observeActivatedViewWatermark(view *pb.View, indexID string) {
	if s == nil || s.metrics == nil || view == nil || strings.TrimSpace(indexID) == "" {
		return
	}
	if view.GetFreq() == "" || strings.TrimSpace(view.GetDatasetId()) == "" {
		return
	}
	engine, err := s.engineFor(indexID)
	if err != nil {
		log.Infof("storage view activation metric probe skipped space=%s view=%s index=%s: %v", view.GetSpaceId(), view.GetViewId(), indexID, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, _, err := engine.Query(ctx, indexID, viewindex.QuerySpec{
		Sorts: []*pb.SortSpec{{FieldName: "data_time", Desc: true}},
		Order: pb.SortOrder_SORT_ORDER_DESC, Limit: 5000, TotalMode: pb.TotalMode_NONE,
	})
	if err != nil {
		log.Warnf("storage view activation metric probe failed space=%s view=%s index=%s: %v", view.GetSpaceId(), view.GetViewId(), indexID, err)
		return
	}
	keys := make([]*pb.RowFieldUpsert, 0, len(rows))
	for _, row := range rows {
		if row != nil && row.GetKey() != nil {
			keys = append(keys, &pb.RowFieldUpsert{Key: row.GetKey()})
		}
	}
	s.observeViewWatermark(indexID, view.GetDatasetId(), keys, true)
}

func (s *Service) internalAuth() *pb.AuthInfo {
	const appID = "storage-view"
	return &pb.AuthInfo{AppId: appID, AppKey: serviceauth.Key(s.authSecret, appID)}
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
