package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/watermark"
	"github.com/mooyang-code/moox/packages/seriesset"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultMaxMissingRatio = 0.1
	defaultMaxPeriods      = 30
)

// PeriodReader 读回源数据集某一期的行（判断没有事件可用的周期哪些标的已经到了）。
type PeriodReader interface {
	ReadWindow(context.Context, storageio.ReadRequest) (map[string]*storageio.Frame, error)
}

// ContinuityConfig 是连续水位的参数。
type ContinuityConfig struct {
	Store  *watermark.Store
	Reader PeriodReader
	// MaxMissingRatio 是缺失阈值：未确认缺失的失败标的 ÷ 期望标的超过它的周期视为无效，阻塞水位（默认 10%）；
	// SetRatios 按任务 ID 覆盖。
	MaxMissingRatio float64
	SetRatios       map[string]float64
	// MaxPeriods 是一次推进最多处理的周期数，剩下的交给追赶循环。
	MaxPeriods int
	Now        func() time.Time
}

// Continuity 维护每个任务的连续水位：从水位下一期逐期处理到最新已知周期，前面有还没补到的无效周期就停住，
// 水位不前进；当期缺失在阈值以内时先算已到的标的，缺的记进空洞账本由补采后的只填充补算补上。
type Continuity struct {
	cfg ContinuityConfig

	mu sync.Mutex
	// absent 是补采确认行情源没有的 (任务, 周期) 标的：不计入缺失，也不阻塞。
	absent map[string]map[int64]absentEntry
}

type absentEntry struct {
	subjects map[string]struct{}
	// series 是统一数据集里按序列确认没有的（标的、series_tag）：只扣这一条序列，同标的的另一个市场照常算。
	series map[seriesset.Series]struct{}
	until  time.Time
}

// NewContinuity 构造连续水位推进。
func NewContinuity(cfg ContinuityConfig) (*Continuity, error) {
	if cfg.Store == nil || cfg.Reader == nil {
		return nil, errors.New("continuity requires a watermark store and a period reader")
	}
	if cfg.MaxMissingRatio <= 0 {
		cfg.MaxMissingRatio = defaultMaxMissingRatio
	}
	if cfg.MaxPeriods <= 0 {
		cfg.MaxPeriods = defaultMaxPeriods
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Continuity{cfg: cfg, absent: map[string]map[int64]absentEntry{}}, nil
}

// States 返回全部任务的推进状态（心跳上报与指标用）。
func (c *Continuity) States() []watermark.State { return c.cfg.Store.All() }

// NoteAbsent 记下补采确认行情源没有的标的（补齐通知带来），30 分钟后过期。
// subjects 是按标的确认的（该标的的所有序列都没有），series 是按序列确认的（只有这一条序列没有）。
func (c *Continuity) NoteAbsent(taskID string, from, to time.Time, step time.Duration, subjects []string, series []seriesset.Series) {
	if (len(subjects) == 0 && len(series) == 0) || step <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.absent[taskID] == nil {
		c.absent[taskID] = map[int64]absentEntry{}
	}
	until := c.cfg.Now().Add(30 * time.Minute)
	for period := from.UTC(); period.Before(to); period = period.Add(step) {
		entry := c.absent[taskID][period.Unix()]
		if entry.subjects == nil {
			entry.subjects = map[string]struct{}{}
		}
		if entry.series == nil {
			entry.series = map[seriesset.Series]struct{}{}
		}
		for _, subject := range subjects {
			entry.subjects[strings.TrimSpace(subject)] = struct{}{}
		}
		for _, item := range series {
			entry.series[seriesset.Series{SubjectID: strings.TrimSpace(item.SubjectID), SeriesTag: strings.TrimSpace(item.SeriesTag)}] = struct{}{}
		}
		entry.until = until
		c.absent[taskID][period.Unix()] = entry
	}
}

// absentAt 返回这一期确认没有的标的与序列（复制一份：NoteAbsent 在心跳线程里会修改同一个任务）。
func (c *Continuity) absentAt(taskID string, period time.Time) (map[string]struct{}, map[seriesset.Series]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	for key, entry := range c.absent[taskID] {
		if now.After(entry.until) {
			delete(c.absent[taskID], key)
		}
	}
	entry := c.absent[taskID][period.Unix()]
	subjects := make(map[string]struct{}, len(entry.subjects))
	for subject := range entry.subjects {
		subjects[subject] = struct{}{}
	}
	series := make(map[seriesset.Series]struct{}, len(entry.series))
	for item := range entry.series {
		series[item] = struct{}{}
	}
	return subjects, series
}

func (c *Continuity) ratioFor(taskID string) float64 {
	if ratio, ok := c.cfg.SetRatios[taskID]; ok && ratio > 0 {
		return ratio
	}
	return c.cfg.MaxMissingRatio
}

// onEvent 记下事件带来的最新周期与期望标的，然后推进。
func (c *Continuity) onEvent(ctx context.Context, h *Handler, set domain.FactorTask, members []domain.MemberDef, event periodEvent) error {
	// 连续水位全部在轴上：事件周期在入口已换到轴上，轴是连续的。
	clock := periodclock.Continuous{}
	step, err := clock.Duration(set.Freq)
	if err != nil {
		return err
	}
	period, err := clock.Align(event.periodTime, set.Freq)
	if err != nil {
		return err
	}
	state, ok := c.cfg.Store.Get(set.TaskID)
	if !ok || state.CommittedThrough.IsZero() {
		// 第一次见到这个任务：从这一期起保证连续，不回头补历史（历史用补算作业）。
		state = watermark.State{TaskID: set.TaskID, CommittedThrough: period.Add(-step)}
	}
	state.SpaceID, state.DatasetID, state.Freq = set.SpaceID, set.SourceDatasetID, set.Freq
	if period.After(state.Target) {
		state.Target = period
	}
	if len(event.universe) > 0 && !period.Before(state.Target) {
		state.Universe = append([]string(nil), event.universe...)
		state.UniverseSeries = append([]seriesset.Series(nil), event.universeSeries...)
	}
	if err := c.cfg.Store.Put(state); err != nil {
		return fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, err)
	}
	return c.advance(ctx, h, set, members, step, &event)
}

