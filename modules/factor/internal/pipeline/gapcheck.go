package pipeline

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

// 回看窗口的 K 线空洞检查：因子按「最近 N 根」取窗口，窗口中间缺了几根时计算照常给出一个看似正常的数，所以必须在计算前
// 检查每个因子、每个标的、每个序列、每个目标周期的窗口是否连续。设计见 docs/计划/因子窗口空洞防护详细设计.md。

// GapKey 标识一个有空洞的 (因子, 标的, 目标周期)。
type GapKey struct {
	FactorID string
	Subject  string
	Period   time.Time
}

type gapDetail struct {
	Missing []time.Time // 缺失的时间点（已去掉头部新上市的部分），最多记录 maxGapMissing 个
	Tags    []string    // 有洞的序列
}

const maxGapMissing = 20

// GapReport 记录有空洞的 (因子, 标的, 目标周期)；没有空洞的组合不出现。零值可用，表示没有空洞。
type GapReport struct {
	entries map[GapKey]*gapDetail
}

func (g *GapReport) add(key GapKey, missing []time.Time, tag string) {
	if g.entries == nil {
		g.entries = make(map[GapKey]*gapDetail)
	}
	key.Period = key.Period.UTC()
	detail := g.entries[key]
	if detail == nil {
		detail = &gapDetail{}
		g.entries[key] = detail
	}
	detail.Tags = appendUniqueString(detail.Tags, tag)
	for _, at := range missing {
		if len(detail.Missing) >= maxGapMissing {
			break
		}
		detail.Missing = appendUniqueTime(detail.Missing, at)
	}
}

// Len 是有空洞的 (因子, 标的, 周期) 数量。
func (g GapReport) Len() int { return len(g.entries) }

// Invalid 报告该因子在该标的的该目标周期上的回看窗口是否有空洞。
func (g GapReport) Invalid(factorID, subject string, period time.Time) bool {
	if len(g.entries) == 0 {
		return false
	}
	_, ok := g.entries[GapKey{FactorID: factorID, Subject: subject, Period: period.UTC()}]
	return ok
}

// Subjects 返回该因子在该目标周期上有空洞的标的（已排序）。
func (g GapReport) Subjects(factorID string, period time.Time) []string {
	var out []string
	period = period.UTC()
	for key := range g.entries {
		if key.FactorID == factorID && key.Period.Equal(period) {
			out = append(out, key.Subject)
		}
	}
	sort.Strings(out)
	return out
}

// FactorSubjects 返回该因子在任一目标周期上有空洞的标的（已排序、去重）。
func (g GapReport) FactorSubjects(factorID string) []string {
	set := make(map[string]struct{})
	for key := range g.entries {
		if key.FactorID == factorID {
			set[key.Subject] = struct{}{}
		}
	}
	return sortedKeys(set)
}

// AffectedSubjects 返回任一因子、任一周期有空洞的标的（已排序、去重）。
func (g GapReport) AffectedSubjects() []string {
	set := make(map[string]struct{})
	for key := range g.entries {
		set[key.Subject] = struct{}{}
	}
	return sortedKeys(set)
}

// GapEntry 是写入账本的一条空洞记录：一个标的、一个周期、一个因子。
type GapEntry struct {
	SetID          string
	Subject        string
	Period         time.Time
	FactorID       string
	DefinitionHash string
	Missing        []time.Time
	Tags           []string
	// TriggerEventID 是触发本期的上游事件，截面因子的恢复据此读取冻结全集；人工补算为空。
	TriggerEventID string
}

// GapSink 接收每次因空洞跳过的记录（引擎的永久账本）。写入失败不影响周期推进，调用方记日志与指标。
type GapSink interface {
	RecordGaps(ctx context.Context, entries []GapEntry) error
}

// entries 把报告转换为账本记录（按标的、周期、因子排序，保证可重复）。
// 截面因子只在实时周期记录：人工补算没有触发事件，取不到冻结全集，只记日志。
func (g GapReport) entries4Ledger(plan Plan) []GapEntry {
	hashes := make(map[string]domain.FactorDef, len(plan.Factors))
	for _, factor := range plan.Factors {
		hashes[factor.FactorID] = factor
	}
	out := make([]GapEntry, 0, len(g.entries))
	for key, detail := range g.entries {
		factor, ok := hashes[key.FactorID]
		if !ok {
			continue
		}
		if factor.FactorType == domain.FactorTypeCrossSection && plan.Mode != ModeLive {
			continue
		}
		entry := GapEntry{
			SetID: plan.Set.SetID, Subject: key.Subject, Period: key.Period, FactorID: key.FactorID,
			DefinitionHash: domain.DefinitionHash(factor), Missing: append([]time.Time(nil), detail.Missing...),
			Tags: append([]string(nil), detail.Tags...),
		}
		if plan.Mode == ModeLive {
			entry.TriggerEventID = plan.TriggerEventID
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		if !a.Period.Equal(b.Period) {
			return a.Period.Before(b.Period)
		}
		return a.FactorID < b.FactorID
	})
	return out
}

