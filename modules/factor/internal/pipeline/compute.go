package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorexpr/exec"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// Compute 每个集合每期（补算每个分块）调用一次 factorexpr 程序（设计 8）：集合内可计算的因子编译成一个程序，
// 在 available 标的的网格面板上算出全部目标时刻，再按源行转换成逐行结果。因子级的判断（严格截面因子跳过、
// 空洞标的失败）都在这里完成。
func (r *Runner) Compute(ctx context.Context, plan Plan, loaded LoadResult) (Computation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil || r.clock == nil {
		return Computation{}, errors.New("factor period clock is required")
	}
	if len(plan.Factors) == 0 {
		return Computation{Results: map[string]map[string]ItemResult{}, FactorStates: map[string]storageio.FactorState{}}, nil
	}
	target, err := planTarget(r.clock, plan)
	if err != nil {
		return Computation{}, err
	}
	targets := []time.Time{target}
	if plan.Mode == ModeRecalc {
		targets, err = periodRange(r.clock, plan.TargetStart, plan.TargetEnd, plan.Set.Freq)
		if err != nil {
			return Computation{}, err
		}
	}
	gaps := loaded.Gaps
	available := intersectLists(plan.Expected, loaded.Available)
	missing := subjectDifference(plan.Expected, available)
	missing = uniqueSorted(append(missing, loaded.FailedSubjects...))
	computation := Computation{
		CrossPeriods: make(map[string]map[int64]periodOutcome),
		Results:      make(map[string]map[string]ItemResult, len(plan.Factors)),
		FactorStates: make(map[string]storageio.FactorState, len(plan.Factors)),
	}
	compiled := make(map[string]domain.FactorDef, len(plan.Factors))
	compileErrs := make(map[string]error)
	for _, factor := range plan.Factors {
		def, compileErr := r.compile(factor)
		if compileErr != nil {
			compileErrs[factor.FactorID] = fmt.Errorf("因子 %s 在引擎侧编译失败：%w", factor.FactorID, compileErr)
			if r.cfg.OnCompileError != nil {
				r.cfg.OnCompileError(plan.Set.SetID, factor.FactorID)
			}
		}
		compiled[factor.FactorID] = def
		status := "complete"
		failed := append([]string(nil), missing...)
		// 回看窗口有 K 线空洞的标的对该因子失败（gap_subjects 是 failed_subjects 的子集）。
		gapSubjects := gaps.FactorSubjects(factor.FactorID)
		failed = uniqueSorted(append(failed, gapSubjects...))
		if factor.FactorType == domain.FactorTypeCrossSection && len(missing) > 0 && !factor.AllowPartialUniverse {
			status = "skipped"
		}
		// 严格截面因子：实时周期只要有标的有洞就整因子 skipped；补算按周期逐个跳过（见 crossPeriods），不整块跳过。
		if factor.FactorType == domain.FactorTypeCrossSection && len(gapSubjects) > 0 && !factor.AllowPartialUniverse && plan.Mode == ModeLive {
			status = "skipped"
		}
		if factor.FactorType == domain.FactorTypeCrossSection && len(available) == 0 {
			status = "skipped"
		}
		if len(failed) > 0 && status != "skipped" {
			status = "degraded"
		}
		computation.Results[factor.FactorID] = make(map[string]ItemResult)
		computation.FactorStates[factor.FactorID] = storageio.FactorState{
			FactorID: factor.FactorID, Status: status, FailedSubjects: failed, SourceHash: factor.SpecHash,
			DefinitionHash: domain.DefinitionHash(def), GapSubjects: gapSubjects,
		}
	}

	// 参与本次程序的因子：时序因子全部参与；截面因子未被整因子跳过才参与。
	var runnable []domain.FactorDef
	for _, factor := range plan.Factors {
		if factor.FactorType == domain.FactorTypeCrossSection {
			if computation.FactorStates[factor.FactorID].Status == "skipped" {
				continue
			}
			computation.crossPeriods(factor, gaps, targets)
		}
		if err := compileErrs[factor.FactorID]; err != nil {
			computation.fail(factor, plan, available, err)
			continue
		}
		runnable = append(runnable, compiled[factor.FactorID])
	}
	if len(runnable) == 0 || len(available) == 0 {
		return computation, nil
	}

	workCtx := ctx
	cancel := func() {}
	if plan.Budget > 0 {
		workCtx, cancel = context.WithTimeout(ctx, plan.Budget)
	}
	defer cancel()
	result, grid, runErr := r.run(workCtx, plan, runnable, available, loaded.Frames)
	if ctx.Err() != nil {
		return computation, ctx.Err()
	}
	computation.BudgetHit = errors.Is(workCtx.Err(), context.DeadlineExceeded)
	if runErr != nil {
		// 补算分块超出内存预算是资源错误：整块失败、不写入、不推进进度（分块在读取前已按预算收窄，正常不会出现）。
		if plan.Mode == ModeRecalc && errors.Is(runErr, ErrMemoryBudget) {
			return computation, runErr
		}
		for _, factor := range runnable {
			computation.fail(factor, plan, available, runErr)
		}
		return computation, nil
	}
	for _, factor := range runnable {
		if factor.FactorType == domain.FactorTypeCrossSection {
			computation.addCross(factor, result, grid)
		} else {
			computation.addTimeSeries(factor, result, grid)
		}
	}
	return computation, nil
}

