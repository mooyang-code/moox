package input

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// DefaultReplayMaxBars 是一次回放允许的最多 bar 数。
const DefaultReplayMaxBars = 20000

// ReplayRange 把回放区间换成日历的口径：A 股日线按上海日期回放，两端各取所在上海日期的零点，[起始日期, 结束日期)
// 内的交易日都包含在内（区间通常按 UTC 输入，而 A 股日线的 bar_start 是上海零点、即前一日 16:00 UTC，直接比较会漏掉
// 首日、多出末日）；其余日历原样返回。
func ReplayRange(calendar, bar string, start, end time.Time) (time.Time, time.Time, error) {
	if !stockDaily(calendar, bar) {
		return start, end, nil
	}
	_, location, err := stockCalendar()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	floor := func(at time.Time) time.Time {
		local := at.In(location)
		return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).UTC()
	}
	start, end = floor(start), floor(end)
	if !end.After(start) {
		return time.Time{}, time.Time{}, errors.New("A 股日线按上海日期回放：结束日期必须晚于起始日期")
	}
	return start, end, nil
}

// ReplayBars 返回 bar_start 落在 [start, end) 内的全部周期，超过 limit 根返回错误。A 股内嵌日历的最后一个交易日之后
// 没有可知的周期，枚举到它为止。
func ReplayBars(calendar, bar string, start, end time.Time, limit int) ([]PeriodBoundaries, error) {
	if !end.After(start) {
		return nil, errors.New("回放区间必须满足 start < end")
	}
	current, err := firstBarAtOrAfter(calendar, bar, start)
	if err != nil {
		return nil, err
	}
	bars := make([]PeriodBoundaries, 0)
	for current.StorageStart.Before(end) {
		if len(bars) >= limit {
			return nil, fmt.Errorf("回放区间超过 %d 根 bar，请缩短区间", limit)
		}
		bars = append(bars, current)
		if current.NextEnd.IsZero() {
			break
		}
		if current, err = FromBarEnd(calendar, bar, current.NextEnd); err != nil {
			return nil, err
		}
	}
	return bars, nil
}

// firstBarAtOrAfter 返回 bar_start 不早于 at 的第一根。
func firstBarAtOrAfter(calendar, bar string, at time.Time) (PeriodBoundaries, error) {
	if stockDaily(calendar, bar) {
		// 不经 ClosedPeriod 回看上一根收盘：at 落在日历首日时没有上一根。取 at 所在上海日期起的第一个交易日，
		// at 晚于那天零点时再往后一根。
		day, err := CeilStorageStart(calendar, bar, at)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		current, err := FromStorageStart(calendar, bar, day)
		if err != nil || !current.StorageStart.Before(at) {
			return current, err
		}
		if current.NextEnd.IsZero() {
			return PeriodBoundaries{}, beyondCalendarError(at)
		}
		return FromBarEnd(calendar, bar, current.NextEnd)
	}
	current, err := ClosedPeriod(calendar, bar, at)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	for current.StorageStart.Before(at) {
		if current, err = FromBarEnd(calendar, bar, current.NextEnd); err != nil {
			return PeriodBoundaries{}, err
		}
	}
	return current, nil
}

// beyondCalendarError 说明 at 之后的周期超出了 A 股内嵌日历。
func beyondCalendarError(at time.Time) error {
	return &calendarError{
		message: fmt.Sprintf("%s 之后的周期%s", at.UTC().Format(time.RFC3339), translateCalendarError(marketcalendar.ErrNoNextTradingDay).Error()),
		cause:   marketcalendar.ErrNoNextTradingDay,
	}
}

// historyNeed 返回回放的每一根需要的历史根数（含自身）与来源说明：min_age_bars 的年龄目标是 T − (N−1) 根，
// bars[-1] 要读上一根。
func historyNeed(resolved Resolved, program *dsl.Program) (int, string) {
	need, sources := 1, make([]string, 0, 2)
	if resolved.MinAgeBars > 1 {
		need = resolved.MinAgeBars
		sources = append(sources, fmt.Sprintf("min_age_bars=%d", resolved.MinAgeBars))
	}
	if program != nil && program.UsesPreviousBar {
		need = max(need, 2)
		sources = append(sources, "bars[-1]")
	}
	return need, strings.Join(sources, "、")
}

