package view

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"trpc.group/trpc-go/trpc-go/client"

	"trpc.group/trpc-go/trpc-go/log"
)

// MetadataClient 是 storage-view 读数据集声明用的元数据接口：每个数据集恰好有一个 View，列等于数据集启用的列；
// 已建好的索引状态只在索引文件里，不写回元数据。
type MetadataClient interface {
	ListDatasets(context.Context, *pb.ListDatasetsReq, ...client.Option) (*pb.ListDatasetsRsp, error)
	ListDatasetColumns(context.Context, *pb.ListDatasetColumnsReq, ...client.Option) (*pb.ListDatasetColumnsRsp, error)
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
	// BackfillPageSize 为 0 时使用 storageconfig.DefaultViewBackfillPageSize（2000）。
	BackfillPageSize        uint32
	BackfillRequestInterval time.Duration
}

// initialViewRevision 是索引建成时的修订号，之后每次加列加 1。
const initialViewRevision uint64 = 1

// metadataPageSize 是维护循环列出数据集与列时每页的条数。
const metadataPageSize = 500

// Coverage scans are intentionally decoupled from the cheap physical
// contract check. A large DuckDB View must not be COUNT/MIN/MAX-scanned on
// every maintenance tick.
const coverageRefreshInterval = 5 * time.Minute
const coverageStatTimeout = 5 * time.Second

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
		opts.BackfillPageSize = storageconfig.DefaultViewBackfillPageSize
	}
	s.SetSeriesBars(opts.Bars)
	s.mu.Lock()
	s.maintenance = opts
	s.mu.Unlock()
	s.setMetadataClient(opts.Metadata)
	return opts, nil
}

// observeViewFileSize 更新索引文件是否超过告警线：只告警，不动数据。
func (s *Service) observeViewFileSize(runtime *viewRuntime, stats viewindex.ViewIndexStats, opts MaintenanceOptions) {
	exceeded := stats.Exists && opts.MaxViewFileBytes > 0 && stats.PhysicalBytes >= uint64(opts.MaxViewFileBytes)
	wasExceeded := runtime.fileOverLimit.Swap(exceeded)
	s.reportFilesOverLimit()
	if exceeded && !wasExceeded {
		log.Warnf("storage view 索引文件超过告警线 space=%s dataset=%s index=%s 文件=%d 告警线=%d", runtime.ref.spaceID, runtime.ref.datasetID, runtime.indexID, stats.PhysicalBytes, opts.MaxViewFileBytes)
	}
}

// reportFilesOverLimit 按当前挂着的 View 上报索引文件超过告警线的清单（空间/数据集）；摘掉的 View 随运行时一起消失。
func (s *Service) reportFilesOverLimit() {
	s.mu.RLock()
	over := make([]string, 0)
	for ref, runtime := range s.views {
		if runtime.fileOverLimit.Load() {
			over = append(over, ref.spaceID+"/"+ref.datasetID)
		}
	}
	s.mu.RUnlock()
	sort.Strings(over)
	s.metrics.SetViewFilesOverLimit(over)
}

// listViewSpecs 列出元数据里全部数据集（含停用的）的 View 声明，按维护先后排好。先列数据集再列列：
// 数据集与它建时带的列在同一事务里写入，之后列出的列一定包含已列出数据集建时的列。分页信息不完整时返回错误，不能断定已经列全。
func listViewSpecs(ctx context.Context, metadata MetadataClient) ([]*viewSpec, error) {
	var datasets []*pb.Dataset
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := metadata.ListDatasets(ctx, &pb.ListDatasetsReq{Page: &pb.Page{Page: pageNo, Size: metadataPageSize}})
		if err != nil {
			return nil, err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		datasets = append(datasets, rsp.GetDatasets()...)
		done, err := lastPage(rsp.GetPageResult(), len(rsp.GetDatasets()), "数据集")
		if err != nil {
			return nil, err
		}
		if done {
			break
		}
	}
	columns := make(map[datasetRef][]*pb.DatasetColumn)
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := metadata.ListDatasetColumns(ctx, &pb.ListDatasetColumnsReq{Page: &pb.Page{Page: pageNo, Size: metadataPageSize}})
		if err != nil {
			return nil, err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, column := range rsp.GetColumns() {
			ref := datasetRef{spaceID: column.GetSpaceId(), datasetID: column.GetDatasetId()}
			columns[ref] = append(columns[ref], column)
		}
		done, err := lastPage(rsp.GetPageResult(), len(rsp.GetColumns()), "数据集列")
		if err != nil {
			return nil, err
		}
		if done {
			break
		}
	}
	specs := make([]*viewSpec, 0, len(datasets))
	for _, dataset := range datasets {
		if dataset.GetSpaceId() == "" || dataset.GetDatasetId() == "" {
			continue
		}
		specs = append(specs, newViewSpec(dataset, columns[datasetRef{spaceID: dataset.GetSpaceId(), datasetID: dataset.GetDatasetId()}]))
	}
	sortSpecsForMaintenance(specs)
	return specs, nil
}

