package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/eventmapper"
	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view/eventconsumer"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// liveIndexWriteTimeout 是单次 DuckDB 实时写入专用的时间预算。JetStream 投递到达时，常因前一个索引占着写入闸门而只剩
// 很短的截止时间；继承这个截止时间会让健康的写入变成不断 NAK 的循环，并让 View 落后 Primary 数分钟。
const liveIndexWriteTimeout = 30 * time.Second

// primaryPointReadChunkSize 让实时补全留在 DataNode 的点读预算之内（1 万个键、10 万个键字段）。全市场 1m 的批次
// 否则会因为“read request exceeds key/field limit”让整条投递失败。
func primaryPointReadChunkSize(fieldCount, keyCount int) int {
	chunkSize := keyCount
	if fieldCount > 0 && chunkSize > 100000/fieldCount {
		chunkSize = 100000 / fieldCount
		if chunkSize == 0 {
			chunkSize = 1
		}
	}
	if chunkSize > 512 {
		chunkSize = 512
	}
	if chunkSize < 1 {
		chunkSize = 1
	}
	return chunkSize
}

func liveIndexWriteContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return nil, func() {}, ctx.Err()
	}
	base := context.TODO()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	writeCtx, cancel := context.WithTimeout(base, liveIndexWriteTimeout)
	return writeCtx, cancel, nil
}

func (s *Service) HandleDatasetRows(ctx context.Context, message *eventpb.EventMessage, payload *storagepb.DatasetRowsUpserted) error {
	if message == nil || payload == nil {
		return eventconsumer.Permanent(errors.New("数据集行事件为空"))
	}
	rowEvent, err := eventmapper.ToStorageRows(payload)
	if err != nil {
		return eventconsumer.Permanent(err)
	}
	if err := s.applyDatasetEvent(ctx, message.GetSpaceId(), message.GetSubjectId(), rowEvent.GetRows()); err != nil {
		return err
	}
	s.noteAppliedFromPayload(message.GetSpaceId(), payload)
	s.flushReadyAfterRows(ctx, message.GetSpaceId())
	return nil
}

// flushReadyAfterRows 在行写入后顺带刷新就绪队列。行已经写入，就绪事件发布失败时留在落盘的队列里由后续刷新与
// 后台重试补发，不能让行事件因此失败重投。
func (s *Service) flushReadyAfterRows(ctx context.Context, spaceID string) {
	if err := s.FlushViewDataReady(ctx, spaceID, ""); err != nil {
		log.Printf("行写入后刷新 View 就绪队列失败（空间 %s），稍后重试：%v", spaceID, err)
	}
}

// HandleDatasetRowsBatch 在进入按索引的写入闸门之前，把同一个数据集连续的行事件合并成一批。标记事件从不并入
// 这个批次，所以数据集的顺序围栏保持不变，而 DuckDB 能用一个更大的事务写入多个标的。
func (s *Service) HandleDatasetRowsBatch(ctx context.Context, items []eventconsumer.DatasetRowsBatchItem) error {
	if len(items) == 0 {
		return eventconsumer.Permanent(errors.New("数据集行事件批次为空"))
	}
	spaceID, datasetID := "", ""
	rows := make([]*pb.RowFieldUpsert, 0)
	for index, item := range items {
		if item.Message == nil || item.Payload == nil {
			return eventconsumer.Permanent(fmt.Errorf("数据集行事件批次的第 %d 条为空", index))
		}
		if index == 0 {
			spaceID, datasetID = item.Message.GetSpaceId(), item.Message.GetSubjectId()
		}
		if item.Message.GetSpaceId() != spaceID || item.Message.GetSubjectId() != datasetID {
			return eventconsumer.Permanent(errors.New("数据集行事件批次跨越了不同数据集的队列"))
		}
		if item.Payload.GetSpaceId() != spaceID || item.Payload.GetDatasetId() != datasetID {
			return eventconsumer.Permanent(errors.New("数据集行事件批次的载荷与消息的空间或数据集不一致"))
		}
		rowEvent, err := eventmapper.ToStorageRows(item.Payload)
		if err != nil {
			return eventconsumer.Permanent(err)
		}
		rows = append(rows, rowEvent.GetRows()...)
	}
	if len(rows) == 0 {
		return eventconsumer.Permanent(errors.New("数据集行事件批次没有任何行"))
	}
	if err := s.applyDatasetEvent(ctx, spaceID, datasetID, rows); err != nil {
		return err
	}
	for _, item := range items {
		s.noteAppliedFromPayload(spaceID, item.Payload)
	}
	s.flushReadyAfterRows(ctx, spaceID)
	return nil
}

