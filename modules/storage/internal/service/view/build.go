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

// errActiveContractUnavailable is a startup-fatal metadata condition. A view
// already mid-rebuild must not fall back to its desired contract: doing so
// would acknowledge rows/markers against the wrong View revision.
var errActiveContractUnavailable = errors.New("active view contract unavailable")

type backfillRequestLimiter struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

// primaryHistoryPageReadTimeout 限制一页 Primary 读取的耗时；超时则整次重建失败并重试，不静默跳过。
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

func (s *Service) BackfillView(ctx context.Context, spaceID, viewID string, batchSize int) error {
	return s.BackfillViewWithReader(ctx, spaceID, viewID, batchSize, nil)
}

func (s *Service) BackfillViewWithReader(ctx context.Context, spaceID, viewID string, batchSize int, reader FieldReader) error {
	_, err := s.backfillViewWithReader(ctx, spaceID, viewID, batchSize, reader, nil, 0)
	return err
}

func (s *Service) backfillViewWithReader(ctx context.Context, spaceID, viewID string, batchSize int, reader FieldReader, rangeReader TimeSeriesRangeReader, bars uint64) (uint64, error) {
	return s.backfillViewWithReaderLimited(ctx, spaceID, viewID, batchSize, reader, rangeReader, bars, nil)
}

// backfillViewWithReaderLimited fills the pending index. A time-series View
// takes the latest bars of every series from Primary; a record View copies
// its active index. batchSize is the page size of either read.
func (s *Service) backfillViewWithReaderLimited(ctx context.Context, spaceID, viewID string, batchSize int, reader FieldReader, rangeReader TimeSeriesRangeReader, bars uint64, limiter *backfillRequestLimiter) (uint64, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	catalogView := s.catalogViews[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return 0, errors.New("view runtime is not prepared")
	}
	runtime.mu.Lock()
	activeID, nextID := runtime.active, runtime.next
	runtime.mu.Unlock()
	if nextID == "" {
		return 0, errors.New("view has no pending build")
	}

	// Every time-series rebuild uses Primary for the configured bars;
	// copying the active index can reproduce a short or stale View. Record
	// Bleve Views retain the active-copy path because they have no time-series
	// Primary history reader.
	// rebuild_source=primary 的 View 是派生输出（例如因子结果）：可以在第一个周期到来前合法地为空，
	// 但已有的活动索引不一定完整（中断的构建或丢失的事件会留下部分结果）。主库历史可用时直接从主库重建，
	// 不与活动索引按 Backfill 的“不覆盖”语义合并。
	primaryOnly := rebuildsFromPrimaryOnly(catalogView)
	historyConfigured := bars > 0
	primaryHistoryRebuild := historyConfigured && primaryOnly && reader != nil && rangeReader != nil
	if activeID != "" && historyConfigured && (!primaryOnly || primaryHistoryRebuild) {
		nextEngine, err := s.engineFor(nextID)
		if err != nil {
			return 0, err
		}
		if !strings.EqualFold(strings.TrimSpace(nextEngine.Engine()), "bleve") {
			if reader == nil {
				return 0, errors.New("Primary field reader is required for a time-series View rebuild")
			}
			if rangeReader == nil {
				return 0, errors.New("Primary history range reader is required for a time-series View rebuild")
			}
			activeID = ""
		}
	}
	if activeID != "" && rangeReader != nil {
		if _, err := s.engineFor(activeID); err != nil {
			activeID = ""
		}
	}

	var written uint64
	if activeID != "" {
		active, err := s.engineFor(activeID)
		if err != nil {
			return 0, err
		}
		next, err := s.engineFor(nextID)
		if err != nil {
			return 0, err
		}
		s.mu.RLock()
		nextSchema := s.schemas[nextID]
		activeSchema := s.schemas[activeID]
		s.mu.RUnlock()
		{
			var after *pb.RowKey
			for {
				rows, _, err := active.Query(ctx, activeID, viewindex.QuerySpec{
					AfterKey: after, Sorts: backfillSorts(active.Engine()), Limit: batchSize, TotalMode: pb.TotalMode_NONE,
				})
				if err != nil {
					return written, fmt.Errorf("query active view %q for backfill: %w", activeID, err)
				}
				if len(rows) == 0 {
					break
				}
				writes := make([]viewindex.RowWrite, 0, len(rows))
				for _, row := range rows {
					if row == nil || row.GetKey() == nil {
						continue
					}
					writes = append(writes, viewindex.RowWrite{
						Key:        viewindex.RowKey{Key: proto.Clone(row.GetKey()).(*pb.RowKey)},
						Fields:     projectBackfillFields(row.GetFields(), activeSchema, nextSchema),
						Attributes: row.GetAttributes(),
					})
				}
				if reader != nil && len(writes) > 0 {
					if err := s.enrichBackfillRows(ctx, reader, activeID, nextID, writes, limiter); err != nil {
						return written, err
					}
				}
				for offset := 0; offset < len(writes); offset += 256 {
					end := offset + 256
					if end > len(writes) {
						end = len(writes)
					}
					if err := s.backfillStillActive(spaceID, viewID, nextID); err != nil {
						return written, err
					}
					if err := s.writeIndex(ctx, nextID, next, viewindex.ViewIndexWriteBatch{RowWrites: writes[offset:end], ViewRevision: nextSchema.ViewVersion, ViewSchemaHash: nextSchema.SchemaHash, WriteMode: viewindex.Backfill}); err != nil {
						return written, fmt.Errorf("write view backfill %q: %w", nextID, err)
					}
					written += uint64(end - offset)
				}
				if len(rows) < batchSize {
					break
				}
				after = proto.Clone(rows[len(rows)-1].GetKey()).(*pb.RowKey)
			}
		}
	} else {
		next, err := s.engineFor(nextID)
		if err != nil {
			return 0, err
		}
		if strings.EqualFold(strings.TrimSpace(next.Engine()), "bleve") {
			log.Printf("storage record View %s/%s starts empty: no Primary history reader", spaceID, viewID)
		} else if primaryOnly {
			written, err = s.backfillPrimaryOnlyHistory(ctx, spaceID, viewID, nextID, batchSize, reader, rangeReader, bars, limiter)
			if err != nil {
				return written, err
			}
		} else {
			if reader == nil {
				return 0, errors.New("Primary field reader is required for a time-series View rebuild")
			}
			written, err = s.backfillPrimaryHistory(ctx, spaceID, viewID, nextID, batchSize, reader, rangeReader, bars, limiter)
			if err != nil {
				return written, err
			}
		}
	}
	runtime.mu.Lock()
	if runtime.next != nextID || runtime.buildFailed {
		runtime.mu.Unlock()
		return written, errViewBuildFailed
	}
	runtime.status = "ready"
	runtime.mu.Unlock()
	return written, nil
}