// earliestReader 是存储客户端可选的能力：读取标的最早的一根 K 线，用来区分新上市（窗口头部缺失）与真正的空洞。
type earliestReader interface {
	EarliestPeriod(ctx context.Context, spaceID, datasetID, freq string, subjects, columns []string) (time.Time, bool, error)
}

type firstBarEntry struct {
	at      time.Time
	found   bool
	expires time.Time // 零值表示永久（已找到的首根 K 线不会变）
}

const firstBarMissTTL = 5 * time.Minute

type firstBarCache struct {
	mu      sync.Mutex
	entries map[string]firstBarEntry
}

// firstBar 返回标的在源数据集里最早的一根 K 线时间。known 为 false 表示读取失败或存储不支持：调用方按「不是新上市」处理
// （把头部缺失当作空洞），下期重试，错误不缓存。
func (r *Runner) firstBar(ctx context.Context, plan Plan, subject string) (at time.Time, found, known bool) {
	reader, ok := r.store.(earliestReader)
	if !ok {
		return time.Time{}, false, false
	}
	key := plan.Set.SpaceID + "|" + plan.Set.SourceDatasetID + "|" + plan.Set.Freq + "|" + subject
	now := time.Now()
	r.firstBars.mu.Lock()
	if entry, hit := r.firstBars.entries[key]; hit && (entry.expires.IsZero() || now.Before(entry.expires)) {
		r.firstBars.mu.Unlock()
		return entry.at, entry.found, true
	}
	r.firstBars.mu.Unlock()
	column := "close"
	if len(plan.CarryColumns) > 0 {
		column = plan.CarryColumns[0]
	} else if len(plan.Factors) > 0 && len(plan.Factors[0].InputColumns) > 0 {
		column = plan.Factors[0].InputColumns[0]
	}
	earliest, exists, err := reader.EarliestPeriod(ctx, plan.Set.SpaceID, plan.Set.SourceDatasetID, plan.Set.Freq, []string{subject}, []string{column})
	if err != nil {
		log.Warnf("factor_gap_first_bar_failed set_id=%s subject=%s error=%v", plan.Set.SetID, subject, err)
		return time.Time{}, false, false
	}
	entry := firstBarEntry{at: earliest.UTC(), found: exists}
	if !exists {
		entry.expires = now.Add(firstBarMissTTL)
	}
	r.firstBars.mu.Lock()
	if r.firstBars.entries == nil {
		r.firstBars.entries = make(map[string]firstBarEntry)
	}
	r.firstBars.entries[key] = entry
	r.firstBars.mu.Unlock()
	return entry.at, entry.found, true
}

// gapTargets 是本次计划要检查的目标周期。
func (r *Runner) gapTargets(plan Plan) ([]time.Time, error) {
	if plan.Mode == ModeRecalc {
		return periodRange(r.clock, plan.TargetStart, plan.TargetEnd, plan.Set.Freq)
	}
	target, err := planTarget(r.clock, plan)
	if err != nil {
		return nil, err
	}
	return []time.Time{target}, nil
}

func maxLookback(factors []domain.FactorDef) int {
	max := 1
	for _, factor := range factors {
		if factor.LookbackPeriods > max {
			max = factor.LookbackPeriods
		}
	}
	return max
}

// seriesGaps 是一个标的在回看网格上每个序列的缺失情况。
type seriesGaps struct {
	grid    []time.Time // 网格：最后一个目标往前 len(targets)+maxL-1 个周期，升序
	tags    []string
	missing map[string][]bool // 序列 -> 网格各点是否缺失（已去掉头部新上市）
}