// NoteTarget 显式把任务的追赶目标推到 period（取已有目标与 period 中较晚的）。
// 暂停期间的实时事件在连续水位目标更新之前就被 ACK，目标会停在暂停前；恢复追赶时由这里显式建立目标，
// 不等下一条实时事件。任务还没有水位记录（从未跑过实时周期）时不建立：连续性从第一次实时事件起保证。
func (c *Continuity) NoteTarget(taskID string, period time.Time) error {
	state, ok := c.cfg.Store.Get(taskID)
	if !ok || state.CommittedThrough.IsZero() || !period.After(state.Target) {
		return nil
	}
	state.Target = period
	if err := c.cfg.Store.Put(state); err != nil {
		return fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, err)
	}
	return nil
}

// advanceTask 在没有事件时推进（追赶循环、补齐通知）。
func (c *Continuity) advanceTask(ctx context.Context, h *Handler, taskID string) error {
	state, ok := c.cfg.Store.Get(taskID)
	if !ok || !state.Behind() {
		return nil
	}
	engineTask, found, err := h.sets.EnabledTask(ctx, taskID)
	if err != nil || !found || engineTask.Set.Status != domain.TaskStatusEnabled || !engineTask.ResultReady {
		return err
	}
	set, members := engineTask.Set, engineTask.Members
	step, err := periodclock.Continuous{}.Duration(set.Freq)
	if err != nil {
		return err
	}
	return c.advance(ctx, h, set, members, step, nil)
}

