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

	"github.com/mooyang-code/moox/modules/strategy/internal/period"
	"github.com/mooyang-code/moox/modules/strategy/internal/upstream"
)

// DefaultReplayMaxBars 是一次回放允许的最多 bar 数。
const DefaultReplayMaxBars = 20000

// historyNeed 返回回放的每一根需要的历史根数（含自身）与来源说明：min_age_bars 的年龄目标是 T − (N−1) 根，
// bars[-1] 要读上一根。
func historyNeed(resolved Resolved, program *dsl.Program) (int, string) {
	need, sources := 1, make([]string, 0, 2)
	if longest := resolved.LongestAge(); longest > 1 {
		need = longest
		sources = append(sources, resolved.LongestAgeLabel())
	}
	if program != nil && program.UsesPreviousBar {
		need = max(need, 2)
		sources = append(sources, "bars[-1]")
	}
	return need, strings.Join(sources, "、")
}

// ErrNoCompleteBar 表示区间的起点已经在 View 最后一根完整的 bar 之后：区间内还没有可以推进的数据。
var ErrNoCompleteBar = errors.New("回放区间内没有完整的数据")

// EarlyStartError 表示回放起点早于可用起点：起点之前不够 min_age_bars 与 bars[-1] 要求的历史。
type EarlyStartError struct {
	// First 是区间内的第一根；Coverage 是活跃序列的覆盖起点；Earliest 是可用起点（都是 bar_start）。
	First    time.Time
	Coverage time.Time
	Earliest time.Time
	// History 是起点之前需要的根数，Source 是要求的来源（min_age_bars、bars[-1]）。
	History int
	Source  string
	// Calendar 与 Bar 决定时间的写法：股票市场日线写当地日期（A 股是上海日期）。
	Calendar string
	Bar      string
}

func (e *EarlyStartError) Error() string {
	label := func(at time.Time) string { return period.PeriodLabel(e.Calendar, e.Bar, at) }
	if e.History <= 0 {
		return fmt.Sprintf("回放起点 %s 太早：View 的活跃序列从 %s 起才有数据；可用起点为 %s", label(e.First), label(e.Coverage), label(e.Earliest))
	}
	return fmt.Sprintf("回放起点 %s 太早：View 的活跃序列从 %s 起才有数据，%s 要求起点之前还有 %d 根历史；可用起点为 %s", label(e.First), label(e.Coverage), e.Source, e.History, label(e.Earliest))
}