func (s *Service) backfillPrimaryOnlyHistory(ctx context.Context, spaceID, viewID, nextID string, batchSize int, reader FieldReader, rangeReader TimeSeriesRangeReader, bars uint64, limiter *backfillRequestLimiter) (uint64, error) {
	if reader == nil || rangeReader == nil {
		log.Printf("storage View %s/%s (rebuild_source=primary) starts empty; waiting for the writer's output", spaceID, viewID)
		return 0, nil
	}
	written, err := s.backfillPrimaryHistory(ctx, spaceID, viewID, nextID, batchSize, reader, rangeReader, bars, limiter)
	if err != nil {
		return written, err
	}
	if written == 0 {
		log.Printf("storage View %s/%s (rebuild_source=primary) starts empty; waiting for the writer's output", spaceID, viewID)
	}
	return written, nil
}

func (s *Service) backfillPrimaryHistory(ctx context.Context, spaceID, viewID, nextID string, batchSize int, reader FieldReader, rangeReader TimeSeriesRangeReader, bars uint64, limiter *backfillRequestLimiter) (uint64, error) {
	if bars == 0 {
		return 0, nil
	}
	if reader == nil {
		return 0, errors.New("Primary field reader is required for a time-series View history backfill")
	}
	if rangeReader == nil {
		return 0, errors.New("Primary history range reader is required for a View without an active index")
	}
	s.mu.RLock()
	view := s.catalogViews[viewRef{spaceID: spaceID, viewID: viewID}]
	nextSchema := s.schemas[nextID]
	auth := s.primaryAuth
	if auth != nil {
		auth = proto.Clone(auth).(*pb.AuthInfo)
	}
	s.mu.RUnlock()
	if view == nil || view.GetDatasetId() == "" {
		return 0, errors.New("primary dataset is required for View history backfill")
	}
	if view.GetFreq() == "" {
		return 0, errors.New("time-series View frequency is required for Primary history backfill")
	}
	return s.backfillPrimaryHistoryByPeriods(ctx, spaceID, viewID, nextID, batchSize, rangeReader, view, nextSchema, auth, view.GetFreq(), bars, limiter)
}

