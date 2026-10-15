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
)

const (
	rowPageSize       = 5000
	defaultConcurrent = 8
	// subjectsPerRead 是一次读取合并的标的数：把逐标的的小请求合成批量请求，减少公网往返与网关压力。
	subjectsPerRead = 50
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

// PlanHistory 返回空间的全部采集计划。
type PlanHistory interface {
	ListAll(context.Context, string) ([]*store.FetchPlanRecord, error)
}

// Checker 按采集计划还原应有标的与时间点，读取 Storage 行比对出缺口。
type Checker struct {
	Plans       PlanHistory
	Rows        RowReader
	Auth        *storagepb.AuthInfo
	Now         func() time.Time
	Concurrency int
	// Calendar 非空时用于过滤非交易时间点（按空间区分，stockcn 使用交易日历）；为空表示连续交易。
	// 日历无法判断（超出覆盖期、计算出错）必须返回错误，不能当作非交易时间。
	Calendar func(spaceID, frequency string, barStart time.Time) (bool, error)
	// CalendarReady 非空时在检查前确认该空间的日历可用；不可用必须报错，不能当作“没有应有数据”。
	CalendarReady func(spaceID string) error
	// Absences 非空时，缺口扣除已确认的行情源缺失（强制重拉除外）。
	Absences AbsenceSource
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
	// BeforeFirstPlan 让范围可以早于每个数据集最早的采集计划生效点（见 extendEarliestSegments）；默认只检查计划覆盖的时间。
	BeforeFirstPlan bool
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
	if c == nil || c.Plans == nil || c.Rows == nil {
		return nil, fmt.Errorf("kline checker is not initialized")
	}
	if strings.TrimSpace(request.SpaceID) == "" || request.From.IsZero() || request.To.IsZero() || !request.From.Before(request.To) {
		return nil, fmt.Errorf("space_id and a non-empty time range are required")
	}
	if c.CalendarReady != nil {
		if err := c.CalendarReady(request.SpaceID); err != nil {
			return nil, fmt.Errorf("trading calendar is unavailable: %w", err)
		}
	}
	records, err := c.Plans.ListAll(ctx, request.SpaceID)
	if err != nil {
		return nil, err
	}
	timeline, err := BuildTimeline(records)
	if err != nil {
		return nil, err
	}
	if request.BeforeFirstPlan {
		extendEarliestSegments(timeline, request.From)
	}
	type job struct {
		key      string
		segment  Segment
		subjects []string
	}
	var jobs []job
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
				selected = append(selected, subject)
			}
			for from := 0; from < len(selected); from += subjectsPerRead {
				jobs = append(jobs, job{key: key, segment: segment, subjects: selected[from:min(from+subjectsPerRead, len(selected))]})
			}
		}
	}
	var points int64
	for _, item := range jobs {
		_, frequencyName, _ := strings.Cut(item.key, "|")
		frequency, err := marketdata.ParseFrequency(frequencyName)
		if err != nil {
			continue
		}
		from, to := request.From.UTC(), request.To.UTC()
		if from.Unix() < item.segment.From {
			from = time.Unix(item.segment.From, 0).UTC()
		}
		if item.segment.To > 0 && to.Unix() > item.segment.To {
			to = time.Unix(item.segment.To, 0).UTC()
		}
		if to.After(from) {
			points += int64(barCount(frequency, alignUp(frequency, from), to)) * int64(len(item.subjects))
		}
	}
	if points > MaxCheckPoints {
		return nil, fmt.Errorf("%w: the range covers about %d bars, above the limit %d; narrow the time range or the subject set", errTooManyChunks, points, MaxCheckPoints)
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

// extendEarliestSegments 把每个数据集最早一段的起点前移到 from，让强制重拉（及其复查）能覆盖早于采集计划生效点的数据。
// 计划只记录“从哪个周期起实时采集”，更早的数据可能来自旧的采集方式或历史导入，其中的孔洞按计划时间线看不到，
// 只能由操作员指定范围重拉。更早时间的标的集合已无记录，沿用最早一段的标的。
func extendEarliestSegments(timeline Timeline, from time.Time) {
	at := from.UTC().Unix()
	for _, segments := range timeline {
		if len(segments) > 0 && segments[0].From > at {
			segments[0].From = at
		}
	}
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

// checkSubjects 检查同一数据集、同一计划段内的一批标的：应有的 K 线对它们相同，只读一次 Storage，再逐标的求缺口。
// 返回缺口、应有根数与扣除的已确认缺失根数。
func (c *Checker) checkSubjects(ctx context.Context, request Request, datasetID, frequencyName string, subjects []string, segment Segment) ([]Gap, int, int, error) {
	frequency, err := marketdata.ParseFrequency(frequencyName)
	if err != nil {
		return nil, 0, 0, err
	}
	from, to := request.From.UTC(), request.To.UTC()
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
		required = deepFields(segment.OutputFields)
	}
	present := map[string]map[int64]bool{}
	absent := 0
	if !request.AssumeMissing {
		present, err = c.readPresent(ctx, request.SpaceID, datasetID, frequencyName, subjects, bars[0], bars[len(bars)-1].Add(time.Second), required, request.Deep)
		if err != nil {
			return nil, 0, 0, err
		}
		if absent, err = c.coverAbsences(ctx, request.SpaceID, datasetID, frequencyName, subjects, bars, present); err != nil {
			return nil, 0, 0, err
		}
	}
	var gaps []Gap
	for _, subject := range subjects {
		gaps = append(gaps, subjectGaps(datasetID, frequencyName, subject, frequency, bars, present[subject])...)
	}
	return gaps, len(bars) * len(subjects), absent, nil
}

// coverAbsences 把已确认行情源没有、且确实缺失的 K 线并入 present（不再算缺口），返回并入的根数。
func (c *Checker) coverAbsences(ctx context.Context, spaceID, datasetID, frequencyName string, subjects []string, bars []time.Time, present map[string]map[int64]bool) (int, error) {
	if c.Absences == nil || len(bars) == 0 {
		return 0, nil
	}
	frequency, err := marketdata.ParseFrequency(frequencyName)
	if err != nil {
		return 0, err
	}
	absences, err := c.Absences.AbsencesIn(ctx, spaceID, datasetID, frequencyName, subjects, bars[0], frequency.BarEnd(bars[len(bars)-1]))
	if err != nil {
		return 0, fmt.Errorf("read source absences: %w", err)
	}
	covered := 0
	for _, absence := range absences {
		bySubject := present[absence.SubjectID]
		if bySubject == nil {
			bySubject = map[int64]bool{}
			present[absence.SubjectID] = bySubject
		}
		// bars 已按时间排序：二分找到区间内的第一根。
		start := sort.Search(len(bars), func(i int) bool { return !bars[i].Before(absence.From) })
		for i := start; i < len(bars) && bars[i].Before(absence.To); i++ {
			if !bySubject[bars[i].Unix()] {
				bySubject[bars[i].Unix()] = true
				covered++
			}
		}
	}
	return covered, nil
}

// subjectGaps 把应有的 K 线与实际存在的时间点比对，合并成连续缺口。
func subjectGaps(datasetID, frequencyName, subject string, frequency marketdata.Frequency, bars []time.Time, present map[int64]bool) []Gap {
	var gaps []Gap
	var open *Gap
	for _, bar := range bars {
		if present[bar.Unix()] {
			if open != nil {
				gaps = append(gaps, *open)
				open = nil
			}
			continue
		}
		// 中间隔着非交易时段（日历跳过的 K 线）时拆成两段，补采分块不会跨休市区间。
		if open != nil && !bar.Equal(open.To) {
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

func deepFields(configured []string) []string {
	if len(configured) > 0 {
		return configured
	}
	return []string{"open", "high", "low", "close"}
}

// readPresent 一次读出一批标的在区间内存在（深度检查时字段齐全）的 K 线开始时间，按标的分组返回。
func (c *Checker) readPresent(ctx context.Context, spaceID, datasetID, frequency string, subjects []string, from, to time.Time, columns []string, deep bool) (map[string]map[int64]bool, error) {
	selectors := make([]*storagepb.TimeSeriesSelector, 0, len(subjects))
	present := make(map[string]map[int64]bool, len(subjects))
	for _, subject := range subjects {
		selectors = append(selectors, &storagepb.TimeSeriesSelector{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subject, Freq: frequency})
		present[subject] = map[int64]bool{}
	}
	for page := uint32(1); ; page++ {
		rsp, err := c.Rows.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
			AuthInfo: c.Auth, SpaceId: spaceID, DatasetId: datasetID,
			Selectors:   selectors,
			TimeRange:   &storagepb.TimeRange{StartTime: from.Format(time.RFC3339), EndTime: to.Format(time.RFC3339)},
			ColumnNames: columns, Page: &storagepb.Page{Page: page, Size: rowPageSize},
		})
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("read rows: %s", rsp.GetRetInfo().GetMsg())
		}
		for _, row := range rsp.GetRows() {
			barTime, parseErr := time.Parse(time.RFC3339, row.GetKey().GetDataTime())
			if parseErr != nil {
				continue
			}
			if deep && !hasFields(row, columns) {
				continue
			}
			if bySubject, ok := present[row.GetKey().GetSubjectId()]; ok {
				bySubject[barTime.UTC().Unix()] = true
			}
		}
		if !rsp.GetPageResult().GetHasMore() {
			return present, nil
		}
	}
}

func hasFields(row *storagepb.TimeSeriesRow, required []string) bool {
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