// advance 从水位下一期逐期处理到最新已知周期（一次至多 MaxPeriods 期）。
func (c *Continuity) advance(ctx context.Context, h *Handler, set domain.FactorTask, members []domain.MemberDef, step time.Duration, event *periodEvent) error {
	state, _ := c.cfg.Store.Get(set.TaskID)
	for processed := 0; state.Behind() && processed < c.cfg.MaxPeriods; processed++ {
		period := state.CommittedThrough.Add(step)
		scope, triggerID := periodScope{universe: state.Universe, universeSeries: state.UniverseSeries}, CatchupEventID(set.TaskID, period)
		if event != nil && event.periodTime.Equal(period) {
			scope, triggerID = event.scope(), event.eventID
		} else if len(scope.universeSeries) > 0 {
			missing, err := c.readbackSeries(ctx, h, set, members, period, scope.universeSeries)
			if err != nil {
				return err
			}
			scope.failedSeries = missing
		} else {
			missing, err := c.readback(ctx, h, set, members, period, scope.universe)
			if err != nil {
				return err
			}
			scope.failed = missing
		}
		if state.InFlightTrigger != "" && state.InFlightPeriod.Equal(period) {
			triggerID = state.InFlightTrigger
		}
		// 有序列全集（统一数据集）时，期望与缺失按序列数计：一个市场整体晚到不会让整期按标的全部缺失而阻塞水位。
		counts, confirmed, scope := c.scoped(set, period, scope)
		expected, unconfirmed := counts.expected, counts.unconfirmed
		if reason := c.invalid(set.TaskID, counts.judgeExpected, counts.judgeMissing, confirmed); reason != "" {
			if state.BlockedSince.IsZero() || !state.BlockedPeriod.Equal(period) {
				if state.BlockedSince.IsZero() {
					state.BlockedSince = c.cfg.Now().UTC()
				}
				log.WarnContextf(ctx, "factor_watermark_blocked task_id=%s period=%s reason=%s missing=%d expected=%d", set.TaskID, periodclock.FormatPeriod(set.SpaceID, set.Freq, period), reason, unconfirmed, expected)
			}
			state.BlockedPeriod, state.BlockedReason = period, reason
			if err := c.cfg.Store.Put(state); err != nil {
				return fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, err)
			}
			return nil
		}
		// 补采确认行情源没有的标的不参与计算：不读窗口、不记空洞账本、不补算。
		// 范围内的标的全部确认没有时这一期没有可算的，直接推进。
		if expected > 0 {
			state.InFlightPeriod, state.InFlightTrigger = period, triggerID
			if err := c.cfg.Store.Put(state); err != nil {
				return fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, err)
			}
			if _, err := h.computePeriod(ctx, set, members, period, scope, triggerID); err != nil {
				return err
			}
		}
		if !state.BlockedSince.IsZero() {
			log.InfoContextf(ctx, "factor_watermark_unblocked task_id=%s period=%s blocked_for=%s", set.TaskID, periodclock.FormatPeriod(set.SpaceID, set.Freq, period), c.cfg.Now().Sub(state.BlockedSince).Truncate(time.Second))
		}
		state.CommittedThrough, state.BlockedSince, state.BlockedPeriod, state.BlockedReason = period, time.Time{}, time.Time{}, ""
		state.InFlightPeriod, state.InFlightTrigger = time.Time{}, ""
		if err := c.cfg.Store.Put(state); err != nil {
			return fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, err)
		}
	}
	return nil
}

// periodCounts 是一期的期望与缺失数：expected、unconfirmed 是总数（标的数，统一数据集是序列数）；
// judgeExpected、judgeMissing 是判断这一期有没有效的数：单序列数据集就是总数，统一数据集取缺失比例最低的那个 series_tag 的数。
type periodCounts struct {
	expected, unconfirmed       int
	judgeExpected, judgeMissing int
}