// EarlyStartError 表示回放起点早于可用起点：起点之前不够 min_age_bars 与 bars[-1] 要求的历史。
type EarlyStartError struct {
	// First 是区间内的第一根；Coverage 是活跃序列的覆盖起点；Earliest 是可用起点。
	First    time.Time
	Coverage time.Time
	Earliest time.Time
	// History 是起点之前需要的根数，Source 是要求的来源（min_age_bars、bars[-1]）。
	History int
	Source  string
}

func (e *EarlyStartError) Error() string {
	if e.History <= 0 {
		return fmt.Sprintf("回放起点 %s 太早：View 的活跃序列从 %s 起才有数据；可用起点为 %s", e.First.Format(time.RFC3339), e.Coverage.Format(time.RFC3339), e.Earliest.Format(time.RFC3339))
	}
	return fmt.Sprintf("回放起点 %s 太早：View 的活跃序列从 %s 起才有数据，%s 要求起点之前还有 %d 根历史；可用起点为 %s", e.First.Format(time.RFC3339), e.Coverage.Format(time.RFC3339), e.Source, e.History, e.Earliest.Format(time.RFC3339))
}

// ReplayWindow 校验并截断回放区间 [start, end)：
// 起点不能早于活跃序列的覆盖起点（CoverageStart）加上 min_age_bars 与 bars[-1] 所需的历史，否则返回 *EarlyStartError；
// 终点截到最新一根（按日历规整后的 IndexedTo）之前：更晚的 bar 还没有数据，最新一根也可能还没写完，都不能当作
// 完整的 bar 回放。最新一根之后还有行时（A 股数据超出内嵌日历，最新一根被夹到日历的最后一个交易日）它已经写完，
// 回放到它为止。
func ReplayWindow(resolved Resolved, program *dsl.Program, view ViewInfo, start, end time.Time) (time.Time, error) {
	if !end.After(start) {
		return time.Time{}, errors.New("回放区间必须满足 start < end")
	}
	need, source := historyNeed(resolved, program)
	// 稳定状态下覆盖起点是最新一根往前 SeriesBars−1 根，可用起点再往后 need−1 根，而回放只到最新一根之前：
	// need 不小于 SeriesBars 时没有任何一根可以回放。
	if view.SeriesBars > 0 && need > 1 && need >= view.SeriesBars {
		return time.Time{}, fmt.Errorf("回放的每一根之前需要 %d 根历史（%s），但 View %s 每个序列只保留最近 %d 根、回放又只到最新一根之前，永远无法满足", need-1, source, resolved.ViewID, view.SeriesBars)
	}
	bounds, err := CoverageBounds(view, resolved)
	if err != nil {
		return time.Time{}, coverageUnusable(resolved.ViewID, err, "暂不能回放")
	}
	coverage := coverageStart(bounds, view, resolved, time.Time{})
	latest, err := FromStorageStart(resolved.Calendar, resolved.Bar, bounds.To)
	if err != nil {
		return time.Time{}, err
	}
	// limit 是终点的上限，last 是最后一根可以回放的 bar；区间按毫秒落库，加一毫秒即可包含最新一根。
	limit, last := latest.StorageStart, latest.PreviousStart
	if bounds.ToClosed {
		limit, last = latest.StorageStart.Add(time.Millisecond), latest.StorageStart
	}
	if last.IsZero() {
		return time.Time{}, fmt.Errorf("View %s 还没有完整的 bar（最新一根 %s 可能还没写完），暂不能回放", resolved.ViewID, latest.StorageStart.Format(time.RFC3339))
	}
	if end.After(limit) {
		end = limit
	}
	if !end.After(start) {
		return time.Time{}, fmt.Errorf("回放区间内没有完整的数据：View %s 最后一根完整的 bar_start 为 %s", resolved.ViewID, last.Format(time.RFC3339))
	}
	// 先反向判断历史够不够：从覆盖起点往后推 need−1 根可能越过 A 股内嵌日历的末尾，会被误报成日历需要更新。
	bound := last
	if need > 1 {
		bound, err = HistoryStart(resolved.Calendar, resolved.Bar, last, need)
		if errors.Is(err, marketcalendar.ErrNoPreviousTradingDay) {
			bound, err = time.Time{}, nil
		}
		if err != nil {
			return time.Time{}, err
		}
	}
	if bound.IsZero() || coverage.After(bound) {
		if need > 1 {
			return time.Time{}, fmt.Errorf("View %s 的活跃序列当前只覆盖 %s 至 %s，回放的每一根之前需要 %d 根历史（%s），暂不能回放", resolved.ViewID, coverage.Format(time.RFC3339), latest.StorageStart.Format(time.RFC3339), need-1, source)
		}
		return time.Time{}, fmt.Errorf("View %s 的活跃序列当前只覆盖 %s 至 %s，还没有可以回放的完整 bar", resolved.ViewID, coverage.Format(time.RFC3339), latest.StorageStart.Format(time.RFC3339))
	}
	earliest := coverage
	if need > 1 {
		period, err := FromStorageStart(resolved.Calendar, resolved.Bar, coverage)
		if err != nil {
			return time.Time{}, err
		}
		barEnd, err := AdvanceBarEnd(resolved.Calendar, resolved.Bar, period.BarEnd, need-1)
		if err != nil {
			return time.Time{}, err
		}
		shifted, err := FromBarEnd(resolved.Calendar, resolved.Bar, barEnd)
		if err != nil {
			return time.Time{}, err
		}
		earliest = shifted.StorageStart
	}
	first, err := firstBarAtOrAfter(resolved.Calendar, resolved.Bar, start)
	if err != nil {
		return time.Time{}, err
	}
	if first.StorageStart.Before(earliest) {
		return time.Time{}, &EarlyStartError{First: first.StorageStart, Coverage: coverage, Earliest: earliest, History: need - 1, Source: source}
	}
	return end, nil
}