// backfillPrimaryHistoryByPeriods 重建 View 保留的最近行：不指定标的、每个序列取最近 periods 根，
// 按 next_cursor 逐页读取（每页 pageSize 行，DataNode 再按单页值数上限截断），一次拿到值。
// 某一页失败（含超时）时整次重建按现有失败处理重试，不静默跳过标的。
func (s *Service) backfillPrimaryHistoryByPeriods(ctx context.Context, spaceID, viewID, nextID string, pageSize int, rangeReader TimeSeriesRangeReader, view *pb.View, nextSchema viewindex.ViewIndexSchema, auth *pb.AuthInfo, frequency string, periods uint64, limiter *backfillRequestLimiter) (uint64, error) {
	columns := viewColumnFields(nextSchema.Columns)
	if len(columns) == 0 {
		log.Printf("storage view history backfill has no dataset columns space=%s view=%s", spaceID, viewID)
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
		if err := s.backfillStillActive(spaceID, viewID, nextID); err != nil {
			return written, err
		}
		pageCtx, cancel := context.WithTimeout(ctx, primaryHistoryPageReadTimeout)
		rsp, err := readPrimaryTimeSeriesRowsLimited(pageCtx, limiter, rangeReader, &pb.ReadTimeSeriesRowsReq{
			AuthInfo: auth, SpaceId: view.GetSpaceId(), DatasetId: view.GetDatasetId(),
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
			if err := s.backfillStillActive(spaceID, viewID, nextID); err != nil {
				return written, err
			}
			engine, err := s.engineFor(nextID)
			if err != nil {
				return written, err
			}
			if err := s.writeIndex(ctx, nextID, engine, viewindex.ViewIndexWriteBatch{RowWrites: writes[offset:end], ViewRevision: nextSchema.ViewVersion, ViewSchemaHash: nextSchema.SchemaHash, WriteMode: viewindex.Backfill}); err != nil {
				return written, fmt.Errorf("write Primary period backfill %q: %w", nextID, err)
			}
			written += uint64(end - offset)
		}
		cursor = rsp.GetPageResult().GetNextCursor()
		if cursor == "" {
			if written == 0 {
				log.Printf("storage view history backfill found no Primary rows space=%s view=%s freq=%s", spaceID, viewID, frequency)
			}
			return written, nil
		}
	}
}

// rebuildsFromPrimaryOnly 判断 View 是否只从主库历史重建：View 属性 rebuild_source 为 primary；
// 缺省（active_then_primary）先复制活动索引再从主库补。
func rebuildsFromPrimaryOnly(view *pb.View) bool {
	return view != nil && strings.EqualFold(strings.TrimSpace(view.GetAttributes()["rebuild_source"]), "primary")
}

var errViewBuildFailed = errors.New("view build has been marked failed")

func (s *Service) backfillStillActive(spaceID, viewID, nextID string) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errViewBuildFailed
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.next != nextID || runtime.buildFailed {
		return errViewBuildFailed
	}
	return nil
}

func backfillSorts(engine string) []*pb.SortSpec {
	if engine == "bleve" {
		return []*pb.SortSpec{{FieldName: "record_id"}, {FieldName: "version"}}
	}
	return []*pb.SortSpec{
		{FieldName: "data_time"}, {FieldName: "subject_id"}, {FieldName: "freq"}, {FieldName: "series_tag"},
		{FieldName: "record_id"}, {FieldName: "version"},
	}
}

