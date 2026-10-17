package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorlib"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorexpr/exec"
)

// compare 是因子表达式引擎的一致性核对工具（docs/计划/因子表达式引擎详细设计.md 12.2）：沿用补算的读取路径读一份窗口交给
// factorexpr 计算，与结果数据集里已经写入的值（基线，例如 Python 时代的历史结果）逐个 (因子输出, 标的, 周期, 标签) 比较。
// 只读：不组装、不写 Storage、不上报、不记账本、不同步目录、不访问 mgr，也不写引擎的状态目录。

// CompareRequest 是一次核对的输入。
type CompareRequest struct {
	// SetFile 是引擎目录快照 data/engine/catalog.json 的副本：提供因子集与启用成员的定义。
	SetFile string
	// Library 可选：YAML 因子库目录（每个 *.yaml 是 [{factor_id, name, spec}]），按 factor_id 覆盖快照里的定义，
	// 用于核对改过的定义；不填时使用快照里的定义。
	Library        string
	SetIDs         []string
	Start, End     time.Time
	Factors        []string
	Subjects       []string
	ComputeWorkers int
	ChunkPeriods   int
}

// 连续值超过容差时的裁决。
const (
	OutlierVerified     = "verified_reference"
	OutlierCancellation = "cancellation"
	OutlierFloatLimited = "float_limited" // 新实现的误差在 float64 固有误差之内（病态的抵消）
	OutlierUnexplained  = "unexplained"
)

// 新旧结果为“旧有值、新为 null”时的原因分类。
const (
	ReasonGap      = "gap"             // 窗口内有缺失的 K 线
	ReasonHead     = "head"            // 窗口起点早于该序列的第一根 K 线（新上市）
	ReasonComplete = "complete_window" // 窗口完整：分母为 0、常数窗口等退化情况，需人工抽查
)

// OutputStats 是一个输出列的核对结果。
type OutputStats struct {
	Set      string `json:"set"`
	FactorID string `json:"factor_id"`
	Output   string `json:"output"`
	Discrete bool   `json:"discrete"` // 排名类输出：差异按离散跳变统计
	Variance bool   `json:"variance"` // 方差类输出：连续值容差放宽到 1e-7
	Compared int64  `json:"compared"`
	BothNull int64  `json:"both_null"`
	// BothValue 中按相对误差分档：<=1e-15、<=1e-12、<=1e-9、<=1e-7、>1e-7。
	BothValue     int64            `json:"both_value"`
	ErrorBuckets  map[string]int64 `json:"error_buckets"`
	MaxRelError   float64          `json:"max_rel_error"`
	DiscreteFlips int64            `json:"discrete_flips"`
	// TieFlips 是能证明的并列翻转：新实现窗口里当期值与另一个值精确相等，pandas 的滑动求和让它们差在末位、打破了并列。
	TieFlips int64 `json:"tie_flips"`
	// VerifiedFlips 是没有精确并列、但 256 位精度重算的排名与新实现一致的跳变（近似并列，旧实现的末位误差改变了次序）。
	VerifiedFlips int64 `json:"verified_flips"`
	// FloatLimitedFlips 是发生在 float64 分辨不出的近似并列上的跳变（高精度下两个值的差小于朴素 float64 实现的误差）。
	FloatLimitedFlips int64 `json:"float_limited_flips"`
	UnexplainedFlips  int64 `json:"unexplained_flips"`
	// ContinuousOutliers 是超过容差的连续值差异的裁决：verified_reference（256 位精度重算，新实现更准）、
	// cancellation（真值接近 0 的抵消，绝对差相对该序列量级可忽略）、unexplained。
	ContinuousOutliers map[string]int64 `json:"continuous_outliers"`
	OldValueNewNull    map[string]int64 `json:"old_value_new_null"`
	OldNullNewValue    int64            `json:"old_null_new_value"`
	MissingBaseline    int64            `json:"missing_baseline"` // 新引擎有值、基线没有对应行（例如当时预热中跳过），只做参考
	Samples            []string         `json:"samples,omitempty"`
}