// scoped 整理一期的范围：扣除补采确认没有的标的，返回期望数与缺失数、确认没有的标的，以及真正要算的范围。
// 单序列数据集按标的计数（原有口径）；有序列全集（统一数据集）时按序列计数，要算的范围里也带按序列的全集与失败名单，
// 并且按 series_tag 分别看缺失比例：只要有一个市场的缺失在阈值以内，这一期就有效，另一个市场整体晚到不会连累它
// （晚到的序列已经列在失败名单里，记进空洞账本，补齐后由只填充补算补上）。
func (c *Continuity) scoped(set domain.FactorTask, period time.Time, scope periodScope) (counts periodCounts, confirmed map[string]struct{}, out periodScope) {
	subjects, missing, confirmed := c.missing(set, period, scope.universe, scope.failed)
	out = periodScope{universe: withoutSubjects(scope.universe, confirmed), failed: missing}
	if len(scope.universeSeries) == 0 {
		return periodCounts{len(subjects), len(missing), len(subjects), len(missing)}, confirmed, out
	}
	// 按序列确认没有的（例如永续没有、现货有）只扣这一条序列；同标的的另一条序列仍在范围内。
	_, absentSeries := c.absentAt(set.TaskID, period)
	for _, item := range scope.universeSeries {
		if _, absent := confirmed[strings.TrimSpace(item.SubjectID)]; absent {
			continue
		}
		if _, absent := absentSeries[item]; absent {
			confirmed[item.SubjectID] = struct{}{} // 只用于“这一期有没有确认缺失”的判断与日志
			continue
		}
		out.universeSeries = append(out.universeSeries, item)
	}
	out.universe = seriesSubjectList(out.universeSeries)
	inScope := make(map[seriesset.Series]struct{}, len(out.universeSeries))
	expectedByTag, missingByTag := map[string]int{}, map[string]int{}
	for _, item := range out.universeSeries {
		inScope[item] = struct{}{}
		expectedByTag[item.SeriesTag]++
	}
	var failedSubjects []string
	for _, item := range scope.failedSeries {
		if _, ok := inScope[item]; ok {
			out.failedSeries = append(out.failedSeries, item)
			failedSubjects = append(failedSubjects, item.SubjectID)
			missingByTag[item.SeriesTag]++
		}
	}
	out.failed = uniqueStrings(failedSubjects)
	counts = periodCounts{expected: len(out.universeSeries), unconfirmed: len(out.failedSeries)}
	first := true
	for tag, expected := range expectedByTag {
		missed := missingByTag[tag]
		// 比例最低的 tag 的数；比例相同取缺失少的、再取 tag 小的，结果不依赖 map 的遍历顺序。
		better := first || missed*counts.judgeExpected < counts.judgeMissing*expected
		if !better && missed*counts.judgeExpected == counts.judgeMissing*expected {
			better = missed < counts.judgeMissing || (missed == counts.judgeMissing && expected > counts.judgeExpected)
		}
		if better {
			counts.judgeExpected, counts.judgeMissing, first = expected, missed, false
		}
	}
	return counts, confirmed, out
}

// missing 返回该期期望的标的（该期事件的全集，扣除确认没有的）、未确认缺失的失败标的与确认没有的标的。
func (c *Continuity) missing(set domain.FactorTask, period time.Time, universe, failed []string) ([]string, []string, map[string]struct{}) {
	absent, _ := c.absentAt(set.TaskID, period)
	var expected []string
	confirmed := map[string]struct{}{}
	for _, subject := range uniqueStrings(universe) {
		if _, ok := absent[subject]; ok {
			confirmed[subject] = struct{}{}
			continue
		}
		expected = append(expected, subject)
	}
	inExpected := make(map[string]struct{}, len(expected))
	for _, subject := range expected {
		inExpected[subject] = struct{}{}
	}
	var unconfirmed []string
	for _, subject := range uniqueStrings(failed) {
		if _, ok := inExpected[subject]; ok {
			unconfirmed = append(unconfirmed, subject)
		}
	}
	return expected, unconfirmed, confirmed
}

// seriesSubjectList 返回序列涉及的标的（去重、排序）。
func seriesSubjectList(values []seriesset.Series) []string {
	subjects := make([]string, 0, len(values))
	for _, item := range values {
		subjects = append(subjects, item.SubjectID)
	}
	return uniqueStrings(subjects)
}

// withoutSubjects 返回去掉给定标的后的列表。
func withoutSubjects(values []string, drop map[string]struct{}) []string {
	if len(drop) == 0 {
		return values
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := drop[strings.TrimSpace(value)]; !ok {
			out = append(out, value)
		}
	}
	return out
}