// compile 返回编译好的定义。引擎目录里的定义来自 JSON 快照，没有编译结果；按 (factor_id, spec) 缓存编译结果。
func (r *Runner) compile(factor domain.FactorDef) (domain.FactorDef, error) {
	if factor.Compiled() != nil {
		return factor, nil
	}
	key := factor.FactorID + "\x00" + factor.Spec
	if cached, ok := r.compiled.Load(key); ok {
		def := cached.(domain.FactorDef)
		def.Name, def.SpecHash, def.AllowPartialUniverse = factor.Name, factor.SpecHash, factor.AllowPartialUniverse
		def.CreatedAt, def.UpdatedAt = factor.CreatedAt, factor.UpdatedAt
		return def, nil
	}
	def, err := domain.CompileDefinition(factor)
	if err != nil {
		return factor, err
	}
	r.compiled.Store(key, def)
	return def, nil
}

// computeGrid 是一次计算的时间网格与目标区间（网格下标）。
type computeGrid struct {
	start    time.Time
	duration time.Duration
	first    int
	last     int
}

func (g computeGrid) at(t int) time.Time { return g.start.Add(time.Duration(t) * g.duration) }

func (g computeGrid) index(at time.Time) int { return int(at.Sub(g.start) / g.duration) }

// run 构建面板并执行程序。网格从“第一个目标周期的集合回看窗口起点”到最后一个目标周期（设计 7.1）。
func (r *Runner) run(ctx context.Context, plan Plan, factors []domain.FactorDef, subjects []string,
	frames map[string]*storageio.Frame) (*exec.Result, computeGrid, error) {
	if r.cfg.MaxMemory > 0 {
		// 读取后按实际的序列数（标的 × series_tag）复核：读取前的预检只能按标的数估计。
		if err := withinBudget(factors, countSeries(subjects, frames), planPeriods(r.clock, plan), len(plan.CarryColumns), r.cfg.MaxMemory); err != nil {
			return nil, computeGrid{}, err
		}
	}
	inputs := make([]factorexpr.ProgramInput, 0, len(factors))
	for _, factor := range factors {
		inputs = append(inputs, factorexpr.ProgramInput{FactorID: factor.FactorID, Factor: factor.Compiled()})
	}
	program, err := factorexpr.NewProgram(inputs)
	if err != nil {
		return nil, computeGrid{}, err
	}
	duration, err := r.clock.Duration(plan.Set.Freq)
	if err != nil {
		return nil, computeGrid{}, err
	}
	last, err := planTarget(r.clock, plan)
	if err != nil {
		return nil, computeGrid{}, err
	}
	first := last
	if plan.Mode == ModeRecalc {
		first = plan.TargetStart.UTC()
	}
	window, err := r.clock.Window(first, plan.Set.Freq, max(1, program.Lookback))
	if err != nil {
		return nil, computeGrid{}, err
	}
	grid := computeGrid{start: window[0].UTC(), duration: duration}
	grid.first, grid.last = grid.index(first), grid.index(last)
	panel, err := buildPanel(grid, program.Columns, subjects, frames)
	if err != nil {
		return nil, computeGrid{}, err
	}
	result, err := exec.Run(ctx, program, panel, exec.Options{Start: grid.first, End: grid.last, Workers: r.cfg.ComputeWorkers})
	if err != nil {
		return nil, computeGrid{}, err
	}
	return result, grid, nil
}