func (s *Service) applyDatasetEvent(ctx context.Context, spaceID, datasetID string, rows []*pb.RowFieldUpsert) error {
	s.mu.RLock()
	ref := datasetRef{spaceID: spaceID, datasetID: datasetID}
	viewKeys := make(map[viewRef]struct{})
	var standalone []string
	for id := range s.byData[ref] {
		if viewKey, ok := s.indexView[id]; ok {
			viewKeys[viewKey] = struct{}{}
		} else {
			standalone = append(standalone, id)
		}
	}
	s.mu.RUnlock()
	if len(viewKeys) == 0 && len(standalone) == 0 {
		// 启动或恢复期间，活动索引或首次构建的索引可能还没有挂载。受管理的数据集的投递保持未确认，不能确认一行之后的
		// 范围扫描无法找回的数据；与任何活动 View 都无关的数据集，在发现流程确认没有活动 View 投影它之后可以安全确认。
		managed, err := s.datasetHasActiveView(ctx, spaceID, datasetID)
		if err != nil {
			return err
		}
		if managed {
			return errors.New("View 索引映射尚未就绪")
		}
		return nil
	}
	for viewKey := range viewKeys {
		activeWatermarkRows := make(map[string][]*pb.RowFieldUpsert, 1)
		s.mu.RLock()
		runtime := s.views[viewKey]
		s.mu.RUnlock()
		if runtime == nil {
			continue
		}
		runtime.mu.Lock()
		// 上一次写 B 索引可能失败了，而元数据的 Fail 调用当时暂时不可用。数据集的投递保持未确认，先重试持久化这次失败，
		// 再尝试下一次写入；否则之后一次成功的重投会让 buildFailed 一直置位，卡住激活，元数据状态也永远无法收敛。
		if runtime.buildFailed && runtime.next != "" {
			failedID := runtime.next
			failedGeneration := s.indexGenerationOf(failedID)
			if failErr := s.failRuntimeBuild(ctx, viewKey, runtime, errors.New("重试已失败的替换构建")); failErr != nil {
				runtime.mu.Unlock()
				return failErr
			}
			runtime.next = ""
			runtime.nextDatasetIDs = nil
			runtime.nextPrimaryDatasetID = ""
			runtime.buildFailed = false
			runtime.buildID = ""
			runtime.ownerID = ""
			runtime.buildCancel = nil
			if runtime.active == "" {
				runtime.status = "failed"
			} else {
				runtime.status = "active"
			}
			runtime.mu.Unlock()
			s.removeFailedBuildAtGeneration(ctx, failedID, failedGeneration)
			return errors.New("View 的替换构建已失败")
		}
		activeID, nextID := runtime.active, runtime.next
		activeReady, activeErr := s.liveIndexReady(ctx, activeID)
		var activeFailure error
		if activeErr != nil && activeID != "" {
			// 索引统计不能下结论，所以先做一次写入尝试，再判断活动指针是否已失效。如果写入本身失败而替换索引是健康的，
			// 投递保持未确认，直到替换索引就绪、激活后成为权威索引。
			writtenRows, err := s.applyEventToIndex(ctx, activeID, datasetID, rows)
			if err == nil {
				activeReady, activeErr = true, nil
				if len(writtenRows) > 0 {
					activeWatermarkRows[activeID] = append(activeWatermarkRows[activeID], writtenRows...)
				}
			} else if nextID != "" {
				log.Printf("storage view 活动索引不可用而替换索引已就绪，改写替换索引 space=%s view=%s index=%s: %v", viewKey.spaceID, viewKey.viewID, activeID, err)
				activeFailure = err
				activeReady, activeErr = false, nil
			} else {
				runtime.mu.Unlock()
				return err
			}
		}
		if activeErr != nil && nextID == "" {
			runtime.mu.Unlock()
			return activeErr
		}
		if activeErr == nil && !activeReady && nextID == "" {
			runtime.mu.Unlock()
			return fmt.Errorf("View 的活动索引 %q 不可用", activeID)
		}
		if activeErr == nil && activeReady {
			writtenRows, err := s.applyEventToIndex(ctx, activeID, datasetID, rows)
			if err != nil {
				log.Printf("storage view 活动索引写入失败 space=%s view=%s index=%s dataset=%s: %v", viewKey.spaceID, viewKey.viewID, activeID, datasetID, err)
				if nextID == "" {
					runtime.mu.Unlock()
					return err
				}
				// 轻量的存在检查只能证明索引目录在；活动索引损坏或不可写时，先把这一行写进替换索引，
				// 本次投递保持未确认，等替换索引激活。
				activeFailure = err
				activeReady = false
			} else if len(writtenRows) > 0 {
				activeWatermarkRows[activeID] = append(activeWatermarkRows[activeID], writtenRows...)
			}
		} else if activeErr == nil {
			// 准备替换索引期间崩溃，可能留下失效的活动指针。不能什么都没写就确认这一行：继续写健康的替换索引，
			// 激活时排空消费者，保持“先行后标记”的顺序。
			log.Printf("storage view 活动索引不可用，实时行写入替换索引 space=%s view=%s index=%s", viewKey.spaceID, viewKey.viewID, nextID)
		}
		// 先发布活动索引已写成功的水位，再处理替换索引：替换索引失败不能让新鲜度监控看不到权威索引已提交的数据。
		for indexID, writtenRows := range activeWatermarkRows {
			s.observeViewWatermark(indexID, datasetID, writtenRows, false)
		}
		if nextID != "" {
			_, nextWriteErr := s.applyEventToIndex(ctx, nextID, datasetID, rows)
			if err := nextWriteErr; err != nil {
				failedID := nextID
				failedGeneration := s.indexGenerationOf(failedID)
				// 实时投递的截止时间很短。替换索引占着它的写入闸门时发生的超时是背压，不能证明 B 索引已损坏。投递保持未确认，
				// 下一次尝试再重试写入；在这里把整个构建标记为 FAILED，会让每一次大批量回填自我取消。
				if isTransientBuildWriteError(err) {
					runtime.mu.Unlock()
					return err
				}
				if activeErr != nil || !activeReady {
					// 首次构建没有权威的 A 索引。先持久化失败状态再返回；投递本身保持未确认，好让替换构建收到这一行。失败状态
					// 落盘之后，立即删掉这个未启用的槽位，不等周期性的维护器；这也让丢失了 Fail 响应的情形在下一次重试时收敛。
					if failErr := s.failRuntimeBuild(ctx, viewKey, runtime, err); failErr != nil {
						runtime.mu.Unlock()
						return errors.Join(err, failErr)
					}
					runtime.next = ""
					runtime.nextDatasetIDs = nil
					runtime.nextPrimaryDatasetID = ""
					if runtime.active == "" {
						runtime.status = "failed"
					} else {
						runtime.status = "active"
					}
					runtime.mu.Unlock()
					s.removeFailedBuildAtGeneration(ctx, failedID, failedGeneration)
					return err
				}
				if failErr := s.failRuntimeBuild(ctx, viewKey, runtime, err); failErr != nil {
					runtime.mu.Unlock()
					return errors.Join(err, failErr)
				}
				runtime.next = ""
				runtime.status = "failed"
				runtime.mu.Unlock()
				s.removeFailedBuildAtGeneration(ctx, failedID, failedGeneration)
				continue
			}
			if activeFailure != nil {
				// 替换索引收到了这一行，但活动索引的统计不能下结论、它的写入失败了。投递保持未确认；激活围栏可以切到
				// 已就绪的替换索引。
				runtime.mu.Unlock()
				return activeFailure
			}
			if !activeReady && runtime.active == "" {
				// 从零开始的重建从持久化的消息流写 B 索引。替换索引写入后确认投递。重建从不记录日志、也不发出 subject-ready；
				// 激活之后的实时写入直接发布。
				runtime.mu.Unlock()
				continue
			}
			if !activeReady && runtime.status != "active" {
				// 这一行已经在替换索引里，但它还不是持久化的就绪或活动索引。源投递保持未确认，这样构建元数据到达就绪之前
				// 崩溃也不会丢掉这一行。
				runtime.mu.Unlock()
				return fmt.Errorf("View 的替换索引 %q 等待激活", nextID)
			}
		}
		runtime.mu.Unlock()
	}
	for _, id := range standalone {
		writtenRows, err := s.applyEventToIndex(ctx, id, datasetID, rows)
		if err != nil {
			return err
		}
		if len(writtenRows) > 0 {
			s.observeActiveViewWatermark(id, datasetID, writtenRows)
		}
	}
	return nil
}

