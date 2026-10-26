package observability

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
)

// ErrInvalidArrivalQuery 表示到达历史的查询参数不合法（缺链路、周期无效、日期格式错或在未来）。
var ErrInvalidArrivalQuery = errors.New("到达历史查询参数无效")

// ArrivalHistoryQuery 选一条数据链路（源数据集，即入库的数据集，加周期）和一天。
type ArrivalHistoryQuery struct {
	SpaceID, DatasetID, Freq string
	// TaskID 选定因子任务：给了就查它的结果数据集；没给且源数据集上只有一个任务，就查这个任务；有多个任务时只给任务列表和源数据集这一段。
	TaskID string
	// Day 是 YYYY-MM-DD，按市场时区（加密货币 UTC，A 股 Asia/Shanghai）的自然日；为空取市场时区的今天。
	Day string
}

// ArrivalPoint 是一根 K 线在一个环节的到达情况。
type ArrivalPoint struct {
	Close        time.Time
	DelaySeconds int64
	// SkippedBars 是它和上一份快照里的水位之间跳过的根数：补数据时一批写完，中间几根不单独记录。
	SkippedBars int
}

// ArrivalSeries 是链路上一个环节的一个数据集一天的到达曲线。
type ArrivalSeries struct {
	// Stage 是环节：storage（入库）、view（视图）、factor（因子）、factor_view（因子视图）、strategy（策略计算）。
	Stage, Producer, DatasetID, PrimaryDatasetID string
	LatenessLimitSeconds                         int64
	Points                                       []ArrivalPoint
}

// ArrivalHistory 是一条链路一天的到达曲线：时间点按收线时间落在 [Start, End) 内。
type ArrivalHistory struct {
	// Timezone 是市场时区的 IANA 名称，Day 按它切分。
	Timezone   string
	Day        string
	Start, End time.Time
	Series     []ArrivalSeries
	// Tasks 是这个源数据集 + 周期上的全部因子任务，由页面选择任务后再查。
	Tasks              []ArrivalTask
	RetentionTruncated bool
	// Truncated 表示记录超过一次查询的上限，曲线只含前面的部分。
	Truncated bool
}

// ArrivalTask 是源数据集上的一个因子任务。
type ArrivalTask struct {
	TaskID, Name, ResultDatasetID string
}

// FactorResultSource 列出源数据集 + 周期上的因子任务结果数据集；monmetrics.QueryService 实现它（来自 Storage 元数据）。
type FactorResultSource interface {
	ListFactorResultDatasets(ctx context.Context, spaceID, sourceDatasetID, freq string) ([]monmetrics.FactorResultDataset, error)
}

var arrivalStageOrder = map[string]int{"storage": 0, "view": 1, "factor": 2, "factor_view": 3, "strategy": 4}

// ArrivalHistory 按链路读出一天的逐根到达记录，并按市场交易日历算出收线时间、延迟与跳过的根数。
// 链路成员：入库是源数据集本身；视图是主数据集为它的 View；因子是选定任务的结果数据集；因子视图是主数据集为因子结果的 View；
// 策略的输入在运行时解析、Monitor 拿不到，按同空间同周期列出。同一个源数据集可以有多个因子任务：任务列表来自 Storage 元数据，
// 给了 task_id 就查它，只有一个任务时默认查它，有多个任务又没选时只返回任务列表和源数据集这一段。
func (b Builder) ArrivalHistory(ctx context.Context, q ArrivalHistoryQuery) (ArrivalHistory, error) {
	q.SpaceID, q.DatasetID, q.Day = strings.TrimSpace(q.SpaceID), strings.TrimSpace(q.DatasetID), strings.TrimSpace(q.Day)
	if q.SpaceID == "" || q.DatasetID == "" {
		return ArrivalHistory{}, fmt.Errorf("%w：需要空间与源数据集", ErrInvalidArrivalQuery)
	}
	freq, err := frequencypkg.Parse(q.Freq)
	if err != nil {
		return ArrivalHistory{}, fmt.Errorf("%w：周期 %q 无效", ErrInvalidArrivalQuery, q.Freq)
	}
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	clock, ok := b.spaceClocks(ctx, now)[q.SpaceID]
	if !ok {
		return ArrivalHistory{}, fmt.Errorf("%w：空间 %s 没有采集任务声明的交易日历，不记录到达", ErrInvalidArrivalQuery, q.SpaceID)
	}
	if clock.err != nil {
		return ArrivalHistory{}, fmt.Errorf("%w：空间 %s 无法按交易日历判断：%v", ErrInvalidArrivalQuery, q.SpaceID, clock.err)
	}
	location := clock.clock.Location()
	day := now.In(location)
	if q.Day != "" {
		if day, err = time.ParseInLocation("2006-01-02", q.Day, location); err != nil {
			return ArrivalHistory{}, fmt.Errorf("%w：日期 %q 应为 YYYY-MM-DD", ErrInvalidArrivalQuery, q.Day)
		}
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location)
	end := start.AddDate(0, 0, 1)
	if start.After(now) {
		return ArrivalHistory{}, fmt.Errorf("%w：%s 还没到", ErrInvalidArrivalQuery, start.Format("2006-01-02"))
	}
	out := ArrivalHistory{
		Timezone: location.String(), Day: start.Format("2006-01-02"), Start: start.UTC(), End: end.UTC(),
		RetentionTruncated: start.Before(now.Add(-monmetrics.BarArrivalRetention)),
	}
	// 收线落在这一天的根，开盘可能更早（小时线、日线在前一天，周线、月线在几周前）：多读两个周期（至少一天），再按收线时间筛。
	tasks, err := b.arrivalTasks(ctx, q.SpaceID, q.DatasetID, string(freq))
	if err != nil {
		return ArrivalHistory{}, err
	}
	out.Tasks = tasks
	factorID, err := selectArrivalTask(tasks, q.TaskID)
	if err != nil {
		return ArrivalHistory{}, err
	}
	lookback := max(2*freq.NominalDuration(), 24*time.Hour)
	rows, truncated, err := b.Metrics.ListBarArrivals(ctx, monmetrics.BarArrivalChain{
		SpaceID: q.SpaceID, Freq: string(freq), SourceDatasetID: q.DatasetID, FactorDatasetID: factorID,
	}, start.Add(-lookback), end)
	if err != nil {
		return ArrivalHistory{}, err
	}
	out.Truncated = truncated
	type seriesKey struct{ producer, datasetID string }
	grouped := make(map[seriesKey][]monmetrics.BarArrival)
	stages := make(map[seriesKey]string)
	for _, row := range rows {
		stage := arrivalStage(row, q.DatasetID, factorID)
		if stage == "" {
			continue
		}
		key := seriesKey{row.Producer, row.DatasetID}
		grouped[key] = append(grouped[key], row)
		stages[key] = stage
	}
	for key, records := range grouped {
		series := ArrivalSeries{
			Stage: stages[key], Producer: key.producer, DatasetID: key.datasetID, PrimaryDatasetID: records[0].PrimaryDatasetID,
			LatenessLimitSeconds: int64(b.Policy.Lateness(q.SpaceID, key.datasetID, string(freq)) / time.Second),
		}
		for _, record := range records {
			point := arrivalPoint(clock.clock, freq, record)
			if point.Close.Before(start) || !point.Close.Before(end) {
				continue
			}
			series.Points = append(series.Points, point)
		}
		if len(series.Points) > 0 {
			out.Series = append(out.Series, series)
		}
	}
	sort.Slice(out.Series, func(i, j int) bool {
		left, right := out.Series[i], out.Series[j]
		if arrivalStageOrder[left.Stage] != arrivalStageOrder[right.Stage] {
			return arrivalStageOrder[left.Stage] < arrivalStageOrder[right.Stage]
		}
		return left.DatasetID < right.DatasetID
	})
	return out, nil
}