// lastPage 报告这一页是不是最后一页；分页信息缺失或自相矛盾时返回错误。
func lastPage(page *pb.PageResult, items int, what string) (bool, error) {
	if page == nil {
		return false, fmt.Errorf("列出%s没有返回分页信息", what)
	}
	if page.GetHasMore() && items == 0 {
		return false, fmt.Errorf("列出%s时这一页为空却说还有下一页", what)
	}
	return !page.GetHasMore(), nil
}

// RestoreActiveViews 在启动时清掉孤立的索引文件，再挂接已经建好的索引文件；不建新索引，可以在连 EventBus 之前运行，不拖住实时流。
// 没有文件、没建完（没记下跟随进度）或不属于它（删除后同 ID 重建）的数据集不挂接，由维护流程重新建。
func (s *Service) RestoreActiveViews(ctx context.Context, opts MaintenanceOptions) error {
	if opts.Metadata == nil {
		return errors.New("metadata client is required")
	}
	s.mu.Lock()
	// 断档重建沿用维护参数（回填页大小、请求间隔、保留根数）：跟随器可能先于维护循环启动，维护也可能被禁用。
	s.maintenance = opts
	s.mu.Unlock()
	s.setMetadataClient(opts.Metadata)
	specs, err := listViewSpecs(ctx, opts.Metadata)
	if err != nil {
		return err
	}
	// 先清掉没有对应数据集的索引文件，再挂接。
	s.removeOrphanIndexes(ctx, specs)
	for _, spec := range specs {
		if spec.Status != "active" {
			continue
		}
		engine := s.engines[spec.engineName()]
		if engine == nil {
			return fmt.Errorf("view engine %q is unavailable", spec.engineName())
		}
		stats, err := restoreIndexStats(ctx, engine, viewindex.IndexID(spec.SpaceID, spec.DatasetID))
		if err != nil {
			// 一个索引读不出来不能让整个进程起不来：这个数据集不挂接，由维护流程处理，其他照常恢复。
			log.Errorf("storage view 启动时读取索引失败，跳过挂接 space=%s dataset=%s：%v", spec.SpaceID, spec.DatasetID, err)
			continue
		}
		if !indexBuiltFor(spec, stats) {
			continue
		}
		runtime, err := s.ensureRuntime(ctx, spec)
		if err != nil {
			return err
		}
		s.attachActiveView(runtime, spec, stats)
	}
	return nil
}

func restoreIndexStats(ctx context.Context, engine viewindex.Engine, indexID string) (viewindex.ViewIndexStats, error) {
	if reader, ok := engine.(viewindex.MetadataStatReader); ok {
		return reader.StatMetadata(ctx, indexID)
	}
	return engine.Stat(ctx, indexID)
}

// indexOwnedBy 报告索引文件是否属于这个数据集：文件存在、数据集 ID 与数据集的创建时间都和元数据一致。
// 索引标识只由空间与数据集决定，删除后按同样 ID 重建的数据集靠创建时间认出旧索引，不会挂上旧索引、读到旧行。
func indexOwnedBy(spec *viewSpec, stats viewindex.ViewIndexStats) bool {
	return stats.Exists && stats.DatasetID == spec.DatasetID && stats.DatasetCreatedAt == spec.CreatedAt
}