// CompareReport 是全部集合的核对结果。
type CompareReport struct {
	Start   time.Time      `json:"start"`
	End     time.Time      `json:"end"`
	Outputs []*OutputStats `json:"outputs"`
	Elapsed string         `json:"elapsed"`
}

// Failures 按设计 12.2 的验收标准列出不通过的项；为空表示通过。“旧有值、新为 null”里窗口完整的部分需要人工抽查，
// 不在这里自动判失败。
func (r CompareReport) Failures() []string {
	var failures []string
	for _, stats := range r.Outputs {
		label := stats.Set + "/" + stats.Output
		if stats.OldNullNewValue > 0 {
			failures = append(failures, fmt.Sprintf("%s：旧为 null、新有值 %d 个", label, stats.OldNullNewValue))
		}
		if n := stats.ContinuousOutliers[OutlierUnexplained]; n > 0 {
			failures = append(failures, fmt.Sprintf("%s：%d 个连续值差异超过容差且无法解释", label, n))
		}
		if stats.UnexplainedFlips > 0 {
			failures = append(failures, fmt.Sprintf("%s：%d 个离散跳变不是并列翻转", label, stats.UnexplainedFlips))
		}
	}
	return failures
}

// LoadLibrary 读取 YAML 因子库目录并编译每个因子。
func LoadLibrary(dir string) (map[string]*factorexpr.Factor, error) {
	entries, err := factorlib.LoadDir(dir)
	if err != nil {
		return nil, err
	}
	library := make(map[string]*factorexpr.Factor, len(entries))
	for _, entry := range entries {
		factor, err := factorexpr.Compile(entry.Spec)
		if err != nil {
			return nil, fmt.Errorf("编译因子 %s：%w", entry.FactorID, err)
		}
		library[entry.FactorID] = factor
	}
	return library, nil
}

