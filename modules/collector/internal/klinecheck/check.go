package klinecheck

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	rowPageSize       = 2000
	defaultConcurrent = 8
	// subjectsPerRead 是一次读取合并的标的数：把逐标的的小请求合成批量请求，减少公网往返与网关压力。
	subjectsPerRead = 50
	// subjectsPerCount 是一次计数合并的标的数：计数只回传每个标的一个数字，批次可以比读行大得多（Storage 上限 2000）。
	subjectsPerCount = 500
	// MaxGaps 是一次检查返回的缺口数上限；超过时标记 Truncated。
	MaxGaps = 5000
	// MaxCheckPoints 是一次检查或补采允许展开的（标的 × 时间点）总数上限，防止一次请求占满内存。
	MaxCheckPoints = 3_000_000
)

// RowReader 读取 Storage 中的时序行。
type RowReader interface {
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
}

// AbsenceSource 返回已确认“行情源没有”的 K 线区间。
type AbsenceSource interface {
	AbsencesIn(ctx context.Context, spaceID, datasetID, frequency string, subjects []string, from, to time.Time) ([]store.SourceAbsence, error)
}

// RowCounter 按标的统计时间范围内已存在的行数，只回传计数。生产里是 Storage 主服务的 PrimaryStore.CountTimeSeriesRows，
// 在 Pebble 的序列索引上数键，看得到全部保留期内的数据（historyStarts 据此判断范围之前有没有数据）。
// Rows 实现了它时，检查先比对根数，只有根数对不上的标的才读行定位缺口。
type RowCounter interface {
	CountTimeSeriesRows(context.Context, *storagepb.CountTimeSeriesRowsReq, ...client.Option) (*storagepb.CountTimeSeriesRowsRsp, error)
}

// Checker 按标的时间线得到应有标的与时间点，读取 Storage 行比对出缺口。
type Checker struct {
	Timeline    TimelineSource
	Rows        RowReader
	Now         func() time.Time
	Concurrency int
	// Calendar 非空时用于过滤非交易时间点（按市场的分桶时钟，交易时段市场只有交易桶应有 K 线）；为空表示连续交易。
	// 日历无法判断（超出覆盖期、计算出错）必须返回错误，不能当作非交易时间。
	Calendar func(spaceID, frequency string, barStart time.Time) (bool, error)
	// Absences 非空时，缺口扣除已确认的行情源缺失（强制重拉除外）。
	Absences AbsenceSource
	// Fields 是行情源目录里可用的附加字段：深度检查的必需字段不含它们（来源没有值时这一列本来就是空的）。
	Fields marketdata.FieldOffers

	timelines timelineCache
}

// Request 是一次检查的范围。From 含、To 不含（K 线开始时间）。
type Request struct {
	SpaceID   string
	DatasetID string
	SubjectID string
	From      time.Time
	To        time.Time
	Deep      bool
	// AssumeMissing 不读取行，把范围内全部应有的 K 线都当作缺失（强制重拉）。
	AssumeMissing bool
	// IncludeOpenBar 让 From 落在某根 K 线中间时，把这根（开始时间早于 From、收盘晚于 From）也算进应有的 K 线。
	// 恢复追赶用它：暂停那一刻正在走的那根 K 线（例如 07:06:45 暂停时的 07:06 那根 1m、07:00 那根 1h）是在暂停期间收盘的，
	// 暂停期间没有采到，但它的开始时间早于暂停时间，按默认的「向上对齐」会被漏掉。
	IncludeOpenBar bool
	// FullHistory 让早于时间线起点的部分全部算应有的 K 线，不按每个标的自己的第一根裁掉（见 historyStarts）。
	// 历史补齐用：数据集刚接入，时间线起点之前一根都没有，按“从第一根起算”会一个缺口都报不出来。
	FullHistory bool
	// Subjects 非空时只检查这些标的（历史补齐按标的分批提交）；与 SubjectID 同时给出时取交集。
	Subjects []string
	// MergeSessions 让日内缺口跨过休市区间连成一段（日线以上本来就不拆），MaxGapBars 大于 0 时每段至多这么多根应有的 K 线。
	// 历史补齐用它把一个标的连续多个交易日合成一个分块：函数调用次数随分块数增长，按交易时段拆开会多出一个数量级。
	// 缺口里没有已存在的 K 线，所以 Gap.Missing 就是这一段应有的根数，补采按它（而不是按墙上时间）计 BarLimit。
	MergeSessions bool
	MaxGapBars    int
}

