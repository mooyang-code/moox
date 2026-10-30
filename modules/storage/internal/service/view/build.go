package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
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

// buildInitialIndex 为还没有索引的 View 建索引：准备空索引、把实时写入接到它上面、从 Primary 回填，最后提交元数据，
// View 从此可读。建索引期间 View 不可读，读取方等待。半成品不续建：失败或进程退出后，下一轮删掉重建。
func (s *Service) buildInitialIndex(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View) error {
	columns, err := desiredViewColumns(ctx, opts.Metadata, auth, view)
	if err != nil {
		return err
	}
	columns = numberedViewColumns(columns)
	ref := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	s.mu.Lock()
	s.catalogViews[ref] = proto.Clone(view).(*pb.View)
	runtime := s.views[ref]
	s.mu.Unlock()
	if runtime != nil {
		// 元数据是权威：它说没有活动索引时（View 被删除后重建），内存里残留的活动索引不再有效。
		runtime.mu.Lock()
		if runtime.active != "" {
			runtime.clearActiveLocked()
		}
		runtime.mu.Unlock()
	}
	bars := viewBars(view, opts)
	engine := strings.ToLower(strings.TrimSpace(view.GetEngine()))
	// 回填单次最多读取 maxLatestPerSeries 行，记录的保留行数不能超过实际回填的行数。
	schema := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: initialViewRevision,
		PrimaryDatasetID: view.GetDatasetId(), Engine: engine, Columns: columns, RowsPerSeries: min(bars, maxLatestPerSeries),
	}
	schema.SchemaHash = viewindex.HashViewIndexSchema(schema)
	indexID := viewindex.ViewIndexID(view.GetSpaceId(), view.GetViewId())
	prepared, err := s.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{
		AuthInfo: auth, IndexId: indexID, Engine: engine,
		Schema: &pb.ViewIndexSchema{
			SpaceId: schema.SpaceID, ViewId: schema.ViewID, ViewVersion: schema.ViewVersion,
			Engine: engine, Columns: columns, ViewSchemaHash: schema.SchemaHash, DatasetId: schema.PrimaryDatasetID,
			RowsPerSeries: schema.RowsPerSeries,
		},
	})
	if err == nil {
		err = requireSuccess(prepared.GetRetInfo())
	}
	if err != nil {
		return fmt.Errorf("准备 View 索引：%w", err)
	}
	if _, err := s.backfillInitialIndex(ctx, view, schema, indexID, opts, bars); err != nil {
		s.discardInitialBuild(ctx, ref, indexID)
		if isPrimaryHistoryIndexNotReady(err) {
			// Primary 的历史索引是首次使用时异步物化的：这是正常的预热等待，下一轮再建。
			return nil
		}
		return err
	}
	return s.completeInitialBuild(ctx, opts, auth, view, schema, indexID)
}

// numberedViewColumns 按当前顺序给列编号（克隆后修改）：追加校验要求已建列的 sort_order 互不相同，
// 而没有显式投影的 View 取自数据集的列都没有排序值。
func numberedViewColumns(columns []*pb.ViewColumn) []*pb.ViewColumn {
	numbered := make([]*pb.ViewColumn, 0, len(columns))
	for _, column := range columns {
		if column == nil {
			continue
		}
		clone := proto.Clone(column).(*pb.ViewColumn)
		clone.SortOrder = uint32(len(numbered))
		numbered = append(numbered, clone)
	}
	return numbered
}