// Compare 逐个集合、逐个补算分块核对。
func Compare(ctx context.Context, cfg *Config, req CompareRequest) (CompareReport, error) {
	started := time.Now()
	report := CompareReport{Start: req.Start.UTC(), End: req.End.UTC()}
	if cfg == nil {
		return report, errors.New("factor engine config is required")
	}
	if req.SetFile == "" {
		return report, errors.New("--set-file 是必填项")
	}
	if !req.Start.Before(req.End) {
		return report, errors.New("--start 必须早于 --end")
	}
	var library map[string]*factorexpr.Factor
	if req.Library != "" {
		var err error
		if library, err = LoadLibrary(req.Library); err != nil {
			return report, err
		}
	}
	cache, err := NewCatalogCache(req.SetFile)
	if err != nil {
		return report, err
	}
	if found, err := cache.Load(); err != nil {
		return report, err
	} else if !found {
		return report, fmt.Errorf("目录快照 %s 不存在", req.SetFile)
	}
	gateway, err := cfg.GatewayClient.OpenExternal(cfg.sourcePath)
	if err != nil {
		return report, err
	}
	defer gateway.Close()
	storage, err := newStorageClient(gateway, cfg.Storage)
	if err != nil {
		return report, err
	}
	clock := periodclock.Continuous{}
	runner := pipeline.NewRunner(storage, clock, pipeline.Config{
		ReadBatchSubjects: cfg.Pipeline.ReadBatchSubjects, ReadWorkers: cfg.Pipeline.ReadWorkers, ReadTimeout: cfg.Pipeline.ReadTimeout,
		BulkReadWorkers: cfg.Pipeline.BulkReadWorkers, ReadRetryBackoff: cfg.Pipeline.ReadRetryBackoff,
		ReadBreakerFailures: cfg.Pipeline.ReadBreakerFailures, ComputeWorkers: req.ComputeWorkers,
	})
	chunk := req.ChunkPeriods
	if chunk <= 0 {
		chunk = cfg.Recalc.ChunkPeriods
	}
	for _, setID := range req.SetIDs {
		set, err := cache.Set(strings.TrimSpace(setID))
		if err != nil {
			return report, err
		}
		factors, err := selectSetFactors(set, req.Factors)
		if err != nil {
			return report, err
		}
		definitions := make(map[string]*factorexpr.Factor, len(factors))
		for _, factor := range factors {
			if override, ok := library[factor.FactorID]; ok {
				definitions[factor.FactorID] = override
			} else if factor.Compiled() != nil {
				definitions[factor.FactorID] = factor.Compiled()
			}
		}
		target, err := newCompareTarget(set.Set, factors, definitions, req.ComputeWorkers)
		if err != nil {
			return report, err
		}
		subjects, err := setSubjects(ctx, storage, set, req.Subjects)
		if err != nil {
			return report, err
		}
		columns, err := storage.DatasetColumns(ctx, set.Set.SpaceID, set.Set.SourceDatasetID)
		if err != nil {
			return report, err
		}
		duration, err := clock.Duration(set.Set.Freq)
		if err != nil {
			return report, err
		}
		start, err := clock.Align(req.Start, set.Set.Freq)
		if err != nil {
			return report, err
		}
		for chunkStart := start; chunkStart.Before(req.End); {
			chunkEnd := chunkStart.Add(time.Duration(chunk) * duration)
			if chunkEnd.After(req.End) {
				chunkEnd = req.End
			}
			plan := pipeline.Plan{
				Mode: pipeline.ModeRecalc, Set: set.Set, Factors: factors, TargetStart: chunkStart, TargetEnd: chunkEnd,
				Expected: subjects, Available: subjects, CarryColumns: columns,
			}
			loaded, err := runner.Load(ctx, plan)
			if err != nil {
				return report, fmt.Errorf("读取 %s [%s, %s)：%w", setID, chunkStart.Format(time.RFC3339), chunkEnd.Format(time.RFC3339), err)
			}
			baseline, err := readBaseline(ctx, storage, cfg, set.Set, target.order, subjects, chunkStart, chunkEnd)
			if err != nil {
				return report, fmt.Errorf("读取 %s 的已有结果：%w", setID, err)
			}
			if err := target.compareChunk(ctx, clock, plan, loaded, baseline); err != nil {
				return report, err
			}
			chunkStart = chunkEnd
		}
		report.Outputs = append(report.Outputs, target.outputStats()...)
	}
	report.Elapsed = time.Since(started).Round(time.Second).String()
	return report, nil
}

// readBaseline 读取结果数据集在 [start, end) 的已有输出，按标的整理成与计算结果相同的形状（列为 data_time、series_tag 与输出列）。
func readBaseline(ctx context.Context, storage storageio.Store, cfg *Config, set domain.FactorSet, outputs, subjects []string,
	start, end time.Time) (pipeline.Computation, error) {
	baseline := pipeline.Computation{Results: map[string]map[string]pipeline.ItemResult{"": {}}}
	batch := max(1, cfg.Pipeline.ReadBatchSubjects)
	for lo := 0; lo < len(subjects); lo += batch {
		hi := min(len(subjects), lo+batch)
		frames, err := storage.ReadWindow(ctx, storageio.ReadRequest{
			SpaceID: set.SpaceID, DatasetID: set.ResultDatasetID, Freq: set.Freq, Subjects: subjects[lo:hi],
			Start: start, End: end, Columns: outputs, PageTimeout: cfg.Pipeline.ReadTimeout,
		})
		if err != nil {
			return pipeline.Computation{}, err
		}
		for subject, frame := range frames {
			if frame != nil {
				baseline.Results[""][subject] = pipeline.ItemResult{Columns: frame.Columns, Rows: frame.Rows}
			}
		}
	}
	return baseline, nil
}

// compareTarget 是一个集合的核对状态。
type compareTarget struct {
	set     domain.FactorSet
	program *factorexpr.Program
	roots   map[string]*factorexpr.Node // 输出列 → 表达式根节点
	owners  map[string]string           // 输出列 → factor_id
	stats   map[string]*OutputStats
	order   []string
	workers int
	// aux 计算 ts_rank、ts_std、ts_var 根节点的参数序列，用于裁决差异（并列检查、高精度重算）；与主程序的节点按规范键去重，不重复计算。
	aux         *factorexpr.Program
	auxLookback int
}