// Gap 是一段连续缺失的 K 线：[From, To) 内按频率共缺 Missing 根。
type Gap struct {
	DatasetID string
	Frequency string
	SubjectID string
	From      time.Time
	To        time.Time
	Missing   int
}

// Result 是检查结果。Absent 是已扣除的、确认行情源没有的根数，不计入 Missing。
type Result struct {
	Gaps      []Gap
	Expected  int
	Missing   int
	Absent    int
	Truncated bool
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// Check 检查范围内每个数据集、标的的 K 线是否齐全。
func (c *Checker) Check(ctx context.Context, request Request) (*Result, error) {
	if c == nil || c.Timeline == nil || c.Rows == nil {
		return nil, fmt.Errorf("kline checker is not initialized")
	}
	if strings.TrimSpace(request.SpaceID) == "" || request.From.IsZero() || request.To.IsZero() || !request.From.Before(request.To) {
		return nil, fmt.Errorf("space_id and a non-empty time range are required")
	}
	cached, err := c.timelines.Load(ctx, c.Timeline, request.SpaceID)
	if err != nil {
		return nil, err
	}
	timeline := cloneTimeline(cached)
	extendEarliestSegments(timeline, request.From)
	var jobs []checkJob
	var wanted map[string]bool
	if len(request.Subjects) > 0 {
		wanted = make(map[string]bool, len(request.Subjects))
		for _, subject := range request.Subjects {
			wanted[subject] = true
		}
	}
	batch := subjectsPerRead
	if _, counting := c.counter(request); counting {
		batch = subjectsPerCount
	}
	keys := make([]string, 0, len(timeline))
	for key := range timeline {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		datasetID, _, _ := strings.Cut(key, "|")
		if request.DatasetID != "" && request.DatasetID != datasetID {
			continue
		}
		for _, segment := range timeline[key] {
			var selected []string
			for _, subject := range segment.Subjects {
				if request.SubjectID != "" && !strings.EqualFold(request.SubjectID, subject) {
					continue
				}
				if len(wanted) > 0 && !wanted[subject] {
					continue
				}
				selected = append(selected, subject)
			}
			for from := 0; from < len(selected); from += batch {
				jobs = append(jobs, checkJob{key: key, segment: segment, subjects: selected[from:min(from+batch, len(selected))]})
			}
		}
	}
	if err := c.checkPoints(request, jobs); err != nil {
		return nil, err
	}
	result := &Result{}
	var mu sync.Mutex
	var firstErr error
	slots := make(chan struct{}, max(c.Concurrency, 1))
	if c.Concurrency <= 0 {
		slots = make(chan struct{}, defaultConcurrent)
	}
	var wg sync.WaitGroup
	for _, item := range jobs {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			datasetID, frequency, _ := strings.Cut(item.key, "|")
			gaps, expected, absent, err := c.checkSubjects(ctx, request, datasetID, frequency, item.subjects, item.segment)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("check %s %s %s 等 %d 个标的: %w", datasetID, frequency, item.subjects[0], len(item.subjects), err)
				}
				return
			}
			result.Expected += expected
			result.Absent += absent
			for _, gap := range gaps {
				result.Missing += gap.Missing
				if len(result.Gaps) >= MaxGaps {
					result.Truncated = true
					continue
				}
				result.Gaps = append(result.Gaps, gap)
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	sort.Slice(result.Gaps, func(i, j int) bool {
		a, b := result.Gaps[i], result.Gaps[j]
		if a.DatasetID != b.DatasetID {
			return a.DatasetID < b.DatasetID
		}
		if a.SubjectID != b.SubjectID {
			return a.SubjectID < b.SubjectID
		}
		return a.From.Before(b.From)
	})
	return result, nil
}

// checkJob 是检查的一个单位：同一数据集、同一计划段内的一批标的。
type checkJob struct {
	key      string
	segment  Segment
	subjects []string
}

// checkPoints 是检查的预检：展开的（标的 × 应有 K 线）总数不能超过 MaxCheckPoints。先按墙上时间估，超了再按交易日历数
// （交易时段市场只有交易桶应有 K 线；与历史补齐定批大小同一套数法，见 tradingBars），同一数据集同一段区间只数一次。
// 区间与 checkSubjects 相同（裁到计划段内，起点向上对齐），但不看截止时间。
func (c *Checker) checkPoints(request Request, jobs []checkJob) error {
	type span struct {
		key      string
		from, to int64
	}
	spans := map[span]int64{}
	var wall int64
	for _, item := range jobs {
		_, frequencyName, _ := strings.Cut(item.key, "|")
		frequency, err := marketdata.ParseFrequency(frequencyName)
		if err != nil {
			continue
		}
		from, to := request.From.UTC(), request.To.UTC()
		if request.IncludeOpenBar {
			from = alignDown(frequency, from)
		}
		if from.Unix() < item.segment.From {
			from = time.Unix(item.segment.From, 0).UTC()
		}
		if item.segment.To > 0 && to.Unix() > item.segment.To {
			to = time.Unix(item.segment.To, 0).UTC()
		}
		if from = alignUp(frequency, from); !to.After(from) {
			continue
		}
		wall += int64(barCount(frequency, from, to)) * int64(len(item.subjects))
		spans[span{key: item.key, from: from.Unix(), to: to.Unix()}] += int64(len(item.subjects))
	}
	if wall <= MaxCheckPoints {
		return nil
	}
	var points int64
	for item, subjects := range spans {
		_, frequencyName, _ := strings.Cut(item.key, "|")
		frequency, err := marketdata.ParseFrequency(frequencyName)
		if err != nil {
			return err
		}
		bars, _, err := c.tradingBars(request.SpaceID, frequency, frequencyName, time.Unix(item.from, 0).UTC(), time.Unix(item.to, 0).UTC())
		if err != nil {
			return err
		}
		points += int64(bars) * subjects
	}
	if points > MaxCheckPoints {
		return fmt.Errorf("%w: the range covers about %d bars, above the limit %d; narrow the time range or the subject set", errTooManyChunks, points, MaxCheckPoints)
	}
	return nil
}

// extendEarliestSegments 把每个数据集最早一段的起点前移到 from，让检查与补采能覆盖早于时间线起点（最早的采集计划生效点）的数据：
// 那段数据来自旧的采集方式或历史导入，其中的孔洞只能由操作员指定范围检查或补采。更早时间的标的集合已无记录，
// 沿用最早一段的标的；原来的起点记在 TimelineFrom，早于它的 K 线只从每个标的自己的第一根起算（见 historyStarts）。
func extendEarliestSegments(timeline Timeline, from time.Time) {
	at := from.UTC().Unix()
	for _, segments := range timeline {
		if len(segments) > 0 && segments[0].From > at {
			segments[0].TimelineFrom = segments[0].From
			segments[0].From = at
		}
	}
}

// historyStarts 返回早于时间线起点的那部分里，每个标的从 bars 的哪个下标起才算应有 K 线：
// 标的在 bars[0] 之前已有数据就从头算（不裁剪，范围开头的真实缺口照常报）；否则从它在时间线起点之前的第一根起算，
// 那之前一根都没有的（新上市）从时间线起点起算。present 是读到的行（扣除已确认缺失之前）。
// 没有计数能力或计数失败时不裁剪：宁可多报，不把真实缺口当成“尚无历史”。返回值只含需要裁剪的标的。
func (c *Checker) historyStarts(ctx context.Context, spaceID, datasetID, frequencyName string, subjects []string, bars []time.Time, present map[string]map[int64]bool, timelineFrom int64) map[string]int {
	boundary := sort.Search(len(bars), func(i int) bool { return bars[i].Unix() >= timelineFrom })
	counter, ok := c.Rows.(RowCounter)
	if boundary == 0 || len(subjects) == 0 || !ok {
		return nil
	}
	prior := map[string]int{}
	for from := 0; from < len(subjects); from += subjectsPerCount {
		batch := subjects[from:min(from+subjectsPerCount, len(subjects))]
		counts, err := countPresent(ctx, counter, spaceID, datasetID, frequencyName, batch, time.Unix(0, 0).UTC(), bars[0])
		if err != nil {
			log.WarnContextf(ctx, "collector kline count before range failed, gaps before the timeline are not trimmed space=%s dataset=%s|%s: %v", spaceID, datasetID, frequencyName, err)
			return nil
		}
		for subject, count := range counts {
			prior[subject] = count
		}
	}
	starts := map[string]int{}
	for _, subject := range subjects {
		if prior[subject] > 0 {
			continue
		}
		start := firstPresent(bars[:boundary], present[subject])
		if start < 0 {
			start = boundary
		}
		if start > 0 {
			starts[subject] = start
		}
	}
	return starts
}

// firstPresent 返回标的第一根已存在的 K 线在 bars 里的下标；一根都没有时返回 -1。
func firstPresent(bars []time.Time, present map[int64]bool) int {
	for i, bar := range bars {
		if present[bar.Unix()] {
			return i
		}
	}
	return -1
}

// expectedBars 返回区间内已过截止时间、应当存在的 K 线开始时间。
func (c *Checker) expectedBars(spaceID string, frequency marketdata.Frequency, frequencyName string, from, to time.Time, lateness time.Duration) ([]time.Time, error) {
	now := c.now()
	var bars []time.Time
	for t := alignUp(frequency, from); t.Before(to); t = frequency.BarEnd(t) {
		if frequency.BarEnd(t).Add(lateness).After(now) {
			break
		}
		if c.Calendar != nil {
			trading, err := c.Calendar(spaceID, frequencyName, t)
			if err != nil {
				return nil, fmt.Errorf("trading calendar at %s: %w", t.Format(time.RFC3339), err)
			}
			if !trading {
				continue
			}
		}
		bars = append(bars, t)
	}
	return bars, nil
}

// tradingBars 数 [from, to) 内应有的 K 线根数与连续段数（休市区间把它们隔开）：有日历时只数交易桶，与 expectedBars
// 同一套过滤，但不看截止时间。检查的预检（checkPoints）与历史补齐的批大小（prepareSeed）都按它估算根数，两边口径一致：
// 按全天 24 小时数，A 股 5m 一天 288 根、实际只有 48 根，批次会永远过不了预检。
func (c *Checker) tradingBars(spaceID string, frequency marketdata.Frequency, frequencyName string, from, to time.Time) (bars, sessions int, err error) {
	var last time.Time
	for t := alignUp(frequency, from); t.Before(to); t = frequency.BarEnd(t) {
		if c.Calendar != nil {
			trading, err := c.Calendar(spaceID, frequencyName, t)
			if err != nil {
				return 0, 0, fmt.Errorf("trading calendar at %s: %w", t.Format(time.RFC3339), err)
			}
			if !trading {
				continue
			}
		}
		if bars == 0 || !t.Equal(frequency.BarEnd(last)) {
			sessions++
		}
		bars++
		last = t
	}
	return bars, sessions, nil
}

// checkSubjects 检查同一数据集、同一计划段内的一批标的：应有的 K 线对它们相同，只读一次 Storage，再逐标的求缺口。
// 返回缺口、应有根数与扣除的已确认缺失根数。
func (c *Checker) checkSubjects(ctx context.Context, request Request, datasetID, frequencyName string, subjects []string, segment Segment) ([]Gap, int, int, error) {
	frequency, err := marketdata.ParseFrequency(frequencyName)
	if err != nil {
		return nil, 0, 0, err
	}
	from, to := request.From.UTC(), request.To.UTC()
	if request.IncludeOpenBar {
		from = alignDown(frequency, from)
	}
	if from.Unix() < segment.From {
		from = time.Unix(segment.From, 0).UTC()
	}
	if segment.To > 0 && to.Unix() > segment.To {
		to = time.Unix(segment.To, 0).UTC()
	}
	if !from.Before(to) {
		return nil, 0, 0, nil
	}
	bars, err := c.expectedBars(request.SpaceID, frequency, frequencyName, from, to, time.Duration(segment.LatenessS)*time.Second)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(bars) == 0 {
		return nil, 0, 0, nil
	}
	required := []string{"close"}
	if request.Deep {
		required = c.deepFields(request.SpaceID, segment.OutputFields)
	}
	present := map[string]map[int64]bool{}
	complete := map[string]bool{}
	var starts map[string]int
	absent := 0
	if !request.AssumeMissing {
		end := bars[len(bars)-1].Add(time.Second)
		absentBars, err := c.absentBars(ctx, request.SpaceID, datasetID, frequencyName, subjects, bars)
		if err != nil {
			return nil, 0, 0, err
		}
		detail := subjects
		if counter, ok := c.counter(request); ok {
			// 先比对根数：根数正好等于“应有 - 已确认缺失”的标的没有缺口，不用读行。
			if counts, err := countPresent(ctx, counter, request.SpaceID, datasetID, frequencyName, subjects, bars[0], end); err != nil {
				// 计数失败（例如 Storage 还没升级）不影响检查，退回逐行比对。
				log.WarnContextf(ctx, "collector kline count failed, falling back to row reads space=%s dataset=%s|%s: %v", request.SpaceID, datasetID, frequencyName, err)
			} else {
				detail = nil
				for _, subject := range subjects {
					if counts[subject] == len(bars)-len(absentBars[subject]) {
						complete[subject] = true
						absent += len(absentBars[subject])
						continue
					}
					detail = append(detail, subject)
				}
			}
		}
		for from := 0; from < len(detail); from += subjectsPerRead {
			chunk := detail[from:min(from+subjectsPerRead, len(detail))]
			rows, err := c.readPresent(ctx, request.SpaceID, datasetID, frequencyName, chunk, bars[0], end, required, request.Deep)
			if err != nil {
				return nil, 0, 0, err
			}
			for subject, times := range rows {
				present[subject] = times
			}
		}
		// 早于时间线起点的部分：每个标的从自己的第一根起算，一根都没有的不报（新上市的标的前面本来没有数据）。
		if segment.TimelineFrom > 0 && !request.FullHistory {
			starts = c.historyStarts(ctx, request.SpaceID, datasetID, frequencyName, detail, bars, present, segment.TimelineFrom)
		}
		absent += coverAbsences(bars, present, absentBars, detail)
	}
	var gaps []Gap
	expected := len(bars) * len(subjects)
	for _, subject := range subjects {
		start := starts[subject]
		expected -= start
		if complete[subject] {
			continue
		}
		gaps = append(gaps, subjectGaps(datasetID, frequencyName, subject, frequency, bars[start:], present[subject], gapOptions{merge: request.MergeSessions, maxBars: request.MaxGapBars})...)
	}
	return gaps, expected, absent, nil
}

// counter 返回可用的计数能力：深度检查要校验字段，强制重拉不读数据，二者都不计数。
func (c *Checker) counter(request Request) (RowCounter, bool) {
	if request.Deep || request.AssumeMissing {
		return nil, false
	}
	counter, ok := c.Rows.(RowCounter)
	return counter, ok
}

// countPresent 按标的统计 [from, to) 内已存在的 K 线根数。
func countPresent(ctx context.Context, counter RowCounter, spaceID, datasetID, frequency string, subjects []string, from, to time.Time) (map[string]int, error) {
	selectors := make([]*storagepb.TimeSeriesSelector, 0, len(subjects))
	for _, subject := range subjects {
		selectors = append(selectors, seriesSelector(subject, frequency))
	}
	rsp, err := counter.CountTimeSeriesRows(ctx, &storagepb.CountTimeSeriesRowsReq{
		SpaceId: spaceID, DatasetId: datasetID, Selectors: selectors,
		TimeRange: &storagepb.TimeRange{StartTime: from.Format(time.RFC3339), EndTime: to.Format(time.RFC3339)},
	})
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("count rows: %s", rsp.GetRetInfo().GetMsg())
	}
	counts := make(map[string]int, len(rsp.GetCounts()))
	for _, item := range rsp.GetCounts() {
		counts[item.GetSubjectId()] = int(item.GetRows())
	}
	return counts, nil
}