// planPeriods 是计划的目标周期数：实时 1 期，补算为目标区间的周期数。
func planPeriods(clock periodclock.Clock, plan Plan) int {
	if plan.Mode != ModeRecalc {
		return 1
	}
	duration, err := clock.Duration(plan.Set.Freq)
	if err != nil || duration <= 0 {
		return 1
	}
	return max(1, int(plan.TargetEnd.Sub(plan.TargetStart)/duration))
}

// countSeries 数出窗口里的 (标的, series_tag) 序列数；没有行的标的按 1 条计。
func countSeries(subjects []string, frames map[string]*storageio.Frame) int {
	total := 0
	for _, subject := range subjects {
		frame := frames[subject]
		if frame == nil {
			total++
			continue
		}
		tags := map[any]struct{}{}
		for _, row := range frame.Rows {
			if len(row) > 1 {
				tags[row[1]] = struct{}{}
			}
		}
		total += max(1, len(tags))
	}
	return total
}

// precheckMemory 在读取之前按标的数（每个标的按一条序列估计）检查本次计算的估算峰值内存。
func (r *Runner) precheckMemory(plan Plan) error {
	if r.cfg.MaxMemory <= 0 {
		return nil
	}
	return withinBudget(plan.Factors, len(plan.Available), planPeriods(r.clock, plan), len(plan.CarryColumns), r.cfg.MaxMemory)
}

// failAll 返回本期全部因子对全部预期标的计算失败的结果（内存预检不通过时使用，没有读取）。
func (r *Runner) failAll(plan Plan, err error) Computation {
	computation := Computation{
		CrossPeriods: make(map[string]map[int64]periodOutcome),
		Results:      make(map[string]map[string]ItemResult, len(plan.Factors)),
		FactorStates: make(map[string]storageio.FactorState, len(plan.Factors)),
	}
	targets := []time.Time{}
	if target, targetErr := planTarget(r.clock, plan); targetErr == nil {
		targets = append(targets, target)
	}
	if plan.Mode == ModeRecalc {
		if periods, rangeErr := periodRange(r.clock, plan.TargetStart, plan.TargetEnd, plan.Set.Freq); rangeErr == nil {
			targets = periods
		}
	}
	expected := uniqueSorted(plan.Expected)
	for _, factor := range plan.Factors {
		computation.Results[factor.FactorID] = make(map[string]ItemResult)
		computation.FactorStates[factor.FactorID] = storageio.FactorState{
			FactorID: factor.FactorID, Status: "degraded", SourceHash: factor.SpecHash, DefinitionHash: domain.DefinitionHash(factor),
		}
		if factor.FactorType == domain.FactorTypeCrossSection {
			computation.crossPeriods(factor, GapReport{}, targets)
		}
		computation.fail(factor, plan, expected, err)
	}
	return computation
}