func isTransientBuildWriteError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	// tRPC 的传输错误可能把 context 错误包在框架错误里，Unwrap 链没有保留。保守地做一次文本检查，免得很短的
	// 投递截止时间把健康、只是繁忙的 B 索引标成永久失败。
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "context deadline exceeded") || strings.Contains(message, "context canceled")
}

// liveIndexReady 确认一个索引指针仍然指向物理索引。维护在准备替换索引期间，活动指针可能短暂保留着已失效的值；
// 调用方据此把行路由到替换索引，而不是悄悄丢掉。
func (s *Service) liveIndexReady(ctx context.Context, indexID string) (bool, error) {
	if indexID == "" {
		return false, nil
	}
	probeCtx, cancel, err := liveIndexWriteContext(ctx)
	if err != nil {
		return false, err
	}
	defer cancel()
	ctx = probeCtx
	s.mu.RLock()
	if len(s.engines) == 0 {
		s.mu.RUnlock()
		return true, nil
	}
	s.mu.RUnlock()
	engine, err := s.engineFor(indexID)
	if err != nil {
		if errors.Is(err, errViewIndexNotReady) {
			return false, nil
		}
		return false, err
	}
	if checker, ok := engine.(viewindex.ExistenceChecker); ok {
		return checker.Exists(ctx, indexID)
	}
	stats, err := engine.Stat(ctx, indexID)
	if err != nil {
		return false, err
	}
	return stats.Exists, nil
}