// ReplayWindow 校验并截断回放区间 [start, end)：
// 起点不能早于活跃序列的覆盖起点加上 min_age_bars 与 bars[-1] 所需的历史，否则返回 *EarlyStartError；
// 终点截到最新一根（按日历规整、并夹到 now 时已闭合的最后一根之内）之前：更晚的 bar 还没有数据，最新一根也可能还没
// 写完，都不能当作完整的 bar 回放。最新一根已经写完时（见 Bounds.ToClosed）回放到它为止。
func ReplayWindow(resolved Resolved, program *dsl.Program, view upstream.ViewInfo, start, end, now time.Time) (time.Time, error) {
	if !end.After(start) {
		return time.Time{}, errors.New("回放区间必须满足 start < end")
	}
	label := func(at time.Time) string { return period.PeriodLabel(resolved.Calendar, resolved.Bar, at) }
	need, source := historyNeed(resolved, program)
	bounds, boundsErr := CoverageBounds(view, resolved, now)
	// 稳定状态下覆盖起点是最新一根往前 SeriesBars−1 根，可用起点再往后 need−1 根，而回放只到最新一根之前：每一根连同
	// 自身所需的根数 need 不小于 SeriesBars 时没有任何一根可以回放。最新一根已经写完时它本身可以回放，交给下面按覆盖
	// 判断。
	if view.SeriesBars > 0 && need > 1 && need >= view.SeriesBars && (boundsErr != nil || !bounds.ToClosed) {
		return time.Time{}, fmt.Errorf("回放的每一根连同自身需要 %d 根数据（%s），但 View %s 每个序列只保留最近 %d 根、回放又只到最新一根之前，永远无法满足", need, source, view.DatasetID, view.SeriesBars)
	}
	if boundsErr != nil {
		return time.Time{}, coverageUnusable(view.DatasetID, boundsErr, "暂不能回放")
	}
	coverage := coverageStart(bounds, view, resolved, time.Time{})
	if coverage.After(bounds.To) {
		return time.Time{}, fmt.Errorf("View %s 的数据已超出%s内嵌交易日历（最晚一行比日历末日 %s 晚 %d 天），每个序列只保留最近 %d 根，日历之内已不能确定还保留着哪些 bar，暂不能回放；请更新日历数据", view.DatasetID, period.MarketSpacedName(resolved.Calendar), label(bounds.To), bounds.Beyond, view.SeriesBars)
	}
	latest, err := period.FromStorageStart(resolved.Calendar, resolved.Bar, bounds.To)
	if err != nil {
		return time.Time{}, err
	}
	// limit 是终点的上限，last 是最后一根可以回放的 bar。最新一根已经写完时它就是日历的最后一根：终点往后放一天
	// （日线即次日的日期键，区间仍是“不含结束日”的整日口径；日内同样包含最后一根），之后没有可知的交易日，
	// ReplayBars 枚举到日历末尾为止。
	limit, last := latest.StorageStart, latest.PreviousStart
	if bounds.ToClosed {
		limit, last = latest.StorageStart.Add(24*time.Hour), latest.StorageStart
	}
	if last.IsZero() {
		return time.Time{}, fmt.Errorf("View %s 还没有完整的 bar（最新一根 %s 可能还没写完），暂不能回放", view.DatasetID, label(latest.StorageStart))
	}
	if end.After(limit) {
		end = limit
	}
	if !end.After(start) {
		return time.Time{}, fmt.Errorf("%w：View %s 最后一根完整的 bar 是 %s", ErrNoCompleteBar, view.DatasetID, label(last))
	}
	// 先反向判断历史够不够：从覆盖起点往后推 need−1 根可能越过 A 股内嵌日历的末尾，会被误报成日历需要更新。
	bound := last
	if need > 1 {
		bound, err = period.HistoryStart(resolved.Calendar, resolved.Bar, last, need)
		if errors.Is(err, marketcalendar.ErrNoPreviousTradingDay) {
			bound, err = time.Time{}, nil
		}
		if err != nil {
			return time.Time{}, err
		}
	}
	if bound.IsZero() || coverage.After(bound) {
		if need > 1 {
			return time.Time{}, fmt.Errorf("View %s 的活跃序列当前只覆盖 %s 至 %s，回放的每一根连同自身需要 %d 根数据（%s），暂不能回放", view.DatasetID, label(coverage), label(latest.StorageStart), need, source)
		}
		return time.Time{}, fmt.Errorf("View %s 的活跃序列当前只覆盖 %s 至 %s，还没有可以回放的完整 bar", view.DatasetID, label(coverage), label(latest.StorageStart))
	}
	earliest := coverage
	if need > 1 {
		startBar, err := period.FromStorageStart(resolved.Calendar, resolved.Bar, coverage)
		if err != nil {
			return time.Time{}, err
		}
		barEnd, err := period.AdvanceBarEnd(resolved.Calendar, resolved.Bar, startBar.BarEnd, need-1)
		if err != nil {
			return time.Time{}, err
		}
		shifted, err := period.FromBarEnd(resolved.Calendar, resolved.Bar, barEnd)
		if err != nil {
			return time.Time{}, err
		}
		earliest = shifted.StorageStart
	}
	first, err := period.FirstBarAtOrAfter(resolved.Calendar, resolved.Bar, start)
	if err != nil {
		return time.Time{}, err
	}
	if first.StorageStart.Before(earliest) {
		return time.Time{}, &EarlyStartError{First: first.StorageStart, Coverage: coverage, Earliest: earliest, History: need - 1, Source: source, Calendar: resolved.Calendar, Bar: resolved.Bar}
	}
	return end, nil
}

// IndexChangedError 表示读取期间 View 的活动索引换了一代（重建过）：新一代只保留最近的根数，调用方必须先按新索引
// 复查剩余区间，再决定是否切换过去继续读。
type IndexChangedError struct {
	View upstream.ViewInfo
}

func (e *IndexChangedError) Error() string {
	return fmt.Sprintf("View %s 的活动索引已换为 %s", e.View.DatasetID, e.View.Generation)
}