// buildPanel 把各标的的窗口放到网格上：每个 (标的, series_tag) 一条序列，按 (标的, 标签) 排序，结果与工作线程数无关；
// null 与网格上缺失的行是 NaN，非数值值报错（入成员时已校验列类型，正常不会出现）。
func buildPanel(grid computeGrid, columns []string, subjects []string, frames map[string]*storageio.Frame) (*exec.Panel, error) {
	length := grid.last + 1
	ordered := append([]string(nil), subjects...)
	sort.Strings(ordered)
	var keys []exec.SeriesKey
	for _, subject := range ordered {
		frame := frames[subject]
		if frame == nil {
			continue
		}
		if len(frame.Columns) < 2 || frame.Columns[0] != "data_time" || frame.Columns[1] != "series_tag" {
			return nil, fmt.Errorf("frame for %q does not start with data_time, series_tag", subject)
		}
		seen := map[string]bool{}
		var tags []string
		for rowNo, row := range frame.Rows {
			if len(row) != len(frame.Columns) {
				return nil, fmt.Errorf("frame row %d for %q has %d values for %d columns", rowNo, subject, len(row), len(frame.Columns))
			}
			tag, ok := row[1].(string)
			if !ok {
				return nil, fmt.Errorf("标的 %s 的 series_tag 类型为 %T", subject, row[1])
			}
			if !seen[tag] {
				seen[tag] = true
				tags = append(tags, tag)
			}
		}
		sort.Strings(tags)
		for _, tag := range tags {
			keys = append(keys, exec.SeriesKey{Subject: subject, Tag: tag})
		}
	}
	panel, err := exec.NewPanel(length, columns, keys)
	if err != nil {
		return nil, err
	}
	for _, subject := range ordered {
		frame := frames[subject]
		if frame == nil {
			continue
		}
		positions := make([]int, len(columns))
		for i, column := range columns {
			positions[i] = -1
			for j, name := range frame.Columns {
				if name == column {
					positions[i] = j
					break
				}
			}
			if positions[i] < 0 {
				return nil, fmt.Errorf("标的 %s 的窗口缺少输入列 %s", subject, column)
			}
		}
		for rowNo, row := range frame.Rows {
			at, err := parseDataTime(row[0])
			if err != nil {
				return nil, fmt.Errorf("标的 %s 第 %d 行 data_time：%w", subject, rowNo, err)
			}
			offset := at.Sub(grid.start)
			if offset < 0 || offset%grid.duration != 0 || int(offset/grid.duration) >= length {
				continue
			}
			t := int(offset / grid.duration)
			s, _ := panel.SeriesIndex(exec.SeriesKey{Subject: subject, Tag: row[1].(string)})
			for i, position := range positions {
				value, ok := numericOrNull(row[position])
				if !ok {
					return nil, fmt.Errorf("标的 %s 的列 %s 不是数值（%T）", subject, columns[i], row[position])
				}
				if row[position] == nil {
					value = math.NaN()
				}
				panel.Set(i, s, t, value)
			}
			panel.MarkPresent(s, t)
		}
	}
	return panel, nil
}

// outputColumns 返回因子各输出列在结果里的位置。
func outputColumns(result *exec.Result, factor domain.FactorDef) []int {
	positions := make([]int, 0, len(factor.Outputs))
	for _, output := range factor.Outputs {
		for i, column := range result.Columns {
			if column.FactorID == factor.FactorID && column.Name == output {
				positions = append(positions, i)
				break
			}
		}
	}
	return positions
}

// rowValues 取一个 (序列, 时刻) 的输出值；NaN 写成 null。
func rowValues(result *exec.Result, positions []int, s, t int) []any {
	values := make([]any, len(positions))
	for i, column := range positions {
		if value := result.Value(column, s, t); !math.IsNaN(value) {
			values[i] = value
		}
	}
	return values
}

// addTimeSeries 把时序因子的结果按标的转换成逐行结果：只在有源行的目标时刻产生行（设计 7.4）。
func (c *Computation) addTimeSeries(factor domain.FactorDef, result *exec.Result, grid computeGrid) {
	positions := outputColumns(result, factor)
	columns := append([]string{"data_time", "series_tag"}, factor.Outputs...)
	for s, key := range result.Series {
		item := c.Results[factor.FactorID][key.Subject]
		item.FactorID, item.Columns = factor.FactorID, columns
		for t := grid.first; t <= grid.last; t++ {
			if result.Present(s, t) {
				item.Rows = append(item.Rows, append([]any{grid.at(t), key.Tag}, rowValues(result, positions, s, t)...))
			}
		}
		c.Results[factor.FactorID][key.Subject] = item
	}
}

// crossPeriods 预置截面因子各目标周期的结果：严格截面因子在有标的有洞的周期记为 gap、不产出（其余周期照常），
// 不能用缺了标的的全集算排名与标准化；其余周期先记为 ok，计算失败时由 fail 改为 failed。
func (c *Computation) crossPeriods(factor domain.FactorDef, gaps GapReport, targets []time.Time) {
	for _, period := range targets {
		if !factor.AllowPartialUniverse && len(gaps.Subjects(factor.FactorID, period)) > 0 {
			setCrossPeriod(c, factor.FactorID, period, periodGap)
			continue
		}
		setCrossPeriod(c, factor.FactorID, period, periodOK)
	}
}

