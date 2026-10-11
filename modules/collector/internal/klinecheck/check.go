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
	// MaxGaps 是一次检查返回的缺口数上限；超过时标记 Truncated。
	MaxGaps = 5000
	// MaxCheckPoints 是一次检查或补采允许展开的（标的 × 时间点）总数上限，防止一次请求占满内存。
	MaxCheckPoints = 3_000_000
)

// RowReader 读取 Storage 中的时序行。
type RowReader interface {
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
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
	Calendar func(spaceID, frequency string, barStart time.Time) bool
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

// Result 是检查结果。
type Result struct {
	Gaps      []Gap
	Expected  int
	Missing   int
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
	records, err := c.Plans.ListAll(ctx, request.SpaceID)
	if err != nil {
		return nil, err
	}
	timeline, err := BuildTimeline(records)
	if err != nil {
		return nil, err
	}
	type job struct {
		key     string
		segment Segment
		subject string
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
			for _, subject := range segment.Subjects {
				if request.SubjectID != "" && !strings.EqualFold(request.SubjectID, subject) {
					continue
				}
				jobs = append(jobs, job{key: key, segment: segment, subject: subject})
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
			points += int64(barCount(frequency, alignUp(frequency, from), to))
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
			gaps, expected, err := c.checkSubject(ctx, request, datasetID, frequency, item.subject, item.segment)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("check %s %s %s: %w", datasetID, frequency, item.subject, err)
				}
				return
			}
			result.Expected += expected
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

// expectedBars 返回区间内已过截止时间、应当存在的 K 线开始时间。
func (c *Checker) expectedBars(spaceID string, frequency marketdata.Frequency, frequencyName string, from, to time.Time, lateness time.Duration) []time.Time {
	now := c.now()
	var bars []time.Time
	for t := alignUp(frequency, from); t.Before(to); t = frequency.BarEnd(t) {
		if frequency.BarEnd(t).Add(lateness).After(now) {
			break
		}
		if c.Calendar != nil && !c.Calendar(spaceID, frequencyName, t) {
			continue
		}
		bars = append(bars, t)
	}
	return bars
}

func (c *Checker) checkSubject(ctx context.Context, request Request, datasetID, frequencyName, subject string, segment Segment) ([]Gap, int, error) {
	frequency, err := marketdata.ParseFrequency(frequencyName)
	if err != nil {
		return nil, 0, err
	}
	from, to := request.From.UTC(), request.To.UTC()
	if from.Unix() < segment.From {
		from = time.Unix(segment.From, 0).UTC()
	}
	if segment.To > 0 && to.Unix() > segment.To {
		to = time.Unix(segment.To, 0).UTC()
	}
	if !from.Before(to) {
		return nil, 0, nil
	}
	bars := c.expectedBars(request.SpaceID, frequency, frequencyName, from, to, time.Duration(segment.LatenessS)*time.Second)
	if len(bars) == 0 {
		return nil, 0, nil
	}
	required := []string{"close"}
	if request.Deep {
		required = deepFields(segment.OutputFields)
	}
	present := map[int64]bool{}
	if !request.AssumeMissing {
		var err error
		present, err = c.readPresent(ctx, request.SpaceID, datasetID, frequencyName, subject, bars[0], bars[len(bars)-1].Add(time.Second), required, request.Deep)
		if err != nil {
			return nil, 0, err
		}
	}
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
	return gaps, len(bars), nil
}

func deepFields(configured []string) []string {
	if len(configured) > 0 {
		return configured
	}
	return []string{"open", "high", "low", "close"}
}

// readPresent 读出区间内存在（深度检查时字段齐全）的 K 线开始时间。
func (c *Checker) readPresent(ctx context.Context, spaceID, datasetID, frequency, subject string, from, to time.Time, columns []string, deep bool) (map[int64]bool, error) {
	present := map[int64]bool{}
	for page := uint32(1); ; page++ {
		rsp, err := c.Rows.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
			AuthInfo: c.Auth, SpaceId: spaceID, DatasetId: datasetID,
			Selectors:   []*storagepb.TimeSeriesSelector{{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subject, Freq: frequency}},
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
			present[barTime.UTC().Unix()] = true
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