// completeInitialBuild 提交建好的索引并把它转为活动索引。持有运行时锁提交元数据，实时写入不会夹在提交和转换之间。
// 响应不明确时读回元数据判断；仍看不到活动索引就保留现场返回错误，下一轮按元数据决定是挂接还是重建，绝不能在这里删除索引。
func (s *Service) completeInitialBuild(ctx context.Context, opts MaintenanceOptions, auth *pb.AuthInfo, view *pb.View, schema viewindex.ViewIndexSchema, indexID string) error {
	ref := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	s.mu.RLock()
	runtime := s.views[ref]
	s.mu.RUnlock()
	if runtime == nil {
		return errViewBuildCancelled
	}
	runtime.mu.Lock()
	if runtime.building != indexID {
		runtime.mu.Unlock()
		return errViewBuildCancelled
	}
	rsp, err := opts.Metadata.CompleteViewInitialBuild(ctx, &pb.CompleteViewInitialBuildReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), IndexId: indexID,
		Columns: schema.Columns, ViewSchemaHash: schema.SchemaHash,
	})
	if err == nil {
		err = requireSuccess(rsp.GetRetInfo())
	}
	committed := rsp.GetView()
	if err != nil {
		readback, readErr := s.readActiveView(ctx, opts.Metadata, auth, view.GetSpaceId(), view.GetViewId())
		if readErr != nil || readback.GetActiveIndexId() != indexID {
			runtime.mu.Unlock()
			return errors.Join(err, readErr)
		}
		committed = readback
	}
	if committed.GetActiveIndexId() != indexID {
		runtime.mu.Unlock()
		return fmt.Errorf("元数据没有返回建好的索引 %q", indexID)
	}
	s.attachActiveViewLocked(committed, runtime, schema, schema.Columns, schema.Engine)
	runtime.mu.Unlock()
	// 回填的行不走实时事件：建成后补一次水位，否则 Monitor 要等下一次实时写入才能看到这个 View 的输出。
	s.observeActivatedViewWatermark(view, indexID)
	if err := s.ReplayPendingSubjects(ctx); err != nil {
		log.Printf("storage view 建成后重放待补标的失败：%v", err)
	}
	return nil
}

// discardInitialBuild 丢弃建失败的半成品，下一轮重建。
func (s *Service) discardInitialBuild(ctx context.Context, ref viewRef, indexID string) {
	s.mu.RLock()
	runtime := s.views[ref]
	engine := s.engines[s.indexEngine[indexID]]
	s.mu.RUnlock()
	if runtime == nil || engine == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.building != indexID {
		return
	}
	release, err := s.indexWriteGate(indexID).lock(ctx)
	if err != nil {
		log.Printf("storage view 删除半成品索引 %q 失败：%v", indexID, err)
		return
	}
	defer release()
	runtime.building = ""
	s.mu.Lock()
	s.forgetIndexLocked(indexID)
	s.mu.Unlock()
	if err := engine.Remove(ctx, indexID); err != nil {
		log.Printf("storage view 删除半成品索引 %q 失败：%v", indexID, err)
	}
}

// backfillInitialIndex 从 Primary 回填时序 View 每个序列最近的 bars 根。记录型 View 和 Bleve 没有可读的 Primary 历史，从空开始。
func (s *Service) backfillInitialIndex(ctx context.Context, view *pb.View, schema viewindex.ViewIndexSchema, indexID string, opts MaintenanceOptions, bars uint64) (uint64, error) {
	if bars == 0 || schema.Engine == "bleve" {
		log.Printf("storage view %s/%s 从空开始：没有 Primary 历史可回填", view.GetSpaceId(), view.GetViewId())
		return 0, nil
	}
	if view.GetFreq() == "" {
		return 0, errors.New("回填 Primary 历史需要时序 View 的频率")
	}
	if opts.PrimaryRange == nil {
		return 0, errors.New("回填 Primary 历史需要 Primary 历史读取客户端")
	}
	pageSize := int(opts.BackfillPageSize)
	if pageSize <= 0 {
		pageSize = viewBackfillBatchSize
	}
	written, err := s.backfillPrimaryHistory(ctx, view, schema, indexID, pageSize, opts.PrimaryRange, bars, newBackfillRequestLimiter(opts.BackfillRequestInterval))
	if err != nil {
		return written, err
	}
	if written == 0 {
		// 派生输出（例如因子结果）在第一个周期到来之前合法地为空。
		log.Printf("storage view %s/%s 从空开始：Primary 还没有这个 View 的行", view.GetSpaceId(), view.GetViewId())
	}
	return written, nil
}