func (s *Service) enrichBackfillRows(ctx context.Context, reader FieldReader, activeID, nextID string, writes []viewindex.RowWrite, limiter *backfillRequestLimiter) error {
	s.mu.RLock()
	activeSchema := s.schemas[activeID]
	nextSchema := s.schemas[nextID]
	s.mu.RUnlock()
	activeColumns := make(map[string]viewColumnShape, len(activeSchema.Columns))
	for _, column := range activeSchema.Columns {
		if column != nil {
			activeColumns[column.GetColumnName()] = viewColumnShapeOf(column)
		}
	}
	datasetID := strings.TrimSpace(nextSchema.PrimaryDatasetID)
	var changed []*pb.ViewColumn
	for _, column := range nextSchema.Columns {
		if column == nil {
			continue
		}
		if active, exists := activeColumns[column.GetColumnName()]; exists && active.equal(viewColumnShapeOf(column)) {
			continue
		}
		changed = append(changed, column)
	}
	fieldIDs := viewColumnFields(changed)
	if datasetID == "" || len(fieldIDs) == 0 {
		return nil
	}
	fields := make(map[string]struct{}, len(fieldIDs))
	for _, fieldID := range fieldIDs {
		fields[fieldID] = struct{}{}
	}
	s.mu.RLock()
	auth := s.primaryAuth
	if auth != nil {
		auth = proto.Clone(auth).(*pb.AuthInfo)
	}
	s.mu.RUnlock()
	if auth == nil {
		return errors.New("primary auth is not configured")
	}
	// Primary bounds a read request at 100,000 key-field pairs. Keep
	// enrichment below that limit even for wide Views and large rebuild
	// pages instead of failing the whole build.
	chunkSize := len(writes)
	if chunkSize > 100000/len(fieldIDs) {
		chunkSize = 100000 / len(fieldIDs)
		if chunkSize == 0 {
			chunkSize = 1
		}
	}
	// The key-field limit is not a latency budget. A 100k-pair request
	// can still exceed the PrimaryStore RPC deadline on a busy Pebble
	// host, causing the whole A/B build to self-cancel. Keep individual
	// point-read chunks small; the outer history page remains 10k keys.
	if chunkSize > 512 {
		chunkSize = 512
	}
	for chunkStart := 0; chunkStart < len(writes); chunkStart += chunkSize {
		chunkEnd := chunkStart + chunkSize
		if chunkEnd > len(writes) {
			chunkEnd = len(writes)
		}
		keys := make([]*pb.RowKey, 0, chunkEnd-chunkStart)
		positions := make(map[string]int, chunkEnd-chunkStart)
		for index := chunkStart; index < chunkEnd; index++ {
			key := proto.Clone(writes[index].Key.Key).(*pb.RowKey)
			key.DatasetId = datasetID
			keys = append(keys, key)
			positions[viewindex.RowKeyID(key)] = index
		}
		if err := limiter.wait(ctx); err != nil {
			return err
		}
		rsp, err := reader.ReadRows(ctx, &pb.PrimaryReadRowsReq{AuthInfo: auth, Keys: keys, ColumnNames: fieldIDs})
		if err != nil {
			return err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return err
		}
		for _, row := range rsp.GetRows() {
			position, ok := positions[viewindex.RowKeyID(row.GetKey())]
			if !ok {
				continue
			}
			for _, field := range row.GetFields() {
				if _, ok := fields[field.GetFieldId()]; ok {
					writes[position].Fields = append(writes[position].Fields, &pb.FieldValue{FieldId: field.GetFieldId(), Value: field.GetValue()})
				}
			}
		}
	}
	return nil
}

type viewColumnShape struct {
	origin     string
	originType pb.ColumnOriginType
	valueType  pb.FieldValueType
}

func viewColumnShapeOf(column *pb.ViewColumn) viewColumnShape {
	if column == nil {
		return viewColumnShape{}
	}
	return viewColumnShape{origin: column.GetOriginId(), originType: column.GetOriginType(), valueType: column.GetValueType()}
}