// arrivalStage 判断一条到达记录属于链路的哪个环节；不属于这条链路时返回空。factorID 为空表示没有选定任务，不含因子环节。
func arrivalStage(row monmetrics.BarArrival, sourceID, factorID string) string {
	switch {
	case row.Producer == "storage" && row.DatasetID == sourceID:
		return "storage"
	case row.Producer == "storage_view" && row.PrimaryDatasetID == sourceID:
		return "view"
	case factorID != "" && row.Producer == "factor" && row.DatasetID == factorID:
		return "factor"
	case factorID != "" && row.Producer == "storage_view" && row.PrimaryDatasetID == factorID:
		return "factor_view"
	case row.Producer == "strategy":
		return "strategy"
	default:
		return ""
	}
}

// arrivalPoint 按市场交易日历算出这一根的收线时间、延迟与跳过的根数。日历算不出时按名义周期推算收线、不数跳过的根。
func arrivalPoint(clock monmetrics.MarketClock, freq frequencypkg.Frequency, record monmetrics.BarArrival) ArrivalPoint {
	point := ArrivalPoint{Close: record.BarAt.Add(freq.NominalDuration())}
	bar, err := clock.BarAt(record.BarAt, freq)
	if err == nil {
		point.Close = bar.Close
	}
	point.DelaySeconds = int64(max(record.ArrivedAt.Sub(point.Close), 0) / time.Second)
	if err != nil || record.PreviousBarAt == nil {
		return point
	}
	previous, err := clock.BarAt(*record.PreviousBarAt, freq)
	if err != nil {
		return point
	}
	if count, _, err := clock.ClosedBarsAfter(previous, bar.Close, freq); err == nil && count > 1 {
		point.SkippedBars = count - 1
	}
	return point
}

// arrivalTasks 列出源数据集 + 周期上的全部因子任务；没有配置 Storage 元数据时没有任务。
func (b Builder) arrivalTasks(ctx context.Context, spaceID, sourceDatasetID, freq string) ([]ArrivalTask, error) {
	if b.Metrics == nil {
		return nil, nil
	}
	datasets, err := b.Metrics.ListFactorResultDatasets(ctx, spaceID, sourceDatasetID, freq)
	if err != nil {
		return nil, fmt.Errorf("读取源数据集 %s 上的因子任务：%w", sourceDatasetID, err)
	}
	tasks := make([]ArrivalTask, 0, len(datasets))
	for _, dataset := range datasets {
		tasks = append(tasks, ArrivalTask{TaskID: dataset.TaskID, Name: dataset.Name, ResultDatasetID: dataset.DatasetID})
	}
	return tasks, nil
}

// selectArrivalTask 返回要查的因子结果数据集 ID：给了 taskID 就必须是列表里的任务；没给时只有一个任务就查它，
// 没有或有多个任务时返回空（链路不含因子环节）。
func selectArrivalTask(tasks []ArrivalTask, taskID string) (string, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID != "" {
		for _, task := range tasks {
			if task.TaskID == taskID {
				return task.ResultDatasetID, nil
			}
		}
		return "", fmt.Errorf("%w：源数据集上没有因子任务 %s", ErrInvalidArrivalQuery, taskID)
	}
	if len(tasks) == 1 {
		return tasks[0].ResultDatasetID, nil
	}
	return "", nil
}