func (s *Service) applyEventToIndex(ctx context.Context, id, datasetID string, rows []*pb.RowFieldUpsert) ([]*pb.RowFieldUpsert, error) {
	if id == "" {
		return nil, nil
	}
	writeCtx, cancel, err := liveIndexWriteContext(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	ctx = writeCtx
	engine, err := s.engineFor(id)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	schema := s.schemas[id]
	viewKey, mapped := s.indexView[id]
	var catalogView *pb.View
	if mapped {
		catalogView = s.catalogViews[viewKey]
	}
	s.mu.RUnlock()
	if err := validateFactorResultEventColumns(catalogView, schema, datasetID, rows); err != nil {
		return nil, err
	}
	writes := eventWrites(schema, datasetID, rows)
	if len(writes) == 0 {
		return nil, nil
	}
	complete, incomplete := partitionCompleteWrites(schema, writes)
	if len(incomplete) > 0 {
		recovered, err := s.recoverMissingRows(ctx, schema, rows, incomplete)
		if err != nil {
			return nil, err
		}
		complete = append(complete, recovered...)
	}
	if len(complete) == 0 {
		return nil, nil
	}
	if err := s.writeIndex(ctx, id, engine, viewindex.ViewIndexWriteBatch{RowWrites: complete, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.LiveWrite}); err != nil {
		return nil, err
	}
	return rowsWrittenByIndexKeys(rows, complete, schema.PrimaryDatasetID), nil
}

func validateFactorResultEventColumns(view *pb.View, schema viewindex.ViewIndexSchema, datasetID string, rows []*pb.RowFieldUpsert) error {
	if view == nil || !strings.EqualFold(strings.TrimSpace(view.GetAttributes()["primary_dataset_role"]), "factor_result") ||
		datasetID != strings.TrimSpace(schema.PrimaryDatasetID) {
		return nil
	}
	known := make(map[string]struct{}, len(schema.Columns))
	for _, column := range schema.Columns {
		if field := viewColumnField(column); field != "" {
			known[field] = struct{}{}
		}
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		for _, field := range row.GetFields() {
			if field == nil {
				continue
			}
			if _, ok := known[field.GetFieldId()]; !ok {
				return fmt.Errorf("因子结果字段 %q 不在活动 View 的 schema 中，等 schema 维护完成后重试", field.GetFieldId())
			}
		}
	}
	return nil
}

func rowsWrittenByIndexKeys(rows []*pb.RowFieldUpsert, writes []viewindex.RowWrite, primaryDatasetID string) []*pb.RowFieldUpsert {
	byKey := make(map[string][]*pb.RowFieldUpsert, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		key := proto.Clone(row.GetKey()).(*pb.RowKey)
		if primaryDatasetID != "" {
			key.DatasetId = primaryDatasetID
		}
		byKey[viewindex.RowKeyID(key)] = append(byKey[viewindex.RowKeyID(key)], row)
	}
	result := make([]*pb.RowFieldUpsert, 0, len(writes))
	seen := make(map[string]struct{}, len(writes))
	for _, write := range writes {
		if write.Key.Key == nil {
			continue
		}
		key := viewindex.RowKeyID(write.Key.Key)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, byKey[key]...)
	}
	return result
}