// backfillPrimaryHistory 回填 View 保留的最近行：不指定标的、每个序列取最近 periods 根，
// 按 next_cursor 逐页读取（每页 pageSize 行，DataNode 再按单页值数上限截断），一次拿到值。
// 某一页失败（含超时）时整次建索引失败重试，不静默跳过标的。
func (s *Service) backfillPrimaryHistory(ctx context.Context, view *pb.View, schema viewindex.ViewIndexSchema, indexID string, pageSize int, rangeReader TimeSeriesRangeReader, periods uint64, limiter *backfillRequestLimiter) (uint64, error) {
	spaceID, viewID, frequency := view.GetSpaceId(), view.GetViewId(), view.GetFreq()
	columns := viewColumnFields(schema.Columns)
	if len(columns) == 0 {
		log.Printf("storage view history backfill has no dataset columns space=%s view=%s", spaceID, viewID)
		return 0, nil
	}
	auth := s.primaryAuthSnapshot()
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
		if err := s.backfillStillBuilding(spaceID, viewID, indexID); err != nil {
			return written, err
		}
		pageCtx, cancel := context.WithTimeout(ctx, primaryHistoryPageReadTimeout)
		rsp, err := readPrimaryTimeSeriesRowsLimited(pageCtx, limiter, rangeReader, &pb.ReadTimeSeriesRowsReq{
			AuthInfo: auth, SpaceId: spaceID, DatasetId: view.GetDatasetId(),
			ColumnNames: columns, LatestPerSeries: perSeries, Page: &pb.Page{Size: uint32(pageSize), Cursor: cursor},
		})
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return written, ctx.Err()
			}
			return written, fmt.Errorf("read Primary history for %s/%s (written=%d): %w", spaceID, view.GetDatasetId(), written, err)
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return written, fmt.Errorf("read Primary history for %s/%s (written=%d): %w", spaceID, view.GetDatasetId(), written, err)
		}
		writes := make([]viewindex.RowWrite, 0, len(rsp.GetRows()))
		for _, row := range rsp.GetRows() {
			key := row.GetKey()
			if key == nil || !strings.EqualFold(strings.TrimSpace(key.GetFreq()), strings.TrimSpace(frequency)) {
				continue
			}
			writes = append(writes, viewindex.RowWrite{
				Key: viewindex.RowKey{Key: &pb.RowKey{
					SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(),
					Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime(), SeriesTag: key.GetSeriesTag()}},
				}},
				Fields: row.GetFields(),
			})
		}
		for offset := 0; offset < len(writes); offset += 256 {
			end := min(offset+256, len(writes))
			if err := s.backfillStillBuilding(spaceID, viewID, indexID); err != nil {
				return written, err
			}
			engine, err := s.engineFor(indexID)
			if err != nil {
				return written, err
			}
			if err := s.writeIndex(ctx, indexID, engine, viewindex.ViewIndexWriteBatch{RowWrites: writes[offset:end], ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.Backfill}); err != nil {
				return written, fmt.Errorf("write Primary period backfill %q: %w", indexID, err)
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

func (s *Service) backfillStillBuilding(spaceID, viewID, indexID string) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errViewBuildCancelled
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.building != indexID {
		return errViewBuildCancelled
	}
	return nil
}

func (s *Service) AttachActiveView(ctx context.Context, view *pb.View) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" || view.GetActiveIndexId() == "" {
		return errors.New("active view metadata is required")
	}
	engineName := strings.ToLower(strings.TrimSpace(view.GetEngine()))
	if s.engines[engineName] == nil {
		return fmt.Errorf("view engine %q is unavailable", engineName)
	}
	columns := view.GetActiveColumns()
	schema := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: view.GetViewRevision(),
		PrimaryDatasetID: view.GetDatasetId(), Engine: engineName, Columns: columns, SchemaHash: view.GetActiveViewSchemaHash(),
	}
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	s.mu.Lock()
	runtime := s.views[viewKey]
	if runtime == nil {
		runtime = newViewRuntime()
		s.views[viewKey] = runtime
	}
	attached := s.schemas[view.GetActiveIndexId()]
	s.mu.Unlock()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	// 维护读到的元数据可能早于定时任务刚提交的加列：不能用旧快照把写入契约改回去。
	if runtime.active == view.GetActiveIndexId() && attached.ViewVersion > view.GetViewRevision() {
		return nil
	}
	s.attachActiveViewLocked(view, runtime, schema, columns, engineName)
	return nil
}

// attachActiveViewLocked 更新内存里的活动索引（调用方持有 runtime.mu）：重启恢复、维护和建索引完成都走这里。
func (s *Service) attachActiveViewLocked(view *pb.View, runtime *viewRuntime, schema viewindex.ViewIndexSchema, columns []*pb.ViewColumn, engineName string) {
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalogViews[viewKey] = proto.Clone(view).(*pb.View)
	s.indexEngine[view.GetActiveIndexId()] = engineName
	s.schemas[view.GetActiveIndexId()] = schema
	if runtime.active != view.GetActiveIndexId() {
		runtime.statsIndexID = ""
		runtime.stats = viewindex.ViewIndexStats{}
	}
	runtime.active = view.GetActiveIndexId()
	runtime.building = ""
	runtime.publishReadStateLocked()
	s.indexView[view.GetActiveIndexId()] = viewKey
	s.attachOwnedDatasetMappingLocked(view.GetSpaceId(), view.GetDatasetId(), view.GetActiveIndexId())
}