// addCross 把截面因子的结果合成一个以空串为键的结果，跳过记为 gap 的周期。
func (c *Computation) addCross(factor domain.FactorDef, result *exec.Result, grid computeGrid) {
	positions := outputColumns(result, factor)
	item := ItemResult{FactorID: factor.FactorID, Columns: append([]string{"data_time", "series_tag", "subject_id"}, factor.Outputs...)}
	for t := grid.first; t <= grid.last; t++ {
		if c.CrossPeriods[factor.FactorID][grid.at(t).Unix()] != periodOK {
			continue
		}
		for s, key := range result.Series {
			if result.Present(s, t) {
				item.Rows = append(item.Rows, append([]any{grid.at(t), key.Tag, key.Subject}, rowValues(result, positions, s, t)...))
			}
		}
	}
	c.Results[factor.FactorID][""] = item
}

// fail 记录一个因子本期（本分块）整体计算失败：时序因子对每个标的失败；截面因子未跳过的周期记为 failed，
// 补算时结果不带错误（各周期的结果由 CrossPeriods 表达）。
func (c *Computation) fail(factor domain.FactorDef, plan Plan, available []string, err error) {
	state := c.FactorStates[factor.FactorID]
	state.FailedSubjects = uniqueSorted(append(state.FailedSubjects, available...))
	state.Status = "degraded"
	c.FactorStates[factor.FactorID] = state
	if factor.FactorType != domain.FactorTypeCrossSection {
		for _, subject := range available {
			c.Results[factor.FactorID][subject] = ItemResult{FactorID: factor.FactorID, Err: err}
		}
		return
	}
	for period, outcome := range c.CrossPeriods[factor.FactorID] {
		if outcome == periodOK {
			c.CrossPeriods[factor.FactorID][period] = periodFailed
		}
	}
	item := ItemResult{FactorID: factor.FactorID, Err: err}
	if plan.Mode == ModeRecalc {
		item.Err = nil
	}
	c.Results[factor.FactorID][""] = item
}

func planTarget(clock periodclock.Clock, plan Plan) (time.Time, error) {
	duration, err := clock.Duration(plan.Set.Freq)
	if err != nil {
		return time.Time{}, err
	}
	target := plan.TargetEnd.Add(-duration)
	aligned, err := clock.Align(target, plan.Set.Freq)
	if err != nil {
		return time.Time{}, err
	}
	return aligned, nil
}

func periodRange(clock periodclock.Clock, start, end time.Time, freq string) ([]time.Time, error) {
	if !start.Before(end) {
		return nil, fmt.Errorf("factor period range must have start before end")
	}
	aligned, err := clock.Align(start, freq)
	if err != nil {
		return nil, err
	}
	if !aligned.Equal(start) {
		return nil, fmt.Errorf("factor period range start is not aligned")
	}
	duration, err := clock.Duration(freq)
	if err != nil {
		return nil, err
	}
	periods := make([]time.Time, 0)
	for at := start.UTC(); at.Before(end); at = at.Add(duration) {
		periods = append(periods, at)
	}
	if len(periods) == 0 {
		return nil, fmt.Errorf("factor period range contains no periods")
	}
	return periods, nil
}

func subjectDifference(expected, available []string) []string {
	availableSet := make(map[string]struct{}, len(available))
	for _, subject := range available {
		availableSet[subject] = struct{}{}
	}
	var missing []string
	for _, subject := range expected {
		if _, ok := availableSet[subject]; !ok {
			missing = append(missing, subject)
		}
	}
	return uniqueSorted(missing)
}

// setCrossPeriod 记录截面因子一个目标周期的结果（调用方负责并发保护）。
func setCrossPeriod(computation *Computation, factorID string, period time.Time, outcome periodOutcome) {
	if computation.CrossPeriods[factorID] == nil {
		computation.CrossPeriods[factorID] = make(map[int64]periodOutcome)
	}
	computation.CrossPeriods[factorID][period.UTC().Unix()] = outcome
}