// tolerance 是连续值的容差：方差类放宽到 1e-7（pandas 在线算法自身的误差，设计 12.1）。
func (s *OutputStats) tolerance() float64 {
	if s.Variance {
		return 1e-7
	}
	return 1e-9
}

var varianceOps = map[string]bool{"ts_std": true, "ts_var": true, "ts_zscore": true, "ts_corr": true, "ts_cov": true,
	"ts_skew": true, "ts_kurt": true, "cs_zscore": true}

func newCompareTarget(set domain.FactorSet, factors []domain.FactorDef, library map[string]*factorexpr.Factor, workers int) (*compareTarget, error) {
	target := &compareTarget{set: set, roots: map[string]*factorexpr.Node{}, owners: map[string]string{},
		stats: map[string]*OutputStats{}, workers: workers}
	var inputs []factorexpr.ProgramInput
	for _, def := range factors {
		factor, ok := library[def.FactorID]
		if !ok {
			return nil, fmt.Errorf("因子库里没有 %s", def.FactorID)
		}
		if got, want := strings.Join(factor.OutputNames(), ","), strings.Join(def.Outputs, ","); got != want {
			return nil, fmt.Errorf("因子 %s 的输出列不一致：新 %s，旧 %s", def.FactorID, got, want)
		}
		if factor.Lookback != def.LookbackPeriods {
			return nil, fmt.Errorf("因子 %s 的回看不一致：新 %d，旧 %d", def.FactorID, factor.Lookback, def.LookbackPeriods)
		}
		inputs = append(inputs, factorexpr.ProgramInput{FactorID: def.FactorID, Factor: factor})
		for _, output := range factor.Outputs {
			target.roots[output.Name] = output.Root
			target.owners[output.Name] = def.FactorID
			target.order = append(target.order, output.Name)
			target.stats[output.Name] = &OutputStats{
				Set: set.SetID, FactorID: def.FactorID, Output: output.Name,
				Discrete: output.Root.Op == "ts_rank" || output.Root.Op == "cs_rank", Variance: varianceOps[output.Root.Op],
				ErrorBuckets: map[string]int64{}, OldValueNewNull: map[string]int64{}, ContinuousOutliers: map[string]int64{},
			}
		}
	}
	program, err := factorexpr.NewProgram(inputs)
	if err != nil {
		return nil, err
	}
	target.program = program
	var auxInputs []factorexpr.ProgramInput
	for _, name := range target.order {
		root := target.roots[name]
		if root.Op != "ts_rank" && root.Op != "ts_std" && root.Op != "ts_var" {
			continue
		}
		arg := root.Args[0]
		auxInputs = append(auxInputs, factorexpr.ProgramInput{FactorID: "aux:" + name, Factor: &factorexpr.Factor{
			Outputs: []factorexpr.Output{{Name: auxName(name), Root: arg}}, Lookback: max(1, arg.Lookback)}})
	}
	if len(auxInputs) > 0 {
		if target.aux, err = factorexpr.NewProgram(auxInputs); err != nil {
			return nil, err
		}
		target.auxLookback = target.aux.Lookback
	}
	return target, nil
}

func auxName(output string) string { return "aux__" + output }

func (c *compareTarget) outputStats() []*OutputStats {
	out := make([]*OutputStats, 0, len(c.order))
	for _, name := range c.order {
		out = append(out, c.stats[name])
	}
	return out
}

// comparePanel 是由读到的窗口构建的面板及其时间网格。
type comparePanel struct {
	panel     *exec.Panel
	gridStart time.Time
	duration  time.Duration
	first     []int // 每条序列第一根有源行的网格下标；没有为 -1
}

func (p *comparePanel) index(at time.Time) int { return int(at.Sub(p.gridStart) / p.duration) }