func (s viewColumnShape) equal(other viewColumnShape) bool {
	return s.origin == other.origin && s.originType == other.originType && s.valueType == other.valueType
}

func projectBackfillFields(fields []*pb.FieldValue, active, next viewindex.ViewIndexSchema) []*pb.FieldValue {
	activeShapes := make(map[string]viewColumnShape, len(active.Columns))
	for _, column := range active.Columns {
		if column != nil {
			activeShapes[column.GetColumnName()] = viewColumnShapeOf(column)
		}
	}
	nextShapes := make(map[string]viewColumnShape, len(next.Columns))
	for _, column := range next.Columns {
		if column != nil {
			nextShapes[column.GetColumnName()] = viewColumnShapeOf(column)
		}
	}
	projected := make([]*pb.FieldValue, 0, len(fields))
	for _, field := range fields {
		if field == nil {
			continue
		}
		name := field.GetFieldId()
		nextShape, ok := nextShapes[name]
		activeShape, activeOK := activeShapes[name]
		if ok && activeOK && activeShape.equal(nextShape) {
			projected = append(projected, field)
		}
	}
	return projected
}

// SwitchView 把已完成的替换索引切成活动索引（测试与端到端用例直接驱动切换）。
func (s *Service) SwitchView(ctx context.Context, spaceID, viewID string) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errors.New("view runtime is not prepared")
	}
	runtime.mu.Lock()
	_, _, err := s.switchViewLocked(context.WithoutCancel(ctx), runtime)
	if err != nil {
		runtime.mu.Unlock()
		return err
	}
	runtime.mu.Unlock()
	// The in-memory and metadata switch is already committed. Do not report a
	// cancellation from the caller's context as a failed build: doing so would
	// discard the newly active index after a successful Activate RPC.
	return nil
}

func (s *Service) switchViewLocked(ctx context.Context, runtime *viewRuntime) (string, uint64, error) {
	if runtime == nil || runtime.next == "" || runtime.status != "ready" || runtime.buildFailed {
		return "", 0, errors.New("no completed view build to switch")
	}
	oldID := runtime.active
	oldGeneration := s.indexGenerationOf(oldID)
	if oldID != "" {
		if err := s.retireIndex(ctx, oldID, oldGeneration); err != nil {
			return "", 0, fmt.Errorf("mark old view index retiring: %w", err)
		}
	}
	s.inheritAppliedFence(oldID, runtime.next)
	runtime.active = runtime.next
	runtime.activeDatasetIDs = append([]string(nil), runtime.nextDatasetIDs...)
	runtime.activePrimaryDatasetID = runtime.nextPrimaryDatasetID
	runtime.activeDatasetSet = true
	runtime.statsIndexID = ""
	runtime.stats = viewindex.ViewIndexStats{}
	runtime.publishReadStateLocked()
	runtime.next = ""
	runtime.nextDatasetIDs = nil
	runtime.nextPrimaryDatasetID = ""
	runtime.status = "active"
	runtime.buildID = ""
	runtime.ownerID = ""
	runtime.metadata = nil
	runtime.metadataAuth = nil
	runtime.buildFailed = false
	if runtime.buildCancel != nil {
		runtime.buildCancel()
	}
	runtime.buildCancel = nil
	runtime.buildContext = nil
	return oldID, oldGeneration, nil
}

func (s *Service) TrackViewBuild(ctx context.Context, spaceID, viewID, buildID, ownerID string, metadata MetadataClient, auth *pb.AuthInfo) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil || metadata == nil || buildID == "" || ownerID == "" {
		return errors.New("view build tracking requires runtime, metadata, build_id and owner_id")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.buildCancel != nil {
		runtime.buildCancel()
	}
	buildCtx, cancel := context.WithCancel(ctx)
	runtime.buildContext = buildCtx
	runtime.buildCancel = cancel
	runtime.buildFailed = false
	runtime.buildID = buildID
	runtime.ownerID = ownerID
	runtime.metadata = metadata
	if auth != nil {
		runtime.metadataAuth = proto.Clone(auth).(*pb.AuthInfo)
	}
	return nil
}