// invalid 判断一期是否无效：不知道期望数量、零提交（全部缺失）、缺失超过阈值。数量是标的数，统一数据集是序列数。
// 范围内的标的全部确认没有时不算无效（没有可算的，直接推进）。
func (c *Continuity) invalid(taskID string, expected, missing int, confirmed map[string]struct{}) string {
	switch {
	case expected == 0 && len(confirmed) > 0:
		return ""
	case expected == 0:
		return "no_universe"
	case missing == expected:
		return "no_rows"
	case float64(missing)/float64(expected) > c.ratioFor(taskID):
		return "missing_over_threshold"
	}
	return ""
}

// readback 读回一期源数据，返回还没有行的期望标的。
func (c *Continuity) readback(ctx context.Context, h *Handler, set domain.FactorTask, members []domain.MemberDef, period time.Time, universe []string) ([]string, error) {
	subjects := uniqueStrings(universe)
	if len(subjects) == 0 {
		return nil, nil
	}
	column := "close"
	for _, member := range members {
		if len(member.Factor.InputColumns) > 0 {
			column = member.Factor.InputColumns[0]
			break
		}
	}
	step := periodStep(set)
	readCtx, cancel := context.WithTimeout(ctx, h.cfg.ReadColumnTimeout)
	defer cancel()
	frames, err := c.cfg.Reader.ReadWindow(readCtx, storageio.ReadRequest{SpaceID: set.SpaceID, DatasetID: set.SourceDatasetID, Freq: set.Freq, Subjects: subjects, Start: period, End: period.Add(step), Columns: []string{column}})
	if err != nil {
		return nil, fmt.Errorf("%w: read back period %s: %v", storageio.ErrInfra, periodclock.FormatPeriod(set.SpaceID, set.Freq, period), err)
	}
	var missing []string
	for _, subject := range subjects {
		if frame := frames[subject]; frame == nil || len(frame.Rows) == 0 {
			missing = append(missing, subject)
		}
	}
	return missing, nil
}

// readbackSeries 读回一期源数据，返回还没有行的期望序列（统一数据集按序列判断：永续没到、现货到了，只有永续算缺失）。
func (c *Continuity) readbackSeries(ctx context.Context, h *Handler, set domain.FactorTask, members []domain.MemberDef, period time.Time, universe []seriesset.Series) ([]seriesset.Series, error) {
	var subjects []string
	for _, item := range universe {
		subjects = append(subjects, item.SubjectID)
	}
	subjects = uniqueStrings(subjects)
	if len(subjects) == 0 {
		return nil, nil
	}
	column := "close"
	for _, member := range members {
		if len(member.Factor.InputColumns) > 0 {
			column = member.Factor.InputColumns[0]
			break
		}
	}
	step := periodStep(set)
	readCtx, cancel := context.WithTimeout(ctx, h.cfg.ReadColumnTimeout)
	defer cancel()
	frames, err := c.cfg.Reader.ReadWindow(readCtx, storageio.ReadRequest{SpaceID: set.SpaceID, DatasetID: set.SourceDatasetID, Freq: set.Freq, Subjects: subjects, Start: period, End: period.Add(step), Columns: []string{column}})
	if err != nil {
		return nil, fmt.Errorf("%w: read back period %s: %v", storageio.ErrInfra, periodclock.FormatPeriod(set.SpaceID, set.Freq, period), err)
	}
	present := make(map[seriesset.Series]struct{})
	for subject, frame := range frames {
		if frame == nil {
			continue
		}
		for _, row := range frame.Rows {
			if len(row) > 1 {
				if tag, ok := row[1].(string); ok {
					present[seriesset.Series{SubjectID: subject, SeriesTag: tag}] = struct{}{}
				}
			}
		}
	}
	var missing []seriesset.Series
	for _, item := range universe {
		if _, ok := present[item]; !ok {
			missing = append(missing, item)
		}
	}
	return missing, nil
}

func periodStep(set domain.FactorTask) time.Duration {
	step, err := periodclock.Continuous{}.Duration(set.Freq)
	if err != nil {
		return time.Minute
	}
	return step
}

// CatchupEventID 是没有事件可用、靠读回推进的周期使用的触发事件 ID。
func CatchupEventID(taskID string, period time.Time) string {
	return fmt.Sprintf("catchup:%s:%d", taskID, period.Unix())
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