func (s *Service) observeActiveViewWatermark(indexID, datasetID string, rows []*pb.RowFieldUpsert) {
	s.observeViewWatermark(indexID, datasetID, rows, true)
}

func (s *Service) observeViewWatermark(indexID, datasetID string, rows []*pb.RowFieldUpsert, requireActive bool) {
	if s == nil || s.metrics == nil || indexID == "" || len(rows) == 0 {
		return
	}
	s.mu.RLock()
	viewKey, ok := s.indexView[indexID]
	runtime := s.views[viewKey]
	view := s.catalogViews[viewKey]
	s.mu.RUnlock()
	if !ok || runtime == nil || view == nil {
		return
	}
	if requireActive {
		runtime.mu.Lock()
		isActive := runtime.active == indexID
		runtime.mu.Unlock()
		if !isActive {
			return
		}
	}
	spaceID := strings.TrimSpace(view.GetSpaceId())
	viewID := strings.TrimSpace(view.GetViewId())
	if spaceID == "" || viewID == "" {
		return
	}
	frequency := view.GetFreq()
	var watermark time.Time
	type datasetKey struct {
		subjectID, frequency, seriesTag string
	}
	datasetWatermarks := make(map[datasetKey]time.Time)
	for _, row := range rows {
		if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
			continue
		}
		timeSeries := row.GetKey().GetTimeSeries()
		at, err := time.Parse(time.RFC3339Nano, timeSeries.GetDataTime())
		if err != nil {
			continue
		}
		if at.After(watermark) {
			watermark = at
		}
		subjectID := strings.TrimSpace(timeSeries.GetSubjectId())
		rowFrequency := strings.TrimSpace(timeSeries.GetFreq())
		if subjectID == "" || rowFrequency == "" {
			continue
		}
		seriesTag := strings.TrimSpace(timeSeries.GetSeriesTag())
		if seriesTag == "" {
			seriesTag = "default"
		}
		key := datasetKey{subjectID: subjectID, frequency: rowFrequency, seriesTag: seriesTag}
		if previous, ok := datasetWatermarks[key]; !ok || at.After(previous) {
			datasetWatermarks[key] = at
		}
	}
	if frequency != "" && !watermark.IsZero() {
		s.metrics.ObserveViewOutputWatermark(spaceID, viewID, datasetID, frequency, watermark)
	}
	for key, dataTime := range datasetWatermarks {
		if err := s.metrics.ObserveViewDatasetOutput(observability.ViewDatasetObservation{
			SpaceID: spaceID, ViewID: viewID, DatasetID: datasetID, SubjectID: key.subjectID,
			Frequency: key.frequency, SeriesTag: key.seriesTag, DataTime: dataTime,
		}); err != nil {
			log.Printf("storage view 数据集输出观测失败 space=%s view=%s dataset=%s subject=%s freq=%s series_tag=%s: %v", spaceID, viewID, datasetID, key.subjectID, key.frequency, key.seriesTag, err)
		}
	}
}