func (s *Service) failRuntimeBuild(ctx context.Context, key viewRef, runtime *viewRuntime, cause error) error {
	if runtime == nil || runtime.metadata == nil || runtime.buildID == "" || runtime.ownerID == "" {
		return errors.New("view build failure cannot be persisted")
	}
	if current, err := s.readActiveView(ctx, runtime.metadata, runtime.metadataAuth, key.spaceID, key.viewID); err == nil {
		if build := current.GetIndexBuild(); build != nil && build.GetBuildId() == runtime.buildID && build.GetState() == pb.ViewIndexBuild_FAILED {
			// A previous redelivery may have committed the failure while its RPC
			// response was lost. Treat the state as idempotently persisted so the
			// caller can discard the failed inactive slot.
			runtime.buildFailed = true
			runtime.status = "failing"
			return nil
		}
	}
	runtime.buildFailed = true
	runtime.status = "failing"
	if runtime.buildCancel != nil {
		runtime.buildCancel()
	}
	message := "new view live write failed"
	if cause != nil {
		message = cause.Error()
	}
	rsp, err := runtime.metadata.FailViewIndexBuild(ctx, &pb.FailViewIndexBuildReq{
		AuthInfo: runtime.metadataAuth, SpaceId: key.spaceID, ViewId: key.viewID,
		BuildId: runtime.buildID, OwnerId: runtime.ownerID, Error: message,
	})
	if err != nil {
		log.Printf("storage view failed to mark build %s/%s as failed: %v", key.spaceID, key.viewID, err)
		return err
	}
	if err := requireSuccess(rsp.GetRetInfo()); err != nil {
		return err
	}
	return nil
}

func (s *Service) AttachActiveView(ctx context.Context, view *pb.View) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" || view.GetActiveIndexId() == "" {
		return errors.New("active view metadata is required")
	}
	if err := validateAttachedSingleDataset(view); err != nil {
		return err
	}
	// A metadata row describing an in-flight rebuild must carry the persisted
	// active contract. Falling back to the desired DatasetIds/DatasetId would
	// silently route markers and queries to the next revision, so refuse that
	// inconsistent state and surface an actionable error.
	if view.GetActiveViewRevision() > 0 && view.GetDesiredViewRevision() > view.GetActiveViewRevision() {
		if persistedActiveDatasetID(view) == "" {
			return fmt.Errorf("%w: active view %s/%s is rebuilding without persisted active contract", errActiveContractUnavailable, view.GetSpaceId(), view.GetViewId())
		}
	}
	engineName := strings.ToLower(strings.TrimSpace(view.GetEngine()))
	if s.engines[engineName] == nil {
		return fmt.Errorf("view engine %q is unavailable", engineName)
	}
	columns := view.GetActiveColumns()
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] != "true" {
		columns = view.GetColumns()
	}
	activePrimaryDatasetID := strings.TrimSpace(view.GetAttributes()[activePrimaryDatasetAttr])
	if activePrimaryDatasetID == "" {
		activePrimaryDatasetID = view.GetDatasetId()
	}
	schema := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: view.GetActiveViewRevision(),
		PrimaryDatasetID: activePrimaryDatasetID, Engine: engineName, Columns: columns, SchemaHash: view.GetActiveViewSchemaHash(),
	}
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	s.mu.Lock()
	runtime := s.views[viewKey]
	if runtime == nil {
		runtime = newViewRuntime()
		s.views[viewKey] = runtime
	}
	s.mu.Unlock()
	runtime.mu.Lock()
	previousActive := runtime.active
	activeChanged := previousActive != "" && previousActive != view.GetActiveIndexId()
	previousGeneration := uint64(0)
	if activeChanged {
		// Capture the generation while runtime.mu still excludes PrepareViewIndex
		// for this View. Reading it after unlock could accidentally associate the
		// retired-index candidate with a newly reused slot.
		previousGeneration = s.indexGenerationOf(previousActive)
	}
	if activeChanged {
		if err := s.retireIndex(context.WithoutCancel(ctx), previousActive, previousGeneration); err != nil {
			runtime.mu.Unlock()
			return fmt.Errorf("mark displaced view index retiring: %w", err)
		}
		// 按元数据切换活动索引（例如激活响应丢失后由维护补上）与 switchViewLocked 一样继承写入围栏。
		s.inheritAppliedFence(previousActive, view.GetActiveIndexId())
	}
	s.attachActiveViewLocked(view, runtime, schema, columns, activePrimaryDatasetID, engineName)
	runtime.mu.Unlock()
	return nil
}