// RangeLoader 为回放分段读取 View 的历史行：每段固定活动索引；活动索引换了一代时返回 *IndexChangedError，
// 不静默改读新索引。
type RangeLoader struct {
	Client   upstream.Client
	SpaceID  string
	View     upstream.ViewInfo
	Subjects []upstream.Subject
	Columns  []string
	// ExpectedRevision 非零时固定读取的索引修订号（取自同一段主数据读取返回的修订号）：修订号变化时不退避重读，
	// 直接返回 *RevisionChangedError，由调用方连同主数据一起重读，保证同一段的两次读取来自同一修订号。
	ExpectedRevision uint64
	// Abort 在每次退避重读之前调用，返回非空错误时放弃读取并原样返回它（例如回放已被取消）；可以为空。
	Abort func(context.Context) error
}

// RangeRows 是一段读取的结果：bar_start（Unix 秒）→ subject_id → 行；Ambiguous 记录同一标的同一周期出现多个序列的
// bar 与标的（bar_start → subject_id → 说明），这些标的在该 bar 上的行不能用于估值。
type RangeRows struct {
	Bars      map[int64]map[string]upstream.Row
	Ambiguous map[int64]map[string]string
	// Revision 是这段读取时服务端返回的索引修订号，供同一段的其他读取固定到同一修订号。
	Revision uint64
}

// RevisionChangedError 表示固定了修订号的读取遇到修订号变化：这段读取与它所依赖的主数据已不是同一修订号。
type RevisionChangedError struct{ raw error }

func (e *RevisionChangedError) Error() string {
	return "View 的索引修订号在两次读取之间发生了变化，需要整体重读"
}
func (e *RevisionChangedError) Unwrap() error { return e.raw }

// Load 读取 [start, end) 内全部标的的行。读取期间只是有新的写入（修订号变化、代次不变）时退避后重读，直到
// rangeStaleBudget 用完；活动索引换了一代返回 *IndexChangedError；ctx 结束返回 ctx 的错误。
func (l *RangeLoader) Load(ctx context.Context, start, end time.Time) (RangeRows, error) {
	transportRetries, staleRetries := 0, 0
	deadline := time.Now().Add(rangeStaleBudget)
	for {
		rows, revision, err := l.queryRows(ctx, start, end)
		if err == nil {
			// A/B 槽位名在重建时复用，按槽位名固定的读取在两段之间重建两次（A→B→A）后会直接成功：读完还要确认代次没变。
			// 代次不会重复，读完仍是同一代，说明这段读取期间一直是它。
			if err := l.checkGeneration(ctx); err != nil {
				return RangeRows{}, err
			}
			grouped := groupRange(rows)
			grouped.Revision = revision
			return grouped, nil
		}
		// 长回放有数百次分段读取：一次传输失败不应让整个回放失败，退避后重读同一段。
		var transportErr *upstream.TransportError
		if errors.As(err, &transportErr) && transportRetries < maxRangeTransportRetries {
			transportRetries++
			if err := l.wait(ctx, time.Duration(transportRetries)*rangeRetryBackoff); err != nil {
				return RangeRows{}, err
			}
			continue
		}
		if !errors.Is(err, upstream.ErrStale) {
			return RangeRows{}, err
		}
		if err := l.checkGeneration(ctx); err != nil {
			return RangeRows{}, err
		}
		if l.ExpectedRevision != 0 {
			return RangeRows{}, &RevisionChangedError{raw: err}
		}
		// 同一代索引，只是读取期间有新的写入：写入集中在 bar 边界，立即重读多半还会撞上同一批写入，退避（带抖动）后重读。
		wait := staleBackoff(staleRetries)
		staleRetries++
		if time.Now().Add(wait).After(deadline) {
			return RangeRows{}, upstream.NewStaleError(fmt.Sprintf("View %s 在读取期间持续有新的写入，%s内没有读到一致的数据，请稍后重试", l.View.DatasetID, durationText(rangeStaleBudget)), err)
		}
		if err := l.wait(ctx, wait); err != nil {
			return RangeRows{}, err
		}
	}
}

// queryRows 读一段原始行。
func (l *RangeLoader) queryRows(ctx context.Context, start, end time.Time) ([]upstream.Row, uint64, error) {
	return l.Client.QueryRows(ctx, l.SpaceID, upstream.Query{DatasetID: l.View.DatasetID, Frequency: l.View.Frequency, Subjects: l.Subjects, Start: start, End: end, Columns: l.Columns, ExpectedIndexID: l.View.ActiveIndexID, ExpectedRevision: l.ExpectedRevision})
}

// wait 等待 d，等待前后都确认是否要放弃（Abort）：取消可能就落在等待期间；ctx 结束时返回 ctx 的错误。
func (l *RangeLoader) wait(ctx context.Context, d time.Duration) error {
	if err := l.abort(ctx); err != nil {
		return err
	}
	if err := sleepContext(ctx, d); err != nil {
		return err
	}
	return l.abort(ctx)
}