func partitionCompleteWrites(schema viewindex.ViewIndexSchema, writes []viewindex.RowWrite) (complete, incomplete []viewindex.RowWrite) {
	required := make(map[string]struct{}, len(schema.Columns))
	for _, column := range schema.Columns {
		if column != nil && column.GetColumnName() != "" {
			required[column.GetColumnName()] = struct{}{}
		}
	}
	for _, write := range writes {
		present := make(map[string]struct{}, len(write.Fields))
		for _, field := range write.Fields {
			if field != nil {
				present[field.GetFieldId()] = struct{}{}
			}
		}
		isComplete := len(required) > 0
		for name := range required {
			if _, ok := present[name]; !ok {
				isComplete = false
				break
			}
		}
		if isComplete {
			complete = append(complete, write)
		} else {
			incomplete = append(incomplete, write)
		}
	}
	return complete, incomplete
}

func (s *Service) recoverMissingRows(ctx context.Context, schema viewindex.ViewIndexSchema, eventRowsInput []*pb.RowFieldUpsert, writes []viewindex.RowWrite) ([]viewindex.RowWrite, error) {
	s.mu.RLock()
	reader := s.primary
	auth := s.primaryAuth
	if auth != nil {
		auth = proto.Clone(auth).(*pb.AuthInfo)
	}
	s.mu.RUnlock()
	if reader == nil || auth == nil {
		return nil, errors.New("恢复缺失的 View 行需要 Primary 读取客户端与鉴权")
	}
	datasetID := strings.TrimSpace(schema.PrimaryDatasetID)
	fieldIDs := viewColumnFields(schema.Columns)
	if datasetID == "" || len(fieldIDs) == 0 {
		return nil, nil
	}
	eventRows := make(map[string]*pb.RowFieldUpsert, len(eventRowsInput))
	for _, event := range eventRowsInput {
		if event == nil || event.GetKey() == nil {
			continue
		}
		key := proto.Clone(event.GetKey()).(*pb.RowKey)
		key.DatasetId = datasetID
		eventRows[viewindex.RowKeyID(key)] = event
	}
	keys := make([]*pb.RowKey, 0, len(writes))
	for _, write := range writes {
		key := proto.Clone(write.Key.Key).(*pb.RowKey)
		key.DatasetId = datasetID
		keys = append(keys, key)
	}
	attributeKeys := factorAttributeKeys(schema.Columns)
	values := make(map[string]*pb.RowFieldValues)
	present := make(map[string]struct{})
	chunkSize := primaryPointReadChunkSize(len(fieldIDs)+len(attributeKeys), len(keys))
	for chunkStart := 0; chunkStart < len(keys); chunkStart += chunkSize {
		chunkEnd := chunkStart + chunkSize
		if chunkEnd > len(keys) {
			chunkEnd = len(keys)
		}
		rsp, err := reader.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: auth, Keys: keys[chunkStart:chunkEnd], FieldIds: fieldIDs, AttributeKeys: attributeKeys})
		if err != nil {
			return nil, err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, row := range rsp.GetRows() {
			if row != nil && row.GetKey() != nil && (len(row.GetFields()) != 0 || len(row.GetAttributes()) != 0) {
				values[viewindex.RowKeyID(row.GetKey())] = row
				present[viewindex.RowKeyID(row.GetKey())] = struct{}{}
			}
		}
		for _, key := range rsp.GetExistingKeys() {
			if key != nil {
				present[viewindex.RowKeyID(key)] = struct{}{}
			}
		}
	}
	result := make([]viewindex.RowWrite, 0, len(writes))
	for index, write := range writes {
		primaryID := viewindex.RowKeyID(keys[index])
		if _, ok := present[primaryID]; !ok {
			continue
		}
		complete := viewindex.RowWrite{Key: write.Key}
		var fields []*pb.FieldValue
		if row := values[primaryID]; row != nil {
			fields = append(fields, row.GetFields()...)
			if len(row.GetAttributes()) > 0 {
				complete.Attributes = make(map[string]*pb.TypedValue, len(row.GetAttributes()))
			}
			mergeRowAttributes(complete.Attributes, row.GetAttributes())
		}
		// 事件带的是最新的值，所以在 Primary 行之后应用，两者都有的字段以事件为准。
		if event := eventRows[primaryID]; event != nil {
			fields = append(fields, event.GetFields()...)
			if complete.Attributes == nil && len(event.GetAttributes()) > 0 {
				complete.Attributes = make(map[string]*pb.TypedValue, len(event.GetAttributes()))
			}
			mergeRowAttributes(complete.Attributes, event.GetAttributes())
		}
		for _, fieldID := range fieldIDs {
			complete.Fields = appendMatchingField(complete.Fields, fields, fieldID)
		}
		result = append(result, complete)
	}
	return result, nil
}