// attachActiveViewLocked updates the in-memory active contract. The caller
// must hold runtime.mu; it is used by both restart recovery and the short
// activation critical section.
func (s *Service) attachActiveViewLocked(view *pb.View, runtime *viewRuntime, schema viewindex.ViewIndexSchema, columns []*pb.ViewColumn, activePrimaryDatasetID, engineName string) {
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	activeChanged := runtime.active != "" && runtime.active != view.GetActiveIndexId()
	s.mu.Lock()
	s.catalogViews[viewKey] = proto.Clone(view).(*pb.View)
	s.indexEngine[view.GetActiveIndexId()] = engineName
	s.schemas[view.GetActiveIndexId()] = schema
	if runtime.active != view.GetActiveIndexId() {
		runtime.statsIndexID = ""
		runtime.stats = viewindex.ViewIndexStats{}
	}
	runtime.active = view.GetActiveIndexId()
	runtime.publishReadStateLocked()
	// Only initialize the active contract when attaching an index for the first
	// time. During a desired-metadata refresh the same physical active index is
	// re-attached with the new desired DatasetIds; replacing the snapshot here
	// would let period markers observe the next revision before activation.
	if activeChanged || !runtime.activeDatasetSet {
		runtime.activeDatasetIDs = nil
		if id := persistedActiveDatasetID(view); id != "" {
			runtime.activeDatasetIDs = []string{id}
		}
		if len(runtime.activeDatasetIDs) == 0 && view.GetDatasetId() != "" {
			runtime.activeDatasetIDs = []string{view.GetDatasetId()}
		}
		runtime.activePrimaryDatasetID = activePrimaryDatasetID
		if runtime.activePrimaryDatasetID == "" {
			runtime.activePrimaryDatasetID = view.GetDatasetId()
		}
		runtime.activeDatasetSet = true
	}
	if runtime.next == view.GetActiveIndexId() {
		// First activation attaches the index that PrepareViewIndex stored as
		// next. Clear that alias so a live row is not written twice and a
		// transient second write cannot remove the newly active index.
		runtime.next = ""
		runtime.nextDatasetIDs = nil
		runtime.nextPrimaryDatasetID = ""
		runtime.buildID = ""
		runtime.ownerID = ""
		runtime.metadata = nil
		runtime.metadataAuth = nil
		runtime.buildFailed = false
		if runtime.buildCancel != nil {
			runtime.buildCancel()
		}
		runtime.buildCancel = nil
		runtime.buildContext = nil
	}
	runtime.status = "active"
	s.indexView[view.GetActiveIndexId()] = viewKey
	s.attachOwnedDatasetMappingLocked(view.GetSpaceId(), view.GetDatasetId(), view.GetActiveIndexId())
	// An explicit zero-column projection still owns its Dataset events. Route
	// those events to the index so rows/markers can be acknowledged without
	// pretending the mapping is missing; the index write is intentionally a
	// key/attribute-only no-op for the empty schema.
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] == "true" {
		s.attachExplicitEmptyDatasetMappingsLocked(view, view.GetActiveIndexId())
	}
	s.mu.Unlock()
}

func (s *Service) attachExplicitEmptyDatasetMappingsLocked(view *pb.View, indexID string) {
	if view == nil || indexID == "" {
		return
	}
	datasetIDs := viewDatasetIDs(view)
	for _, datasetID := range datasetIDs {
		if datasetID == "" {
			continue
		}
		ref := datasetRef{spaceID: view.GetSpaceId(), datasetID: datasetID}
		if s.byData[ref] == nil {
			s.byData[ref] = make(map[string]struct{})
		}
		s.byData[ref][indexID] = struct{}{}
	}
}