// seriesSelector 构造读取一个标的序列的选择器（数据集里每个标的只有一条序列，不带 series_tag）。
func seriesSelector(subject, frequency string) *storagepb.TimeSeriesSelector {
	return &storagepb.TimeSeriesSelector{SubjectId: subject, Freq: frequency}
}

// absentBars 返回每个标的在 bars 里已确认行情源没有的 K 线开始时间。
func (c *Checker) absentBars(ctx context.Context, spaceID, datasetID, frequencyName string, subjects []string, bars []time.Time) (map[string]map[int64]bool, error) {
	out := map[string]map[int64]bool{}
	if c.Absences == nil || len(bars) == 0 {
		return out, nil
	}
	frequency, err := marketdata.ParseFrequency(frequencyName)
	if err != nil {
		return nil, err
	}
	absences, err := c.Absences.AbsencesIn(ctx, spaceID, datasetID, frequencyName, subjects, bars[0], frequency.BarEnd(bars[len(bars)-1]))
	if err != nil {
		return nil, fmt.Errorf("read source absences: %w", err)
	}
	for _, absence := range absences {
		set := out[absence.SubjectID]
		if set == nil {
			set = map[int64]bool{}
			out[absence.SubjectID] = set
		}
		// bars 已按时间排序：二分找到区间内的第一根。
		start := sort.Search(len(bars), func(i int) bool { return !bars[i].Before(absence.From) })
		for i := start; i < len(bars) && bars[i].Before(absence.To); i++ {
			set[bars[i].Unix()] = true
		}
	}
	return out, nil
}

