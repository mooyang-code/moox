package view

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// liveIndexWriteTimeout 是单次 DuckDB 实时写入专用的时间预算。JetStream 投递到达时，常因前一个索引占着写锁而只剩
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
	handleStart := time.Now()
	rowEvent, err := eventmapper.ToStorageRows(payload)
	if err != nil {
		return eventconsumer.Permanent(err)
	}
	applyStart := time.Now()
	if err := s.applyDatasetEvent(ctx, message.GetSpaceId(), message.GetSubjectId(), rowEvent.GetRows()); err != nil {
		return err
	}
	s.noteAppliedFromPayload(message.GetSpaceId(), payload)
	fenceStart := time.Now()
	if err := s.makeAppliedDurableBeforeAck(); err != nil {
		return fmt.Errorf("View 写入围栏落盘失败，行事件稍后重投：%w", err)
	}
	flushStart := time.Now()
	s.flushReadyAfterRows(ctx, message.GetSpaceId())
	logRowsApply(message, 1, len(rowEvent.GetRows()), handleStart, applyStart, fenceStart, flushStart)
	return nil
}

// logRowsApply 记录行事件的分段耗时：投递滞后（事件发出到本进程开始处理）、解码、写索引、围栏落盘、刷新就绪。
// 达到阈值才记，避免高频小写入刷屏。
func logRowsApply(first *eventpb.EventMessage, events, rows int, handleStart, applyStart, fenceStart, flushStart time.Time) {
	total := time.Since(handleStart)
	if total < slowIndexApplyLog {
		return
	}
	lag := time.Duration(0)
	if first != nil && first.GetOccurredAt() != nil {
		lag = handleStart.Sub(first.GetOccurredAt().AsTime())
	}
	log.Printf("storage view 行事件 dataset=%s 事件=%d 行=%d 投递滞后=%s 解码=%s 写索引=%s 围栏落盘=%s 刷新就绪=%s 本事件处理=%s",
		first.GetSubjectId(), events, rows, lag.Round(time.Millisecond), applyStart.Sub(handleStart).Round(time.Millisecond),
		fenceStart.Sub(applyStart).Round(time.Millisecond), flushStart.Sub(fenceStart).Round(time.Millisecond),
		time.Since(flushStart).Round(time.Millisecond), total.Round(time.Millisecond))
}

// flushReadyAfterRows 在行写入后顺带刷新就绪队列。行已经写入，就绪事件发布失败时留在落盘的队列里由后续刷新与
// 后台重试补发，不能让行事件因此失败重投。
func (s *Service) flushReadyAfterRows(ctx context.Context, spaceID string) {
	if err := s.FlushViewDataReady(ctx, spaceID, ""); err != nil {
		log.Printf("行写入后刷新 View 就绪队列失败（空间 %s），稍后重试：%v", spaceID, err)
	}
}

// HandleDatasetRowsBatch 在排队取索引写锁之前，把同一个数据集连续的行事件合并成一批。标记事件从不并入
// 这个批次，所以数据集的顺序围栏保持不变，而 DuckDB 能用一个更大的事务写入多个标的。
func (s *Service) HandleDatasetRowsBatch(ctx context.Context, items []eventconsumer.DatasetRowsBatchItem) error {
	if len(items) == 0 {
		return eventconsumer.Permanent(errors.New("数据集行事件批次为空"))
	}
	handleStart := time.Now()
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
	applyStart := time.Now()
	if err := s.applyDatasetEvent(ctx, spaceID, datasetID, rows); err != nil {
		return err
	}
	for _, item := range items {
		s.noteAppliedFromPayload(spaceID, item.Payload)
	}
	fenceStart := time.Now()
	if err := s.makeAppliedDurableBeforeAck(); err != nil {
		return fmt.Errorf("View 写入围栏落盘失败，行事件稍后重投：%w", err)
	}
	flushStart := time.Now()
	s.flushReadyAfterRows(ctx, spaceID)
	logRowsApply(items[0].Message, len(items), len(rows), handleStart, applyStart, fenceStart, flushStart)
	return nil
}

