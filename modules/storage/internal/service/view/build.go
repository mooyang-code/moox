package view

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"

	"trpc.group/trpc-go/trpc-go/log"
)

type backfillRequestLimiter struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

// primaryHistoryPageReadTimeout 限制一页 Primary 读取的耗时；超时则整次建索引失败并重试，不静默跳过。
const primaryHistoryPageReadTimeout = 60 * time.Second

// maxLatestPerSeries 是 Primary 单次读取允许的每序列最近 N 根上限。
const maxLatestPerSeries = 10000

func newBackfillRequestLimiter(interval time.Duration) *backfillRequestLimiter {
	return &backfillRequestLimiter{interval: interval}
}

func (l *backfillRequestLimiter) wait(ctx context.Context) error {
	if l == nil || l.interval <= 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	wait := time.Until(l.next)
	if wait < 0 {
		wait = 0
	}
	l.next = now.Add(wait + l.interval)
	l.mu.Unlock()
	if wait == 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// buildIndex 是唯一的建索引流程，首次建和断档后的重建都走它：先取变更日志头部 H_start，把索引清成空的（此后 View 不可读，
// 读取方等待），从 Primary 读存量，建成时再取一次头部 H_done 与 H_start 一起记进索引（记下跟随进度的索引才算建好），
// 最后转为活动索引。跟随器从 H_start 接着应用（重放的行是幂等写入），(H_start, H_done] 之间的完成标记只推进游标、不发布。
// schemaOf 在运行时锁内给出要建的列契约：首次建用元数据里的列，重建沿用已有索引的契约。
// 调用方持有 buildMu，并决定失败后怎么办（首次建丢弃半成品，重建留着由跟随器接着重建）。
func (s *Service) buildIndex(ctx context.Context, opts MaintenanceOptions, runtime *viewRuntime, spec *viewSpec, targetPhase viewPhase, schemaOf func(*viewRuntime) (viewindex.ViewIndexSchema, error)) (viewindex.FollowState, error) {
	manager := s.followManager()
	var state viewindex.FollowState
	if manager != nil {
		start, err := manager.Head(ctx, spec.SpaceID, spec.DatasetID)
		if err != nil {
			return viewindex.FollowState{}, err
		}
		state.Cursor = start
	}
	schema, err := s.prepareIndex(ctx, runtime, targetPhase, schemaOf)
	if err != nil {
		return viewindex.FollowState{}, fmt.Errorf("准备 View 索引 %q：%w", runtime.indexID, err)
	}
	runtime.bumpRevision()
	if err := s.backfillInitialIndex(ctx, runtime, spec, schema, opts, viewBars(spec, opts)); err != nil {
		return viewindex.FollowState{}, err
	}
	if manager != nil {
		done, err := manager.Head(ctx, spec.SpaceID, spec.DatasetID)
		if err == nil {
			state.BuiltSequence = done.Sequence
			err = s.saveFollowState(ctx, runtime, state)
		}
		if err != nil {
			return viewindex.FollowState{}, fmt.Errorf("记录索引的跟随进度：%w", err)
		}
	}
	if err := s.activateBuiltIndex(runtime, spec, schema); err != nil {
		return viewindex.FollowState{}, err
	}
	return state, nil
}

// buildInitialIndex 为还没有建好索引的 View 建索引（新数据集、文件被删、删除后同名重建）：列契约取数据集启用的列，修订号从头开始。
// 半成品不续建：失败或进程退出后，下一轮删掉重建。建索引期间的周期完成不补发就绪事件。
func (s *Service) buildInitialIndex(ctx context.Context, opts MaintenanceOptions, runtime *viewRuntime, spec *viewSpec) error {
	// 残留的跟随器先停掉。要在取 buildMu 之前停：它可能正等着 buildMu 做断档重建。
	s.followManager().Stop(spec.SpaceID, spec.DatasetID)
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	_, err := s.buildIndex(ctx, opts, runtime, spec, viewPhaseBuilding, func(*viewRuntime) (viewindex.ViewIndexSchema, error) {
		// 回填单次最多读取 maxLatestPerSeries 行，记录的保留行数不能超过实际回填的行数。
		schema := viewindex.ViewIndexSchema{
			SpaceID: spec.SpaceID, DatasetID: spec.DatasetID, ViewVersion: initialViewRevision,
			Engine: spec.engineName(), Columns: spec.Columns, RowsPerSeries: min(viewBars(spec, opts), maxLatestPerSeries),
			DatasetCreatedAt: spec.CreatedAt,
		}
		schema.SchemaHash = viewindex.HashViewIndexSchema(schema)
		return schema, nil
	})
	if err != nil {
		s.discardInitialBuild(ctx, runtime)
		return err
	}
	s.ensureFollower(runtime.ref)
	return nil
}

// rebuildFollowIndex 在断档（View 停机超过日志保留期，或存储已重建）或索引没有跟随进度时由跟随器原地重建索引：
// 沿用当前索引的列契约（修订号、哈希不变）。重建期间 View 不可读；记录型 View 重建后为空，只跟随之后的写入。
// 失败后不丢弃：View 留在 viewPhaseRebuilding 状态，由跟随器接着重建。
func (s *Service) rebuildFollowIndex(ctx context.Context, ref datasetRef) (viewindex.FollowState, error) {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	if s.followManager() == nil {
		return viewindex.FollowState{}, errors.New("View 没有在跟随变更日志")
	}
	s.mu.RLock()
	runtime := s.views[ref]
	opts := s.maintenance
	if opts.PrimaryRange == nil {
		opts.PrimaryRange = s.primaryRange
	}
	s.mu.RUnlock()
	if runtime == nil {
		return viewindex.FollowState{}, fmt.Errorf("View %s/%s 不在目录中", ref.spaceID, ref.datasetID)
	}
	spec := runtime.queryState().spec
	return s.buildIndex(ctx, opts, runtime, spec, viewPhaseRebuilding, func(runtime *viewRuntime) (viewindex.ViewIndexSchema, error) {
		// 当前契约是活动索引或上一次没建完的重建在用的那份。
		schema := runtime.schema
		if schema.SchemaHash == "" {
			return viewindex.ViewIndexSchema{}, fmt.Errorf("View %s/%s 没有可重建的索引", ref.spaceID, ref.datasetID)
		}
		schema.RowsPerSeries = min(viewBars(spec, opts), maxLatestPerSeries)
		schema.DatasetCreatedAt = spec.CreatedAt
		return schema, nil
	})
}

// activateBuiltIndex 把建好的索引转为活动索引。持有运行时锁转换，实时写入不会夹在中间。
func (s *Service) activateBuiltIndex(runtime *viewRuntime, spec *viewSpec, schema viewindex.ViewIndexSchema) error {
	runtime.mu.Lock()
	if !runtime.phase.isBuilding() {
		runtime.mu.Unlock()
		return errViewBuildCancelled
	}
	attachActiveLocked(runtime, spec, schema)
	runtime.mu.Unlock()
	// 回填的行不走实时跟随：建成后补一次水位，否则 Monitor 要等下一次实时写入才能看到这个 View 的输出。
	s.observeActivatedViewWatermark(runtime, spec)
	return nil
}

// discardInitialBuild 丢弃建失败的首次建索引半成品，下一轮重建。
func (s *Service) discardInitialBuild(ctx context.Context, runtime *viewRuntime) {
	ctx = context.WithoutCancel(ctx)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.phase != viewPhaseBuilding {
		return
	}
	release, err := runtime.gate.lock(ctx)
	if err != nil {
		log.Warnf("storage view 删除半成品索引 %q 失败：%v", runtime.indexID, err)
		return
	}
	defer release()
	runtime.phase = viewPhaseEmpty
	runtime.schema = viewindex.ViewIndexSchema{}
	runtime.publishReadStateLocked()
	if err := runtime.engine.Remove(ctx, runtime.indexID); err != nil {
		log.Warnf("storage view 删除半成品索引 %q 失败：%v", runtime.indexID, err)
	}
}

// backfillInitialIndex 从 Primary 回填时序 View 每个序列最近的 bars 根。记录型 View 没有可读的 Primary 历史，从空开始。
func (s *Service) backfillInitialIndex(ctx context.Context, runtime *viewRuntime, spec *viewSpec, schema viewindex.ViewIndexSchema, opts MaintenanceOptions, bars uint64) error {
	if bars == 0 || !spec.timeSeries() {
		log.Infof("storage view %s/%s 从空开始：没有 Primary 历史可回填", spec.SpaceID, spec.DatasetID)
		return nil
	}
	if opts.PrimaryRange == nil {
		return errors.New("回填 Primary 历史需要 Primary 历史读取客户端")
	}
	pageSize := int(opts.BackfillPageSize)
	if pageSize <= 0 {
		pageSize = int(storageconfig.DefaultViewBackfillPageSize)
	}
	written, err := s.backfillPrimaryHistory(ctx, runtime, spec, schema, pageSize, opts.PrimaryRange, bars, newBackfillRequestLimiter(opts.BackfillRequestInterval))
	if err != nil {
		return err
	}
	if written == 0 {
		// 派生输出（例如因子结果）在第一个周期到来之前合法地为空。
		log.Infof("storage view %s/%s 从空开始：Primary 还没有这个数据集的行", spec.SpaceID, spec.DatasetID)
	}
	return nil
}

// backfillPrimaryHistory 回填 View 保留的最近行：不指定标的、每个序列取最近 periods 根，
// 按 next_cursor 逐页读取（每页 pageSize 行，Pebble 再按单页值数上限截断），一次拿到值。
// 某一页失败（含超时）时整次建索引失败重试，不静默跳过标的。
func (s *Service) backfillPrimaryHistory(ctx context.Context, runtime *viewRuntime, spec *viewSpec, schema viewindex.ViewIndexSchema, pageSize int, rangeReader TimeSeriesRangeReader, periods uint64, limiter *backfillRequestLimiter) (uint64, error) {
	spaceID, datasetID, frequency := spec.SpaceID, spec.DatasetID, spec.Freq
	columns := columnNames(schema.Columns)
	if len(columns) == 0 {
		log.Warnf("storage view history backfill has no dataset columns space=%s dataset=%s", spaceID, datasetID)
		return 0, nil
	}
	perSeries := uint32(math.MaxUint32)
	if periods < uint64(perSeries) {
		perSeries = uint32(periods)
	}
	if perSeries > maxLatestPerSeries {
		perSeries = maxLatestPerSeries
	}
	var written uint64
	cursor := ""
	for {
		if err := backfillStillBuilding(runtime); err != nil {
			return written, err
		}
		pageCtx, cancel := context.WithTimeout(ctx, primaryHistoryPageReadTimeout)
		rsp, err := readPrimaryTimeSeriesRowsLimited(pageCtx, limiter, rangeReader, &pb.ReadTimeSeriesRowsReq{
			SpaceId: spaceID, DatasetId: datasetID,
			ColumnNames: columns, LatestPerSeries: perSeries, Page: &pb.Page{Size: uint32(pageSize), Cursor: cursor},
		})
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return written, ctx.Err()
			}
			return written, fmt.Errorf("read Primary history for %s/%s (written=%d): %w", spaceID, datasetID, written, err)
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return written, fmt.Errorf("read Primary history for %s/%s (written=%d): %w", spaceID, datasetID, written, err)
		}
		writes := make([]viewindex.RowWrite, 0, len(rsp.GetRows()))
		for _, row := range rsp.GetRows() {
			ts := row.GetKey().GetTimeSeries()
			if ts == nil || !strings.EqualFold(strings.TrimSpace(ts.GetFrequency()), strings.TrimSpace(frequency)) {
				continue
			}
			writes = append(writes, viewindex.RowWrite{
				Key:    viewindex.RowKey{Key: row.GetKey()},
				Fields: row.GetFields(),
			})
		}
		for offset := 0; offset < len(writes); offset += 256 {
			end := min(offset+256, len(writes))
			if err := backfillStillBuilding(runtime); err != nil {
				return written, err
			}
			if err := s.writeIndex(ctx, runtime, viewindex.ViewIndexWriteBatch{RowWrites: writes[offset:end], ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.Backfill}); err != nil {
				return written, fmt.Errorf("write Primary period backfill %q: %w", runtime.indexID, err)
			}
			written += uint64(end - offset)
		}
		cursor = rsp.GetPageResult().GetNextCursor()
		if cursor == "" {
			return written, nil
		}
	}
}