func (s *Service) AttachPendingViewBuild(ctx context.Context, view *pb.View) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" || view.GetIndexBuild() == nil || view.GetIndexBuild().GetIndexId() == "" {
		return errors.New("pending view build metadata is required")
	}
	build := view.GetIndexBuild()
	engineName := strings.ToLower(strings.TrimSpace(build.GetEngine()))
	if engineName == "" {
		engineName = strings.ToLower(strings.TrimSpace(view.GetEngine()))
	}
	engine := s.engines[engineName]
	if engine == nil {
		return fmt.Errorf("view engine %q is unavailable", engineName)
	}
	stats, err := engine.Stat(ctx, build.GetIndexId())
	if err != nil {
		return err
	}
	if !stats.Exists {
		return fmt.Errorf("pending view index %q is missing", build.GetIndexId())
	}
	if expected := build.GetTargetViewVersion(); expected > 0 && stats.ViewVersion != expected {
		return fmt.Errorf("pending view index %q revision mismatch: metadata=%d physical=%d", build.GetIndexId(), expected, stats.ViewVersion)
	}
	columns := build.GetColumns()
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] != "true" {
		columns = view.GetColumns()
	}
	version := build.GetTargetViewVersion()
	if version == 0 {
		version = view.GetDesiredViewRevision()
	}
	hash := build.GetSchemaHash()
	if hash == "" {
		hash = viewindex.HashViewIndexSchema(viewindex.ViewIndexSchema{SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: version, Engine: engineName, Columns: columns, PrimaryDatasetID: view.GetDatasetId()})
	}
	physicalSchemaHash := viewindex.HashViewIndexSchema(viewindex.ViewIndexSchema{SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: version, Engine: engineName, Columns: columns, PrimaryDatasetID: view.GetDatasetId()})
	if hash != physicalSchemaHash {
		return fmt.Errorf("pending view index %q metadata schema hash is stale: build=%q desired=%q", build.GetIndexId(), hash, physicalSchemaHash)
	}
	if stats.SchemaHash != physicalSchemaHash {
		return fmt.Errorf("pending view index %q schema hash mismatch: expected=%q physical=%q", build.GetIndexId(), physicalSchemaHash, stats.SchemaHash)
	}
	schema := viewindex.ViewIndexSchema{SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: version, Engine: engineName, Columns: columns, SchemaHash: hash, PrimaryDatasetID: view.GetDatasetId()}
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	s.mu.Lock()
	runtime := s.views[viewKey]
	if runtime == nil {
		runtime = newViewRuntime()
		s.views[viewKey] = runtime
	}
	s.mu.Unlock()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	release, err := s.indexWriteGate(build.GetIndexId()).lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	// Recovery may attach a prepared slot without calling PrepareViewIndex in
	// this process. Treat it as a fresh generation so a stale cleanup candidate
	// cannot remove the recovered file.
	s.nextIndexGeneration(build.GetIndexId())
	s.mu.Lock()
	s.catalogViews[viewKey] = proto.Clone(view).(*pb.View)
	s.indexEngine[build.GetIndexId()] = engineName
	s.schemas[build.GetIndexId()] = schema
	s.indexView[build.GetIndexId()] = viewKey
	s.attachOwnedDatasetMappingLocked(view.GetSpaceId(), view.GetDatasetId(), build.GetIndexId())
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] == "true" {
		s.attachExplicitEmptyDatasetMappingsLocked(view, build.GetIndexId())
	}
	s.mu.Unlock()
	if runtime.active == "" {
		runtime.next = build.GetIndexId()
		runtime.nextDatasetIDs = viewDatasetIDs(view)
		runtime.nextPrimaryDatasetID = view.GetDatasetId()
		runtime.status = "ready"
	} else if runtime.active != build.GetIndexId() {
		runtime.next = build.GetIndexId()
		runtime.nextDatasetIDs = viewDatasetIDs(view)
		runtime.nextPrimaryDatasetID = view.GetDatasetId()
		runtime.status = "ready"
	}
	return nil
}

func (s *Service) MarkViewBuildReady(spaceID, viewID string) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errors.New("view runtime is not prepared")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.next == "" {
		runtime.status = "ready"
		return nil
	}
	runtime.status = "ready"
	return nil
}
