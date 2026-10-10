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

// liveIndexWriteTimeout is the dedicated budget for one DuckDB live upsert.
// JetStream deliveries often arrive with a short remaining deadline after a
// previous index held the write gate; inheriting that deadline turns a healthy
// write into a NAK loop and leaves View minutes behind Primary.
const liveIndexWriteTimeout = 30 * time.Second

// primaryPointReadChunkSize keeps live enrichment under the DataNode
// 10k-key / 100k key-field point-read budget. Full-market 1m batches
// otherwise fail the whole delivery with "read request exceeds key/field limit".
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

// HandleDatasetRowsBatch merges contiguous rows events for one Dataset before
// entering the per-index write gate. Markers are never included in this batch,
// so the Dataset ordering fence remains unchanged while DuckDB receives one
// larger transaction for multiple subjects.
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
		// Startup/recovery may not have attached the active or first-build index
		// yet. Keep managed Dataset deliveries pending instead of ACKing a row
		// that cannot be recovered by a later range scan. An unrelated Dataset
		// is safe to ACK after discovery confirms no active View projects it.
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
		// A previous B write may have failed while the metadata Fail RPC was
		// temporarily unavailable. Keep the Dataset delivery pending and retry
		// persisting the failure before attempting another write. Otherwise a
		// later successful redelivery could leave buildFailed set forever and
		// block activation without ever converging the metadata state.
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
			// Stat is inconclusive, so make one write attempt before deciding
			// whether the active pointer is stale. If the write itself fails and
			// a replacement is healthy, keep the delivery pending until the
			// replacement is READY and activation can make it authoritative.
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
				// A live delivery has a short deadline. A timeout while the
				// replacement is holding its per-index write gate is backpressure,
				// not evidence that the B index is corrupt. Keep the delivery pending
				// and let the next attempt retry the write; marking the whole build
				// FAILED here would make every large backfill self-cancel.
				if isTransientBuildWriteError(err) {
					runtime.mu.Unlock()
					return err
				}
				if activeErr != nil || !activeReady {
					// A first build has no authoritative A. Persist the failed
					// state before returning; the delivery itself must remain
					// pending so the replacement build can receive this row. Once
					// the FAILED state is durable, remove this inactive slot now
					// instead of waiting for the periodic maintainer. This also
					// makes a lost Fail RPC response converge on the next retry.
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
				// The replacement received the row, but the active Stat was
				// inconclusive and its write failed. Keep the delivery pending;
				// the activation fence can switch to the READY replacement.
				runtime.mu.Unlock()
				return activeFailure
			}
			if !activeReady && runtime.active == "" {
				// A from-scratch rebuild writes B from the durable stream.
				// ACK after the replacement write. Rebuilds never journal or
				// emit subject-ready; later live writes on the activated index
				// publish directly.
				runtime.mu.Unlock()
				continue
			}
			if !activeReady && runtime.status != "active" {
				// The row is present in the replacement, but it is not yet a
				// durable READY/active index. Keep the source delivery pending so
				// a crash before build metadata reaches READY cannot lose the row.
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
	// tRPC transport errors may wrap the context error in a framework error
	// whose Unwrap chain is not preserved. Keep a conservative textual check so
	// a short delivery deadline cannot mark a healthy, merely busy B index as
	// permanently failed.
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "context deadline exceeded") || strings.Contains(message, "context canceled")
}

// liveIndexReady verifies that an index pointer still names a physical
// index. Maintenance can briefly retain a stale active pointer while preparing
// its replacement; callers use this to route rows to the replacement rather
// than silently dropping them.
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
		// The event carries the newest values, so it is applied after the
		// Primary row and wins for every field both of them hold.
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

// factorAttributeKeys returns the fixed provenance attributes plus the
// per-factor source-hash keys projected by a managed result View. Primary
// attribute reads are exact-key reads, so these keys must be enumerated from
// View column metadata rather than requested with a prefix wildcard.
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

// viewColumnField returns the Dataset field a View column materializes. A
// View indexes exactly one Dataset, so a dataset column is named after the
// field it reads: column_name and origin_id both hold the bare field name.
// Metadata stores an unspecified origin type as a dataset column.
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

// viewColumnFields lists each distinct Dataset field of the columns in order.
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