// indexBuiltFor 报告索引文件是否是这个数据集建好的索引：属于这个数据集，且记下了跟随进度
// （建索引的最后一步才记，没记的是半成品）。
func indexBuiltFor(spec *viewSpec, stats viewindex.ViewIndexStats) bool {
	return indexOwnedBy(spec, stats) && stats.Follow.Cursor.StoreID != ""
}

func (s *Service) maintainOnce(ctx context.Context, opts MaintenanceOptions) error {
	specs, err := listViewSpecs(ctx, opts.Metadata)
	if err != nil {
		return err
	}
	var firstErr error
	active := make(map[observability.ViewKey]struct{})
	existing := make(map[datasetRef]struct{}, len(specs))
	for _, spec := range specs {
		existing[spec.ref()] = struct{}{}
		if spec.Status == "active" {
			active[observability.ViewKey{SpaceID: spec.SpaceID, DatasetID: spec.DatasetID}] = struct{}{}
		}
	}
	// 冷启动时先建业务 K 线 View：所有 View 这一轮都会处理，顺序只避免大的派生 View 先占住 Primary。
	for _, spec := range specs {
		if spec.Status != "active" {
			continue
		}
		// 停服时不再处理剩下的 View，也不为每个 View 各记一条“上下文已取消”。
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.maintainView(ctx, opts, spec); err != nil {
			if ctx.Err() == nil {
				log.Errorf("storage view maintenance %s/%s failed: %v", spec.SpaceID, spec.DatasetID, err)
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	// 完整列出了数据集：停用或删除的数据集不再导出输出水位等指标序列，否则它们会一直带着冻结的水位上报，
	// Monitor 永远认为它迟到。
	s.metrics.RetainViews(active)
	s.detachInactiveViews(ctx, active, existing)
	// 停用期间被删除的数据集没有运行时可摘，它的索引文件在这里按孤立文件删掉。
	s.removeOrphanIndexes(ctx, specs)
	s.reportFilesOverLimit()
	return firstErr
}

// detachInactiveViews 摘掉不再活动的 View：数据集停用时停跟随、不可查，索引文件保留，重新启用后挂回、不重建；
// 数据集已删除时同时删除索引文件，GetViewState 随之返回 VIEW_NOT_FOUND。
func (s *Service) detachInactiveViews(ctx context.Context, active map[observability.ViewKey]struct{}, existing map[datasetRef]struct{}) {
	s.mu.RLock()
	var inactive []*viewRuntime
	for ref, runtime := range s.views {
		if _, ok := active[observability.ViewKey{SpaceID: ref.spaceID, DatasetID: ref.datasetID}]; !ok {
			inactive = append(inactive, runtime)
		}
	}
	s.mu.RUnlock()
	for _, runtime := range inactive {
		_, stillExists := existing[runtime.ref]
		s.detachView(ctx, runtime, !stillExists)
		if stillExists {
			log.Infof("storage view 数据集已停用，View 摘掉、索引保留 space=%s dataset=%s", runtime.ref.spaceID, runtime.ref.datasetID)
		} else {
			log.Infof("storage view 数据集已删除，View 摘掉并删除索引 space=%s dataset=%s", runtime.ref.spaceID, runtime.ref.datasetID)
		}
	}
}

// sortSpecsForMaintenance 按数据集的 maintenance_priority（越小越先）排维护顺序，相同时按空间与数据集 ID。
func sortSpecsForMaintenance(specs []*viewSpec) {
	sort.SliceStable(specs, func(i, j int) bool {
		left, right := specs[i], specs[j]
		if left.MaintenancePriority != right.MaintenancePriority {
			return left.MaintenancePriority < right.MaintenancePriority
		}
		if left.SpaceID != right.SpaceID {
			return left.SpaceID < right.SpaceID
		}
		return left.DatasetID < right.DatasetID
	})
}

// maintainView 处理一个活动数据集的 View：没有建好的索引时建索引；有索引时挂接并刷新覆盖统计。加列由每分钟的定时任务处理。
func (s *Service) maintainView(ctx context.Context, opts MaintenanceOptions, spec *viewSpec) error {
	runtime, err := s.ensureRuntime(ctx, spec)
	if err != nil {
		return err
	}
	if runtime.needsRebuild() {
		// 原地重建由跟随器做（期间文件可能暂时不存在），不在这里另建；跟随器被停过时重新起它，接着重建。
		runtime.setSpec(spec)
		s.ensureFollower(runtime.ref)
		return nil
	}
	isActive := runtime.queryState().active()
	// 维护每轮都会跑：DuckDB 的完整 Stat 要对全表做 COUNT/MIN/MAX，这里只做元数据级的检查，文件大小也在其中。
	stats, metadataOnly, err := maintainIndexStats(ctx, runtime.engine, runtime.indexID)
	if err != nil {
		return err
	}
	// 本进程刚建好、还没有跟随器记进度的索引（没起跟随时）同样算建好。
	if !indexBuiltFor(spec, stats) && !(isActive && indexOwnedBy(spec, stats)) {
		return s.buildInitialIndex(ctx, opts, runtime, spec)
	}
	s.observeViewFileSize(runtime, stats, opts)
	// 元数据级统计不含覆盖字段：保留上一次完整统计，不能用空值覆盖，否则 TotalMode=NONE 的查询会丢掉完整性信息。
	if metadataOnly {
		if cached, ok := cachedActiveIndexStats(runtime, runtime.indexID); ok {
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
			fullStats, fullErr := runtime.engine.Stat(coverageCtx, runtime.indexID)
			cancel()
			markCoverageRefresh(runtime)
			if fullErr == nil && fullStats.Exists {
				stats = fullStats
				metadataOnly = false
			} else if fullErr != nil {
				log.Warnf("storage view coverage refresh deferred space=%s dataset=%s: %v", spec.SpaceID, spec.DatasetID, fullErr)
			}
		}
	}
	s.attachActiveView(runtime, spec, stats)
	if !metadataOnly || stats.IndexedFrom != "" || stats.IndexedTo != "" || stats.EntryCount != 0 {
		s.cacheActiveIndexStats(runtime, runtime.indexID, stats)
	}
	return nil
}

// viewBars 返回 View 每个序列保留的根数：时序数据集取策略里的根数，记录型数据集保留全部、返回 0。
func viewBars(spec *viewSpec, opts MaintenanceOptions) uint64 {
	if !spec.timeSeries() {
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
	last := runtime.statsRefreshedAt.Load()
	return last == 0 || time.Since(time.Unix(0, last)) >= coverageRefreshInterval
}

func markCoverageRefresh(runtime *viewRuntime) {
	if runtime != nil {
		runtime.statsRefreshedAt.Store(time.Now().UnixNano())
	}
}

// observeActivatedViewWatermark 按刚建成的索引刷新新鲜度指标。查询有界且尽力而为：指标探测失败不影响建索引。
func (s *Service) observeActivatedViewWatermark(runtime *viewRuntime, spec *viewSpec) {
	if s == nil || s.metrics == nil || !spec.timeSeries() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, _, err := runtime.engine.Query(ctx, runtime.indexID, viewindex.QuerySpec{
		Sorts: []*pb.SortSpec{{FieldName: "data_time", Desc: true}},
		Order: pb.SortOrder_SORT_ORDER_DESC, Limit: 5000, TotalMode: pb.TotalMode_NONE,
	})
	if err != nil {
		log.Warnf("storage view activation metric probe failed space=%s dataset=%s index=%s: %v", spec.SpaceID, spec.DatasetID, runtime.indexID, err)
		return
	}
	keys := make([]*pb.RowFieldUpsert, 0, len(rows))
	for _, row := range rows {
		if row != nil && row.GetKey() != nil {
			keys = append(keys, &pb.RowFieldUpsert{Key: row.GetKey()})
		}
	}
	s.observeViewWatermark(spec, keys)
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

// columnNames 按顺序列出索引各列的列名（去重）：回读 Primary 也按列名取值。
func columnNames(columns []*viewindex.Column) []string {
	seen := make(map[string]struct{}, len(columns))
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		name := strings.TrimSpace(column.GetColumnName())
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}