// coverAbsences 把这些标的已确认行情源没有、且确实缺失的 K 线并入 present（不再算缺口），返回并入的根数。
func coverAbsences(bars []time.Time, present map[string]map[int64]bool, absent map[string]map[int64]bool, subjects []string) int {
	covered := 0
	for _, subject := range subjects {
		set := absent[subject]
		if len(set) == 0 {
			continue
		}
		bySubject := present[subject]
		if bySubject == nil {
			bySubject = map[int64]bool{}
			present[subject] = bySubject
		}
		for at := range set {
			if !bySubject[at] {
				bySubject[at] = true
				covered++
			}
		}
	}
	return covered
}

// gapOptions 是缺口合并方式：merge 让日内缺口跨过休市区间连成一段，maxBars 大于 0 时每段至多这么多根应有的 K 线。
type gapOptions struct {
	merge   bool
	maxBars int
}

// subjectGaps 把应有的 K 线与实际存在的时间点比对，合并成连续缺口。
func subjectGaps(datasetID, frequencyName, subject string, frequency marketdata.Frequency, bars []time.Time, present map[int64]bool, options gapOptions) []Gap {
	var gaps []Gap
	var open *Gap
	// 日内：中间隔着非交易时段（日历跳过的 K 线）时拆成两段，补采分块不会跨休市区间。
	// 日线及以上不拆：相邻交易日之间隔着周末、节假日是常态，按休市拆开的话一年的日线每个标的要拆成五十多段、
	// 每段一次函数调用；行情源按日期区间或交易日序号取数，区间里夹着非交易日不影响。
	intraday := frequency.Duration() > 0 && frequency.Duration() < 24*time.Hour && !options.merge
	for _, bar := range bars {
		if present[bar.Unix()] {
			if open != nil {
				gaps = append(gaps, *open)
				open = nil
			}
			continue
		}
		if intraday && open != nil && !bar.Equal(open.To) {
			gaps = append(gaps, *open)
			open = nil
		}
		if open != nil && options.maxBars > 0 && open.Missing >= options.maxBars {
			gaps = append(gaps, *open)
			open = nil
		}
		if open == nil {
			open = &Gap{DatasetID: datasetID, Frequency: frequencyName, SubjectID: subject, From: bar}
		}
		open.To = frequency.BarEnd(bar)
		open.Missing++
	}
	if open != nil {
		gaps = append(gaps, *open)
	}
	return gaps
}