// IndexChangedError 表示读取期间 View 的活动索引换了一代（重建过）：新一代只保留最近的根数，调用方必须先按新索引
// 复查剩余区间，再决定是否切换过去继续读。
type IndexChangedError struct {
	View ViewInfo
}

func (e *IndexChangedError) Error() string {
	return fmt.Sprintf("View %s 的活动索引已换为 %s", e.View.ViewID, e.View.Generation)
}

// RangeLoader 为回放分段读取 View 的历史行：每段固定活动索引；活动索引换了一代时返回 *IndexChangedError，
// 不静默改读新索引。
type RangeLoader struct {
	Client   Client
	SpaceID  string
	View     ViewInfo
	Subjects []Subject
	Columns  []string
}

// RangeRows 是一段读取的结果：bar_start（Unix 秒）→ subject_id → 行；Ambiguous 记录同一标的同一周期出现多个序列的
// bar 与标的（bar_start → subject_id → 说明），这些标的在该 bar 上的行不能用于估值。
type RangeRows struct {
	Bars      map[int64]map[string]Row
	Ambiguous map[int64]map[string]string
}

// Load 读取 [start, end) 内全部标的的行。读取期间只是有新的写入（修订号变化、代次不变）时退避后重读，直到
// rangeStaleBudget 用完；活动索引换了一代返回 *IndexChangedError。
func (l *RangeLoader) Load(ctx context.Context, start, end time.Time) (RangeRows, error) {
	transportRetries, staleRetries := 0, 0
	deadline := time.Now().Add(rangeStaleBudget)
	for {
		rows, _, err := l.Client.QueryRows(ctx, l.SpaceID, Query{ViewID: l.View.ViewID, DatasetID: l.View.DatasetID, Frequency: l.View.Frequency, Subjects: l.Subjects, Start: start, End: end, Columns: l.Columns, ExpectedIndexID: l.View.ActiveIndexID})
		if err == nil {
			// A/B 槽位名在重建时复用，按槽位名固定的读取在两段之间重建两次（A→B→A）后会直接成功：读完还要确认代次没变。
			// 代次不会重复，读完仍是同一代，说明这段读取期间一直是它。
			view, viewErr := l.currentView(ctx, &transportRetries)
			if viewErr != nil {
				return RangeRows{}, viewErr
			}
			if view.Generation != l.View.Generation {
				return RangeRows{}, &IndexChangedError{View: view}
			}
			return groupRange(rows), nil
		}
		// 长回放有数百次分段读取：一次传输失败不应让整个回放失败，退避后重读同一段。
		var transportErr *TransportError
		if errors.As(err, &transportErr) && transportRetries < maxRangeTransportRetries {
			transportRetries++
			if sleepContext(ctx, time.Duration(transportRetries)*rangeRetryBackoff) != nil {
				return RangeRows{}, err
			}
			continue
		}
		if !errors.Is(err, ErrStale) {
			return RangeRows{}, err
		}
		view, viewErr := l.currentView(ctx, &transportRetries)
		if viewErr != nil {
			return RangeRows{}, viewErr
		}
		if view.Generation != l.View.Generation {
			return RangeRows{}, &IndexChangedError{View: view}
		}
		// 同一代索引，只是读取期间有新的写入：写入集中在 bar 边界，立即重读多半还会撞上同一批写入，退避（带抖动）后重读。
		wait := staleBackoff(staleRetries)
		staleRetries++
		if time.Now().Add(wait).After(deadline) {
			return RangeRows{}, &staleError{message: fmt.Sprintf("View %s 在读取期间持续有新的写入，%s 内没有读到一致的数据，请稍后重试", l.View.ViewID, rangeStaleBudget), raw: err}
		}
		if sleepContext(ctx, wait) != nil {
			return RangeRows{}, err
		}
	}
}