var errViewBuildCancelled = errors.New("View 的建索引已被取消")

func backfillStillBuilding(runtime *viewRuntime) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.phase.isBuilding() {
		return errViewBuildCancelled
	}
	return nil
}

// builtSchema 由索引文件记下的契约组出 View 的写入契约。
func builtSchema(spec *viewSpec, stats viewindex.ViewIndexStats) viewindex.ViewIndexSchema {
	return viewindex.ViewIndexSchema{
		SpaceID: spec.SpaceID, DatasetID: spec.DatasetID, ViewVersion: stats.ViewVersion,
		Engine: spec.engineName(), Columns: stats.Columns, SchemaHash: stats.SchemaHash, RowsPerSeries: stats.RowsPerSeries,
		DatasetCreatedAt: spec.CreatedAt,
	}
}

// attachActiveView 按索引文件记下的契约挂接 View 的索引，并起跟随器。
func (s *Service) attachActiveView(runtime *viewRuntime, spec *viewSpec, stats viewindex.ViewIndexStats) {
	attachRuntime(runtime, spec, builtSchema(spec, stats))
	// 解锁之后再起跟随器：它可能正等着运行时锁写入。重建没完成时也要起：跟随器会接着重建。
	s.ensureFollower(runtime.ref)
}

// attachRuntime 持有运行时锁挂接活动索引，返回是否挂接了。
func attachRuntime(runtime *viewRuntime, spec *viewSpec, schema viewindex.ViewIndexSchema) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	// 维护在锁外读的索引契约可能早于定时任务刚提交的加列：不能用旧快照把写入契约改回去，只刷新声明与隐藏列。
	// 加列在运行时锁下提交，这里必须在锁内读当前契约，锁外读到的可能已经过期。
	if runtime.phase.isActive() && runtime.schema.ViewVersion > schema.ViewVersion {
		schema = runtime.schema
	}
	// 建索引或断档重建进行中：索引建成前不能挂回去。
	if runtime.phase.isBuilding() {
		return false
	}
	attachActiveLocked(runtime, spec, schema)
	return true
}

// attachActiveLocked 更新内存里的活动索引（调用方持有 runtime.mu）：重启恢复、维护和建索引完成都走这里。
func attachActiveLocked(runtime *viewRuntime, spec *viewSpec, schema viewindex.ViewIndexSchema) {
	if !runtime.phase.isActive() {
		runtime.bumpRevision()
		runtime.hasStats = false
		runtime.stats = viewindex.ViewIndexStats{}
	}
	runtime.spec = spec
	runtime.phase = viewPhaseActive
	runtime.schema = schema
	runtime.publishReadStateLocked()
}

// setSpec 换上维护循环这一轮组出的声明。
func (r *viewRuntime) setSpec(spec *viewSpec) {
	r.mu.Lock()
	r.spec = spec
	r.publishReadStateLocked()
	r.mu.Unlock()
}
