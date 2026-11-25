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

// observeViewWatermark 按刚写进索引的行推进 Monitor 用的输出水位与各标的的最新一根。
func (s *Service) observeViewWatermark(spec *viewSpec, rows []*pb.RowFieldUpsert) {
	if s == nil || s.metrics == nil || spec == nil || len(rows) == 0 {
		return
	}
	spaceID, datasetID := spec.SpaceID, spec.DatasetID
	var watermark time.Time
	type seriesKey struct {
		subjectID, frequency, seriesTag string
	}
	seriesWatermarks := make(map[seriesKey]time.Time)
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
		key := seriesKey{subjectID: subjectID, frequency: rowFrequency, seriesTag: seriesTag}
		if previous, ok := seriesWatermarks[key]; !ok || at.After(previous) {
			seriesWatermarks[key] = at
		}
	}
	if spec.timeSeries() && !watermark.IsZero() {
		s.metrics.ObserveViewOutputWatermark(spaceID, datasetID, spec.Freq, watermark)
	}
	// 跟踪的序列达到上限时只在第一次返回错误，这里不会每次写入、每条序列都打日志。
	for key, dataTime := range seriesWatermarks {
		if err := s.metrics.ObserveViewDatasetOutput(observability.ViewDatasetObservation{
			SpaceID: spaceID, DatasetID: datasetID, SubjectID: key.subjectID,
			Frequency: key.frequency, SeriesTag: key.seriesTag, DataTime: dataTime,
		}); err != nil {
			log.Warnf("storage view 数据集输出观测失败 space=%s dataset=%s subject=%s freq=%s series_tag=%s: %v", spaceID, datasetID, key.subjectID, key.frequency, key.seriesTag, err)
		}
	}
}