// currentView 读取 View 的当前元数据；传输失败与分段读取共用重试次数。
func (l *RangeLoader) currentView(ctx context.Context, transportRetries *int) (ViewInfo, error) {
	for {
		view, err := l.Client.GetView(ctx, l.SpaceID, l.View.ViewID)
		var transportErr *TransportError
		if err == nil || !errors.As(err, &transportErr) || *transportRetries >= maxRangeTransportRetries {
			return view, err
		}
		*transportRetries++
		if sleepContext(ctx, time.Duration(*transportRetries)*rangeRetryBackoff) != nil {
			return ViewInfo{}, err
		}
	}
}

const maxRangeTransportRetries = 3

var (
	// rangeRetryBackoff 是分段读取传输失败后的退避基数（第 n 次重试等待 n 倍）。
	rangeRetryBackoff = time.Second
	// rangeStaleBudget 是一段读取因写入冲突（修订号变化）而重读的总时长上限。
	rangeStaleBudget = time.Minute
	// rangeStaleBackoff 是写入冲突后第一次重读前的等待，之后逐次翻倍、最多 rangeStaleMaxBackoff，并加 ±50% 抖动。
	rangeStaleBackoff    = 250 * time.Millisecond
	rangeStaleMaxBackoff = 4 * time.Second
)

// staleBackoff 返回第 n 次写入冲突后的等待时长。
func staleBackoff(n int) time.Duration {
	wait := rangeStaleBackoff
	for i := 0; i < n && wait < rangeStaleMaxBackoff; i++ {
		wait *= 2
	}
	wait = min(wait, rangeStaleMaxBackoff)
	if wait <= 0 {
		return 0
	}
	return wait/2 + time.Duration(rand.Int64N(int64(wait)))
}

// sleepContext 等待 d，ctx 结束时提前返回其错误。
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func groupRange(rows []Row) RangeRows {
	result := RangeRows{Bars: make(map[int64]map[string]Row), Ambiguous: make(map[int64]map[string]string)}
	tags := make(map[int64]map[string]string)
	for _, row := range rows {
		if row.SubjectID == "" {
			continue
		}
		key := row.DataTime.Unix()
		if result.Bars[key] == nil {
			result.Bars[key] = make(map[string]Row)
			tags[key] = make(map[string]string)
		}
		if tag, seen := tags[key][row.SubjectID]; seen && tag != row.SeriesTag {
			if result.Ambiguous[key] == nil {
				result.Ambiguous[key] = make(map[string]string)
			}
			result.Ambiguous[key][row.SubjectID] = fmt.Sprintf("标的 %s 在同一周期有多个序列（%s、%s）", row.SubjectID, tag, row.SeriesTag)
			continue
		}
		tags[key][row.SubjectID] = row.SeriesTag
		result.Bars[key][row.SubjectID] = row
	}
	return result
}