func buildComparePanel(clock periodclock.Clock, plan pipeline.Plan, lookback int, columns []string, frames map[string]*storageio.Frame) (*comparePanel, error) {
	duration, err := clock.Duration(plan.Set.Freq)
	if err != nil {
		return nil, err
	}
	window, err := clock.Window(plan.TargetStart, plan.Set.Freq, lookback)
	if err != nil {
		return nil, err
	}
	gridStart := window[0]
	length := int(plan.TargetEnd.Sub(gridStart) / duration)
	var keys []exec.SeriesKey
	seen := map[exec.SeriesKey]bool{}
	subjects := make([]string, 0, len(frames))
	for subject := range frames {
		subjects = append(subjects, subject)
	}
	sort.Strings(subjects)
	for _, subject := range subjects {
		for _, row := range frames[subject].Rows {
			tag, _ := row[1].(string)
			key := exec.SeriesKey{Subject: subject, Tag: tag}
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	panel, err := exec.NewPanel(length, columns, keys)
	if err != nil {
		return nil, err
	}
	result := &comparePanel{panel: panel, gridStart: gridStart, duration: duration, first: make([]int, len(keys))}
	for i := range result.first {
		result.first[i] = -1
	}
	for _, subject := range subjects {
		frame := frames[subject]
		positions := make([]int, len(columns))
		for i, column := range columns {
			positions[i] = -1
			for j, name := range frame.Columns {
				if name == column {
					positions[i] = j
				}
			}
			if positions[i] < 0 {
				return nil, fmt.Errorf("标的 %s 的窗口缺少列 %s", subject, column)
			}
		}
		for _, row := range frame.Rows {
			at, ok := row[0].(time.Time)
			if !ok {
				return nil, fmt.Errorf("标的 %s 的 data_time 类型为 %T", subject, row[0])
			}
			t := result.index(at)
			if t < 0 || t >= length {
				continue
			}
			tag, _ := row[1].(string)
			s, _ := panel.SeriesIndex(exec.SeriesKey{Subject: subject, Tag: tag})
			for i, position := range positions {
				panel.Set(i, s, t, numeric(row[position]))
			}
			panel.MarkPresent(s, t)
			if result.first[s] < 0 || t < result.first[s] {
				result.first[s] = t
			}
		}
	}
	return result, nil
}

func numeric(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case int32:
		return float64(v)
	}
	return math.NaN()
}

func (c *compareTarget) compareChunk(ctx context.Context, clock periodclock.Clock, plan pipeline.Plan, loaded pipeline.LoadResult, computation pipeline.Computation) error {
	built, err := buildComparePanel(clock, plan, c.program.Lookback, c.program.Columns, loaded.Frames)
	if err != nil {
		return err
	}
	panel := built.panel
	start := c.program.Lookback - 1
	end := panel.Length() - 1
	result, err := exec.Run(ctx, c.program, panel, exec.Options{Start: start, End: end, Workers: c.workers})
	if err != nil {
		return err
	}
	columnIndex := map[string]int{}
	for i, column := range result.Columns {
		columnIndex[column.Name] = i
	}
	chunk := &compareChunkState{built: built, result: result, columns: columnIndex, scales: map[[2]int]float64{}}
	if c.aux != nil {
		if chunk.aux, err = exec.Run(ctx, c.aux, panel, exec.Options{Start: c.auxLookback - 1, End: end, Workers: c.workers}); err != nil {
			return err
		}
		chunk.auxColumns = map[string]int{}
		for i, column := range chunk.aux.Columns {
			chunk.auxColumns[column.Name] = i
		}
	}
	seen := map[string]map[[2]int]bool{}
	for name := range c.stats {
		seen[name] = map[[2]int]bool{}
	}
	for _, bySubject := range computation.Results {
		for subject, item := range bySubject {
			subjectColumn := -1
			if len(item.Columns) > 2 && item.Columns[2] == "subject_id" {
				subjectColumn = 2
			}
			for _, row := range item.Rows {
				at, err := parseCompareTime(row[0])
				if err != nil {
					return err
				}
				tag, _ := row[1].(string)
				rowSubject := subject
				if subjectColumn >= 0 {
					rowSubject, _ = row[subjectColumn].(string)
				}
				s, ok := panel.SeriesIndex(exec.SeriesKey{Subject: rowSubject, Tag: tag})
				t := built.index(at)
				if !ok || t < start || t > end {
					continue
				}
				for k := 0; k < len(item.Columns); k++ {
					name := item.Columns[k]
					stats, isOutput := c.stats[name]
					if !isOutput {
						continue
					}
					seen[name][[2]int{s, t}] = true
					c.compareValue(stats, chunk, s, t, numeric(row[k]), result.Value(columnIndex[name], s, t))
				}
			}
		}
	}
	for name, stats := range c.stats {
		column := columnIndex[name]
		for s := range result.Series {
			for t := start; t <= end; t++ {
				if result.Present(s, t) && !seen[name][[2]int{s, t}] && !math.IsNaN(result.Value(column, s, t)) {
					stats.MissingBaseline++
				}
			}
		}
	}
	return nil
}

// compareChunkState 是一个分块的计算结果与裁决用的辅助数据。
type compareChunkState struct {
	built      *comparePanel
	result     *exec.Result
	columns    map[string]int
	aux        *exec.Result
	auxColumns map[string]int
	scales     map[[2]int]float64 // (输出列下标, 序列) → 本分块内新结果的最大绝对值
}

func (c *compareTarget) compareValue(stats *OutputStats, chunk *compareChunkState, s, t int, old, current float64) {
	built := chunk.built
	stats.Compared++
	switch {
	case math.IsNaN(old) && math.IsNaN(current):
		stats.BothNull++
	case math.IsNaN(current):
		reason := c.nullReason(built, s, t, c.roots[stats.Output].Lookback)
		stats.OldValueNewNull[reason]++
		if reason == ReasonComplete {
			c.sample(stats, fmt.Sprintf("%s t=%s 旧 %v 新 null（窗口完整）", built.panel.Series()[s].Subject, built.time(t), old))
		}
	case math.IsNaN(old):
		stats.OldNullNewValue++
		c.sample(stats, fmt.Sprintf("%s t=%s 旧 null 新 %v", built.panel.Series()[s].Subject, built.time(t), current))
	default:
		stats.BothValue++
		root := c.roots[stats.Output]
		if stats.Discrete {
			if math.Abs(current-old) > 1e-9 {
				stats.DiscreteFlips++
				if root.Op == "ts_rank" && c.hasTie(chunk, stats.Output, s, t, root.Ints[0]) {
					stats.TieFlips++
					return
				}
				if reference, ok := refereeValueAt(built.panel, c.program.Columns, s, root, t); ok && math.Abs(reference-current) <= 1e-12 {
					stats.VerifiedFlips++
					return
				}
				if nearTieWithinFloatError(built.panel, c.program.Columns, s, root, t) {
					stats.FloatLimitedFlips++
					return
				}
				stats.UnexplainedFlips++
				reference, refereeOK := refereeValueAt(built.panel, c.program.Columns, s, root, t)
				c.sample(stats, fmt.Sprintf("%s t=%s 排名 旧 %v 新 %v 高精度 %v（ok=%t，窗口内没有并列）", built.panel.Series()[s].Subject, built.time(t), old, current, reference, refereeOK))
			}
			return
		}
		relative := math.Abs(current-old) / math.Max(math.Abs(old), 1e-12)
		stats.MaxRelError = math.Max(stats.MaxRelError, relative)
		switch {
		case relative <= 1e-15:
			stats.ErrorBuckets["<=1e-15"]++
		case relative <= 1e-12:
			stats.ErrorBuckets["<=1e-12"]++
		case relative <= 1e-9:
			stats.ErrorBuckets["<=1e-9"]++
		case relative <= 1e-7:
			stats.ErrorBuckets["<=1e-7"]++
		default:
			stats.ErrorBuckets[">1e-7"]++
		}
		if relative <= stats.tolerance() {
			return
		}
		verdict := c.adjudicate(stats, chunk, s, t, old, current)
		stats.ContinuousOutliers[verdict]++
		if verdict == OutlierUnexplained {
			reference, refereeOK := refereeValueAt(built.panel, c.program.Columns, s, c.roots[stats.Output], t)
			c.sample(stats, fmt.Sprintf("%s t=%s 旧 %v 新 %v 高精度 %v（ok=%t，无法解释）", built.panel.Series()[s].Subject, built.time(t), old, current, reference, refereeOK))
		}
	}
}

// hasTie 报告 ts_rank 的参数在以 t 结尾、长度为 n 的窗口里，当期值是否与另一个值精确相等。
func (c *compareTarget) hasTie(chunk *compareChunkState, output string, s, t, n int) bool {
	column, ok := chunk.auxColumns[auxName(output)]
	if !ok || t-n+1 < chunk.aux.Start {
		return false
	}
	current := chunk.aux.Value(column, s, t)
	for i := t - n + 1; i < t; i++ {
		if chunk.aux.Value(column, s, i) == current {
			return true
		}
	}
	return false
}

// adjudicate 裁决超过容差的连续值差异：沿表达式树用 256 位精度重算（compare_referee.go），新实现与参考一致且比旧值更接近时判为
// verified_reference；新旧都接近 0（相对本分块该序列新结果的量级不到 1e-6）判为 cancellation；其余为 unexplained。
func (c *compareTarget) adjudicate(stats *OutputStats, chunk *compareChunkState, s, t int, old, current float64) string {
	root := c.roots[stats.Output]
	if reference, ok := refereeValueAt(chunk.built.panel, c.program.Columns, s, root, t); ok {
		errNew, errOld := math.Abs(current-reference), math.Abs(old-reference)
		if errNew <= 1e-12*math.Max(math.Abs(reference), 1e-300) && errNew < errOld {
			return OutlierVerified
		}
	}
	if withinFloatError(chunk.built.panel, c.program.Columns, s, root, t, current) {
		return OutlierFloatLimited
	}
	key := [2]int{chunk.columns[stats.Output], s}
	scale, ok := chunk.scales[key]
	if !ok {
		for i := chunk.result.Start; i <= chunk.result.End; i++ {
			if v := chunk.result.Value(key[0], s, i); !math.IsNaN(v) {
				scale = math.Max(scale, math.Abs(v))
			}
		}
		chunk.scales[key] = scale
	}
	if scale > 0 && math.Abs(current) <= 1e-6*scale && math.Abs(old) <= 1e-6*scale {
		return OutlierCancellation
	}
	return OutlierUnexplained
}

// nullReason 判断新结果为 null 的原因：窗口内缺 K 线，还是窗口起点早于第一根 K 线，还是窗口完整（退化情况）。
func (c *compareTarget) nullReason(built *comparePanel, s, t, lookback int) string {
	first := built.first[s]
	from := t - lookback + 1
	if first < 0 || first > from {
		return ReasonHead
	}
	for i := from; i <= t; i++ {
		if !built.present(s, i) {
			return ReasonGap
		}
	}
	return ReasonComplete
}

func (p *comparePanel) time(t int) string {
	return p.gridStart.Add(time.Duration(t) * p.duration).UTC().Format(time.RFC3339)
}

func (p *comparePanel) present(s, t int) bool { return p.panel.Present(s, t) }

func (c *compareTarget) sample(stats *OutputStats, text string) {
	if len(stats.Samples) < 10 {
		stats.Samples = append(stats.Samples, text)
	}
}

func parseCompareTime(value any) (time.Time, error) {
	switch v := value.(type) {
	case time.Time:
		return v.UTC(), nil
	case string:
		at, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return time.Time{}, fmt.Errorf("解析基线结果的 data_time %q：%w", v, err)
		}
		return at.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("基线结果的 data_time 类型为 %T", value)
}