// slowIndexApplyLog 是行写入分段耗时日志的阈值：只记录达到该耗时的写入，避免高频小写入刷屏。
const slowIndexApplyLog = 50 * time.Millisecond

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
		s.mu.RLock()
		runtime := s.views[viewKey]
		s.mu.RUnlock()
		if runtime == nil {
			continue
		}
		runtime.mu.Lock()
		target := runtime.writeTargetLocked()
		if target == "" {
			runtime.mu.Unlock()
			return errors.New("View 索引映射尚未就绪")
		}
		// 索引统计不能下结论时直接试写，由写入结果决定。
		if ready, err := s.liveIndexReady(ctx, target); err == nil && !ready {
			runtime.mu.Unlock()
			return fmt.Errorf("View 的索引 %q 不可用", target)
		}
		writtenRows, err := s.applyEventToIndex(ctx, target, datasetID, rows)
		if err != nil {
			log.Printf("storage view 索引写入失败 space=%s view=%s index=%s dataset=%s: %v", viewKey.spaceID, viewKey.viewID, target, datasetID, err)
			runtime.mu.Unlock()
			return err
		}
		// 首次建索引期间不发布水位：建成时统一补一次。
		if target == runtime.active && len(writtenRows) > 0 {
			s.observeViewWatermark(target, datasetID, writtenRows, false)
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

// liveIndexReady 确认一个索引指针仍然指向物理索引。
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
	applyStart := time.Now()
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
	s.mu.RUnlock()
	// 事件里 View 还没扩展出来的新列直接裁掉（按 schema 投影），不等待、不补数：见设计 3.4 的系统边界。
	writes := eventWrites(schema, datasetID, rows)
	if len(writes) == 0 {
		return nil, nil
	}
	complete, incomplete := partitionCompleteWrites(schema, writes)
	recoverStart := time.Now()
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
	writeStart := time.Now()
	if err := s.writeIndex(ctx, id, engine, viewindex.ViewIndexWriteBatch{RowWrites: complete, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.LiveWrite}); err != nil {
		return nil, err
	}
	if total := time.Since(applyStart); total >= slowIndexApplyLog {
		// 分段耗时：准备 = 校验与构造写入；补读 = 从 Primary 补全缺列的行；写入 = 等索引写锁与 DuckDB 事务。
		log.Printf("storage view 行写入索引较慢 index=%s dataset=%s 开始于=%s 行=%d 写入行=%d 补读行=%d 准备=%s 补读=%s 写入=%s 合计=%s",
			id, datasetID, applyStart.Format("15:04:05.000"), len(rows), len(complete), len(incomplete), recoverStart.Sub(applyStart).Round(time.Millisecond),
			writeStart.Sub(recoverStart).Round(time.Millisecond), time.Since(writeStart).Round(time.Millisecond), total.Round(time.Millisecond))
	}
	return rowsWrittenByIndexKeys(rows, complete, schema.PrimaryDatasetID), nil
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
	values := make(map[string]*pb.RowFieldValues)
	present := make(map[string]struct{})
	chunkSize := primaryPointReadChunkSize(len(fieldIDs), len(keys))
	for chunkStart := 0; chunkStart < len(keys); chunkStart += chunkSize {
		chunkEnd := chunkStart + chunkSize
		if chunkEnd > len(keys) {
			chunkEnd = len(keys)
		}
		rsp, err := reader.ReadRows(ctx, &pb.PrimaryReadRowsReq{AuthInfo: auth, Keys: keys[chunkStart:chunkEnd], ColumnNames: fieldIDs})
		if err != nil {
			return nil, err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, row := range rsp.GetRows() {
			// ReadRows 只返回存在的行：返回即存在，存在但没有请求的列时只有键。
			if row != nil && row.GetKey() != nil {
				values[viewindex.RowKeyID(row.GetKey())] = row
				present[viewindex.RowKeyID(row.GetKey())] = struct{}{}
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
		}
		// 事件带的是最新的值，所以在 Primary 行之后应用，两者都有的字段以事件为准。
		if event := eventRows[primaryID]; event != nil {
			fields = append(fields, event.GetFields()...)
		}
		for _, fieldID := range fieldIDs {
			complete.Fields = appendMatchingField(complete.Fields, fields, fieldID)
		}
		result = append(result, complete)
	}
	return result, nil
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
		if len(fields) != 0 {
			key := proto.Clone(row.GetKey()).(*pb.RowKey)
			key.DatasetId = owned
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: key}, Fields: fields})
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