// factorAttributeKeys 返回固定的来源属性，加上受管理的结果 View 投影的每个因子的源哈希键。Primary 的属性读取是
// 按确切的键读取，所以这些键必须从 View 的列元数据里枚举出来，不能用前缀通配符请求。
func factorAttributeKeys(columns []*pb.ViewColumn) []string {
	seen := map[string]struct{}{
		"factor.source_hash":    {},
		"factor.id":             {},
		"factor.parent_task_id": {},
		"factor.computed_at":    {},
	}
	for _, column := range columns {
		if viewColumnField(column) == "" {
			continue
		}
		if factorID := strings.TrimSpace(column.GetAttributes()["origin_factor_id"]); factorID != "" {
			seen["factor.source_hash."+factorID] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for key := range seen {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func appendMatchingField(dst, fields []*pb.FieldValue, fieldID string) []*pb.FieldValue {
	for _, field := range fields {
		if field != nil && field.GetFieldId() == fieldID {
			value := &pb.FieldValue{FieldId: fieldID, Value: field.GetValue()}
			for index, existing := range dst {
				if existing != nil && existing.GetFieldId() == fieldID {
					dst[index] = value
					value = nil
					break
				}
			}
			if value != nil {
				dst = append(dst, value)
			}
		}
	}
	return dst
}

func mergeRowAttributes(dst, src map[string]*pb.TypedValue) {
	if len(src) == 0 {
		return
	}
	if dst == nil {
		return
	}
	for name, value := range src {
		if value != nil {
			dst[name] = value
		}
	}
}

func eventWrites(schema viewindex.ViewIndexSchema, datasetID string, rows []*pb.RowFieldUpsert) []viewindex.RowWrite {
	owned := strings.TrimSpace(schema.PrimaryDatasetID)
	if owned == "" || datasetID != owned {
		return nil
	}
	columns := make(map[string]struct{}, len(schema.Columns))
	for _, field := range viewColumnFields(schema.Columns) {
		columns[field] = struct{}{}
	}
	if len(columns) == 0 {
		return nil
	}
	writes := make([]viewindex.RowWrite, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		fields := make([]*pb.FieldValue, 0, len(row.GetFields()))
		for _, field := range row.GetFields() {
			if _, ok := columns[field.GetFieldId()]; ok {
				fields = append(fields, &pb.FieldValue{FieldId: field.GetFieldId(), Value: field.GetValue()})
			}
		}
		if len(fields) != 0 || len(row.GetAttributes()) != 0 {
			key := proto.Clone(row.GetKey()).(*pb.RowKey)
			key.DatasetId = owned
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: key}, Fields: fields, Attributes: row.GetAttributes()})
		}
	}
	return writes
}

// viewColumnField 返回 View 的一列物化的数据集字段。一个 View 只索引一个数据集，所以数据集列以它读取的字段命名：
// column_name 与 origin_id 都是字段名本身。元数据把未指定的来源类型当作数据集列。
func viewColumnField(column *pb.ViewColumn) string {
	if column == nil {
		return ""
	}
	switch column.GetOriginType() {
	case pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM, pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_EXPRESSION:
		return ""
	}
	return strings.TrimSpace(column.GetOriginId())
}

// viewColumnFields 按顺序列出各列对应的、去重后的数据集字段。
func viewColumnFields(columns []*pb.ViewColumn) []string {
	seen := make(map[string]struct{}, len(columns))
	fields := make([]string, 0, len(columns))
	for _, column := range columns {
		field := viewColumnField(column)
		if field == "" {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		fields = append(fields, field)
	}
	return fields
}