// subjectGrid 计算一个标的的缺失网格。frame 为空或不是合法帧时返回 nil（交给现有路径处理）。
func (r *Runner) subjectGrid(ctx context.Context, plan Plan, subject string, frame *storageio.Frame, targets []time.Time, maxL int) *seriesGaps {
	if frame == nil || len(targets) == 0 {
		return nil
	}
	positions, err := framePositions(frame, nil)
	if err != nil {
		return nil
	}
	grid, err := r.clock.Window(targets[len(targets)-1], plan.Set.Freq, len(targets)+maxL-1)
	if err != nil || len(grid) == 0 {
		return nil
	}
	present := make(map[string]map[int64]struct{})
	for _, values := range frame.Rows {
		if len(values) <= positions["series_tag"] || len(values) <= positions["data_time"] {
			continue
		}
		at, err := parseDataTime(values[positions["data_time"]])
		if err != nil {
			continue
		}
		tag, _ := values[positions["series_tag"]].(string)
		if present[tag] == nil {
			present[tag] = make(map[int64]struct{})
		}
		present[tag][at.UTC().Unix()] = struct{}{}
	}
	result := &seriesGaps{grid: grid, missing: make(map[string][]bool)}
	if len(present) == 0 {
		// 空帧：读取成功但窗口内没有任何行。存在过（有首根 K 线记录且早于窗口结束）说明窗口整段缺失；从未有过数据则沿用现有路径。
		first, found, _ := r.firstBar(ctx, plan, subject)
		if !found || !first.Before(grid[len(grid)-1].Add(time.Nanosecond)) {
			return nil
		}
		present[""] = map[int64]struct{}{}
	}
	for tag := range present {
		result.tags = append(result.tags, tag)
	}
	sort.Strings(result.tags)
	var (
		firstChecked, firstFound, firstKnown bool
		firstAt                              time.Time
	)
	for _, tag := range result.tags {
		flags := make([]bool, len(grid))
		firstPresent := len(grid)
		for j, at := range grid {
			if _, ok := present[tag][at.Unix()]; ok {
				if firstPresent == len(grid) {
					firstPresent = j
				}
				continue
			}
			flags[j] = true
		}
		// 头部缺失：只有该标的在这些时间点之前确实存在过数据才算空洞，否则是新上市（上市前本来就没有数据）。
		if firstPresent > 0 {
			if !firstChecked {
				firstAt, firstFound, firstKnown = r.firstBar(ctx, plan, subject)
				firstChecked = true
			}
			for j := 0; j < firstPresent; j++ {
				existed := firstKnown && firstFound && !firstAt.After(grid[j])
				if firstKnown && !existed {
					flags[j] = false // 新上市：不是空洞
				}
				// 读取失败（!firstKnown）时保守地当作空洞
			}
		}
		result.missing[tag] = flags
	}
	return result
}

// checkGaps 检查每个标的、每个因子、每个目标周期的回看窗口。FillOnly 与否都检查。
func (r *Runner) checkGaps(ctx context.Context, plan Plan, loaded LoadResult) GapReport {
	var report GapReport
	if r == nil || r.clock == nil || len(plan.Factors) == 0 {
		return report
	}
	targets, err := r.gapTargets(plan)
	if err != nil || len(targets) == 0 {
		return report
	}
	maxL := maxLookback(plan.Factors)
	for _, subject := range intersectLists(plan.Expected, loaded.Available) {
		grids := r.subjectGrid(ctx, plan, subject, loaded.Frames[subject], targets, maxL)
		if grids == nil {
			continue
		}
		// 每个序列的缺失前缀和，用来 O(1) 判断窗口内是否有缺失。
		prefix := make(map[string][]int, len(grids.tags))
		for _, tag := range grids.tags {
			counts := make([]int, len(grids.grid)+1)
			for j, missing := range grids.missing[tag] {
				counts[j+1] = counts[j]
				if missing {
					counts[j+1]++
				}
			}
			prefix[tag] = counts
		}
		for _, factor := range plan.Factors {
			lookback := factor.LookbackPeriods
			if lookback < 1 {
				lookback = 1
			}
			for i, target := range targets {
				start, end := maxL-lookback+i, maxL-1+i
				for _, tag := range grids.tags {
					if prefix[tag][end+1]-prefix[tag][start] == 0 {
						continue
					}
					var missing []time.Time
					for j := start; j <= end && len(missing) < maxGapMissing; j++ {
						if grids.missing[tag][j] {
							missing = append(missing, grids.grid[j])
						}
					}
					report.add(GapKey{FactorID: factor.FactorID, Subject: subject, Period: target}, missing, tag)
				}
			}
		}
	}
	return report
}

// cacheHoles 返回帧在最大回看窗口（目标为 plan.PeriodTime）里有空洞的标的：这些标的的窗口不能进实时缓存。
func (r *Runner) cacheHoles(ctx context.Context, plan Plan, frames map[string]*storageio.Frame) map[string]struct{} {
	holes := make(map[string]struct{})
	if r == nil || r.clock == nil || len(plan.Factors) == 0 {
		return holes
	}
	targets, err := r.gapTargets(plan)
	if err != nil || len(targets) == 0 {
		return holes
	}
	maxL := maxLookback(plan.Factors)
	for subject, frame := range frames {
		grids := r.subjectGrid(ctx, plan, subject, frame, targets, maxL)
		if grids == nil {
			continue
		}
		for _, tag := range grids.tags {
			for _, missing := range grids.missing[tag] {
				if missing {
					holes[subject] = struct{}{}
					break
				}
			}
			if _, bad := holes[subject]; bad {
				break
			}
		}
	}
	return holes
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	values = append(values, value)
	sort.Strings(values)
	return values
}

func appendUniqueTime(values []time.Time, value time.Time) []time.Time {
	for _, existing := range values {
		if existing.Equal(value) {
			return values
		}
	}
	values = append(values, value.UTC())
	sort.Slice(values, func(i, j int) bool { return values[i].Before(values[j]) })
	return values
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
