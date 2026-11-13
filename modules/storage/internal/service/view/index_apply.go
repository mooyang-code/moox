package view

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"

	"trpc.group/trpc-go/trpc-go/log"
)

// liveIndexWriteTimeout 是单次 DuckDB 实时写入专用的时间预算：调用方的上下文可能因等前一个写锁只剩很短的截止时间，
// 继承它会让健康的写入反复超时重试。
const liveIndexWriteTimeout = 30 * time.Second

// primaryPointReadChunkSize 让按键回读留在 Pebble 的点读预算之内（1 万个键、10 万个键字段）。全市场 1m 的批次
// 否则会因为“read request exceeds key/field limit”整批失败。
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

// slowIndexApplyLog 是行写入分段耗时日志的阈值：只记录达到该耗时的写入，避免高频小写入刷屏。
const slowIndexApplyLog = 50 * time.Millisecond

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
	// 跟踪的序列达到上限时只在第一次返回错误，这里不会每次写入、每条序列都打日志。
	for key, dataTime := range datasetWatermarks {
		if err := s.metrics.ObserveViewDatasetOutput(observability.ViewDatasetObservation{
			SpaceID: spaceID, ViewID: viewID, DatasetID: datasetID, SubjectID: key.subjectID,
			Frequency: key.frequency, SeriesTag: key.seriesTag, DataTime: dataTime,
		}); err != nil {
			log.Warnf("storage view 数据集输出观测失败 space=%s view=%s dataset=%s subject=%s freq=%s series_tag=%s: %v", spaceID, viewID, datasetID, key.subjectID, key.frequency, key.seriesTag, err)
		}
	}
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