// deepFields 返回深度检查要求每根都有值的字段：任务选中的输出字段去掉附加字段；没有选或只选了附加字段时是 OHLC。
func (c *Checker) deepFields(spaceID string, configured []string) []string {
	var required []string
	for _, field := range configured {
		if _, extra := c.Fields.Spec(spaceID, field); !extra {
			required = append(required, field)
		}
	}
	if len(required) > 0 {
		return required
	}
	return []string{"open", "high", "low", "close"}
}

// readPresent 一次读出一批标的在区间内存在（深度检查时字段齐全）的 K 线开始时间，按标的分组返回。
func (c *Checker) readPresent(ctx context.Context, spaceID, datasetID, frequency string, subjects []string, from, to time.Time, columns []string, deep bool) (map[string]map[int64]bool, error) {
	selectors := make([]*storagepb.TimeSeriesSelector, 0, len(subjects))
	present := make(map[string]map[int64]bool, len(subjects))
	for _, subject := range subjects {
		selectors = append(selectors, seriesSelector(subject, frequency))
		present[subject] = map[int64]bool{}
	}
	cursor := ""
	for {
		rsp, err := c.Rows.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
			SpaceId: spaceID, DatasetId: datasetID,
			Selectors:   selectors,
			TimeRange:   &storagepb.TimeRange{StartTime: from.Format(time.RFC3339), EndTime: to.Format(time.RFC3339)},
			ColumnNames: columns, Page: &storagepb.Page{Size: rowPageSize, Cursor: cursor},
		})
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("read rows: %s", rsp.GetRetInfo().GetMsg())
		}
		for _, row := range rsp.GetRows() {
			ts := row.GetKey().GetTimeSeries()
			barTime, parseErr := time.Parse(time.RFC3339, ts.GetDataTime())
			if parseErr != nil {
				continue
			}
			if deep && !hasFields(row, columns) {
				continue
			}
			if bySubject, ok := present[ts.GetSubjectId()]; ok {
				bySubject[barTime.UTC().Unix()] = true
			}
		}
		if !rsp.GetPageResult().GetHasMore() {
			return present, nil
		}
		cursor = rsp.GetPageResult().GetNextCursor()
		if cursor == "" {
			return nil, fmt.Errorf("read rows: 还有后续页但没有返回游标")
		}
	}
}

func hasFields(row *storagepb.Row, required []string) bool {
	have := map[string]bool{}
	for _, field := range row.GetFields() {
		if field.GetValue() != nil && field.GetValue().GetValue() != nil {
			have[field.GetFieldId()] = true
		}
	}
	for _, name := range required {
		if !have[name] {
			return false
		}
	}
	return true
}