func (l *RangeLoader) abort(ctx context.Context) error {
	if l.Abort == nil {
		return nil
	}
	return l.Abort(ctx)
}

// checkGeneration 确认活动索引仍是读取器固定的那一代，只读代次、不读列；换代时读取完整的 View 并返回
// *IndexChangedError（完整读到的仍是同一代时视为没有换代）。两次读取的传输失败都单独计重试，不占用读取行的重试次数
// （否则读行用完重试后，一次复核失败就会丢掉已读的行）。
func (l *RangeLoader) checkGeneration(ctx context.Context) error {
	generation, err := retryTransport(ctx, l, func() (string, error) {
		return l.Client.ViewGeneration(ctx, l.SpaceID, l.View.DatasetID)
	})
	if err != nil || generation == l.View.Generation {
		return err
	}
	view, err := retryTransport(ctx, l, func() (upstream.ViewInfo, error) {
		return l.Client.GetView(ctx, l.SpaceID, l.View.DatasetID)
	})
	if err != nil || view.Generation == l.View.Generation {
		return err
	}
	return &IndexChangedError{View: view}
}

// retryTransport 调用 call，遇到传输失败时按分段读取的退避重试（最多 maxRangeTransportRetries 次）。
func retryTransport[T any](ctx context.Context, l *RangeLoader, call func() (T, error)) (T, error) {
	for retries := 0; ; retries++ {
		value, err := call()
		var transportErr *upstream.TransportError
		if err == nil || !errors.As(err, &transportErr) || retries >= maxRangeTransportRetries {
			return value, err
		}
		if waitErr := l.wait(ctx, time.Duration(retries+1)*rangeRetryBackoff); waitErr != nil {
			var zero T
			return zero, waitErr
		}
	}
}

// durationText 把时长写成中文（整分钟写“N 分钟”，否则写秒或毫秒）。
func durationText(d time.Duration) string {
	switch {
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%d 分钟", d/time.Minute)
	case d >= time.Second:
		return fmt.Sprintf("%d 秒", d/time.Second)
	default:
		return fmt.Sprintf("%d 毫秒", d/time.Millisecond)
	}
}

const maxRangeTransportRetries = 3

var (
	// rangeRetryBackoff 是分段读取传输失败后的退避基数（第 n 次重试等待 n 倍）。
	rangeRetryBackoff = time.Second
	// rangeStaleBudget 是一段读取因写入冲突（修订号变化）而重读的总时长上限。
	rangeStaleBudget = time.Minute
	// rangeStaleBackoff 是写入冲突后第一次重读前的等待，之后逐次翻倍、最多 rangeStaleMaxBackoff，再加 ±50% 抖动
	// （实际等待最长约 1.5 × rangeStaleMaxBackoff）。
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

// GroupRows 按 bar_start 与标的整理一批行，规则与 RangeLoader 读回的行相同：同一标的同一周期有多个序列时记入 Ambiguous。
// “因子现算”来源用它整理引擎返回的行，歧义判断与 View 来源一致。
func GroupRows(rows []upstream.Row) RangeRows { return groupRange(rows) }

func groupRange(rows []upstream.Row) RangeRows {
	result := RangeRows{Bars: make(map[int64]map[string]upstream.Row), Ambiguous: make(map[int64]map[string]string)}
	tags := make(map[int64]map[string]string)
	markAmbiguous := func(key int64, subjectID, detail string) {
		if result.Ambiguous[key] == nil {
			result.Ambiguous[key] = make(map[string]string)
		}
		result.Ambiguous[key][subjectID] = detail
	}
	for _, row := range rows {
		if row.SubjectID == "" {
			continue
		}
		key := row.DataTime.Unix()
		if result.Bars[key] == nil {
			result.Bars[key] = make(map[string]upstream.Row)
			tags[key] = make(map[string]string)
		}
		if tag, seen := tags[key][row.SubjectID]; seen && tag != row.SeriesTag {
			markAmbiguous(key, row.SubjectID, fmt.Sprintf("标的 %s 在同一周期有多个序列（%s、%s）", row.SubjectID, tag, row.SeriesTag))
			continue
		}
		tags[key][row.SubjectID] = row.SeriesTag
		result.Bars[key][row.SubjectID] = row
	}
	return result
}
