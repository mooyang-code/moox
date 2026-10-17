package marketfetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// planLeadTime 是新周期起点 P 距当前时间的最短提前量。
const planLeadTime = 2 * time.Minute

// PlanDatasetChange 是一份采集计划里一个数据集的变更。
type PlanDatasetChange struct {
	PlanDatasetState
	// Changed：该数据集在某些函数上的采集内容发生变化，需要选生效周期 P；
	// HashChanged：标的集合 hash 变化，需要在 Storage 登记新快照；Gone：数据集不再采集。
	Changed     bool               `json:"changed,omitempty"`
	HashChanged bool               `json:"hash_changed,omitempty"`
	Gone        bool               `json:"gone,omitempty"`
	OldHash     string             `json:"old_hash,omitempty"`
	P           int64              `json:"p,omitempty"`
	Series      []seriesset.Series `json:"series,omitempty"`
	Added       []string           `json:"added,omitempty"`
	Removed     []string           `json:"removed,omitempty"`
}

// PlanFunctionChange 是一份采集计划里一个函数的变更。Old 是已生效（单份）的计划，New 是目标计划；
// New 为空表示该函数退役。
type PlanFunctionChange struct {
	Name    string `json:"name"`
	Region  string `json:"region"`
	Market  string `json:"market"`
	Old     *Plan  `json:"old,omitempty"`
	OldCron string `json:"old_cron,omitempty"`
	New     *Plan  `json:"new,omitempty"`
	NewCron string `json:"new_cron,omitempty"`
	// Push：需要向云端下发过渡环境；Create：函数尚不存在，需要先创建。
	Push   bool `json:"push,omitempty"`
	Create bool `json:"create,omitempty"`
}

// FetchPlanContent 是 t_collector_fetch_plans.c_plan 的内容：本次变更的完整描述。
// Functions 含全部函数（含无变化的），所以最近一份已生效的计划就是完整的已生效状态。
type FetchPlanContent struct {
	Datasets  map[string]*PlanDatasetChange  `json:"datasets"`
	Functions map[string]*PlanFunctionChange `json:"functions"`
}

// FetchPlanProgress 是 c_progress 的内容：各步的确认状态。
type FetchPlanProgress struct {
	Registered   map[string]bool `json:"registered,omitempty"`
	Created      map[string]bool `json:"created,omitempty"`
	Transitioned map[string]bool `json:"transitioned,omitempty"`
	Finalized    map[string]bool `json:"finalized,omitempty"`
	LoggedStart  bool            `json:"logged_start,omitempty"`
	LoggedApply  bool            `json:"logged_apply,omitempty"`
}

func (p *FetchPlanProgress) mark(set *map[string]bool, key string) {
	if *set == nil {
		*set = map[string]bool{}
	}
	(*set)[key] = true
}

func decodeFetchPlanContent(raw string) (*FetchPlanContent, error) {
	var content FetchPlanContent
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return nil, fmt.Errorf("decode fetch plan content: %w", err)
	}
	return &content, nil
}

func decodeFetchPlanProgress(raw string) (*FetchPlanProgress, error) {
	var progress FetchPlanProgress
	if strings.TrimSpace(raw) == "" {
		return &progress, nil
	}
	if err := json.Unmarshal([]byte(raw), &progress); err != nil {
		return nil, fmt.Errorf("decode fetch plan progress: %w", err)
	}
	return &progress, nil
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// nextBoundary 返回不早于 at 的第一个 K 线边界（开始时间）。
func nextBoundary(frequency marketdata.Frequency, at time.Time) time.Time {
	at = at.UTC()
	if step := frequency.Duration(); step > 0 {
		// 与 frequencyDue、klinecheck.alignUp 一致用 Truncate（以零时刻为基准）对齐，周线落在周一；Unix 取模会落在周四。
		start := at.Truncate(step)
		if start.Before(at) {
			start = start.Add(step)
		}
		return start
	}
	first := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	if first.Before(at) {
		first = first.AddDate(0, 1, 0)
	}
	return first
}

// planLeadTimeWithCreate 是计划里包含需要新建的函数时 P 的最短提前量：创建 VPC 函数要几十秒到数分钟。
const planLeadTimeWithCreate = 6 * time.Minute

// ChoosePeriodStartWithLead 与 ChoosePeriodStart 相同，但提前量由调用方指定。
func ChoosePeriodStartWithLead(frequency string, now time.Time, lead time.Duration) (int64, error) {
	parsed, err := marketdata.ParseFrequency(frequency)
	if err != nil {
		return 0, err
	}
	return nextBoundary(parsed, now.Add(lead)).Unix(), nil
}

func lastCopy(dataset *PlanDataset) *PlanCopy {
	if dataset == nil || len(dataset.Copies) == 0 {
		return nil
	}
	return &dataset.Copies[len(dataset.Copies)-1]
}

func findPlanDataset(plan *Plan, key string) *PlanDataset {
	if plan == nil {
		return nil
	}
	for i := range plan.Datasets {
		if planDatasetKey(plan.Datasets[i].DatasetID, plan.Datasets[i].Frequency) == key {
			return &plan.Datasets[i]
		}
	}
	return nil
}

func planDatasetKeys(plans ...*Plan) []string {
	seen := map[string]struct{}{}
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		for _, dataset := range plan.Datasets {
			seen[planDatasetKey(dataset.DatasetID, dataset.Frequency)] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// copyContentEqual 比较两份的内容（忽略生效起点）。
func copyContentEqual(left, right *PlanCopy) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.Hash != right.Hash || left.Count != right.Count || len(left.Subjects) != len(right.Subjects) {
		return false
	}
	for i := range left.Subjects {
		if left.Subjects[i] != right.Subjects[i] {
			return false
		}
	}
	return true
}

// emptyIfNoSubjects 把没有标的的份视为不存在（不再采集）。
func activeCopy(dataset *PlanDataset) *PlanCopy {
	copy := lastCopy(dataset)
	if copy == nil || len(copy.Subjects) == 0 {
		return nil
	}
	return copy
}

func cloneCopy(copy PlanCopy) PlanCopy {
	copy.Subjects = append([]PlanSubject(nil), copy.Subjects...)
	return copy
}

func cloneDataset(dataset PlanDataset) PlanDataset {
	dataset.OutputFields = append([]string(nil), dataset.OutputFields...)
	copies := make([]PlanCopy, len(dataset.Copies))
	for i, copy := range dataset.Copies {
		copies[i] = cloneCopy(copy)
	}
	dataset.Copies = copies
	return dataset
}

// BuildPlanContent 比较已生效状态与期望状态，生成采集计划的内容。返回的 changed 为 false 表示没有任何变化。
// choosePeriod 为需要生效周期的数据集选 P。
func BuildPlanContent(previous map[string]*PlanFunction, previousDatasets map[string]*PlanDatasetState, desired *DesiredState, choosePeriod func(frequency string) (int64, error)) (*FetchPlanContent, bool, error) {
	content := &FetchPlanContent{Datasets: map[string]*PlanDatasetChange{}, Functions: map[string]*PlanFunctionChange{}}
	changed := false

	// 数据集：hash 变化、新增、不再采集。
	datasetKeys := map[string]struct{}{}
	for key := range previousDatasets {
		datasetKeys[key] = struct{}{}
	}
	for key := range desired.Datasets {
		datasetKeys[key] = struct{}{}
	}
	for key := range datasetKeys {
		old := previousDatasets[key]
		current := desired.Datasets[key]
		change := &PlanDatasetChange{}
		switch {
		case current == nil:
			change.PlanDatasetState = *old
			change.Gone, change.Changed = true, true
			change.OldHash = old.Hash
		default:
			change.PlanDatasetState = current.PlanDatasetState
			if old == nil || old.Hash != current.Hash {
				change.HashChanged, change.Changed = true, true
				change.Series = current.Series
				if old != nil {
					change.OldHash = old.Hash
				}
			}
		}
		content.Datasets[key] = change
	}

	// 函数：逐个对比，发现采集内容变化的数据集。
	names := map[string]struct{}{}
	for name := range previous {
		names[name] = struct{}{}
	}
	for name := range desired.Functions {
		names[name] = struct{}{}
	}
	for name := range names {
		old, target := previous[name], desired.Functions[name]
		function := &PlanFunctionChange{Name: name}
		if old != nil {
			function.Region, function.Market, function.Old, function.OldCron = old.Region, old.Market, old.Plan, old.Cron
		}
		if target != nil {
			function.Region, function.Market, function.New, function.NewCron = target.Region, target.Market, target.Plan, target.Cron
		}
		var oldPlan, newPlan *Plan
		if old != nil {
			oldPlan = old.Plan
		}
		if target != nil {
			newPlan = target.Plan
		}
		for _, key := range planDatasetKeys(oldPlan, newPlan) {
			if !copyContentEqual(activeCopy(findPlanDataset(oldPlan, key)), activeCopy(findPlanDataset(newPlan, key))) {
				if change := content.Datasets[key]; change != nil {
					change.Changed = true
				}
			}
		}
		content.Functions[name] = function
	}

	// 选 P，并记录新增与移除的标的。
	for key, change := range content.Datasets {
		if !change.Changed {
			continue
		}
		p, err := choosePeriod(change.Frequency)
		if err != nil {
			return nil, false, err
		}
		change.P = p
		oldSubjects := subjectsOfDataset(previous, key)
		newSubjects := map[string]struct{}{}
		if current := desired.Datasets[key]; current != nil {
			for _, series := range current.Series {
				newSubjects[series.SubjectID] = struct{}{}
			}
		}
		for subject := range newSubjects {
			if _, ok := oldSubjects[subject]; !ok {
				change.Added = append(change.Added, subject)
			}
		}
		for subject := range oldSubjects {
			if _, ok := newSubjects[subject]; !ok {
				change.Removed = append(change.Removed, subject)
			}
		}
		sort.Strings(change.Added)
		sort.Strings(change.Removed)
		changed = true
	}

	// 哪些函数需要下发：过渡计划与已生效计划不同、cron 不同、或新函数。
	for _, function := range content.Functions {
		transitional := content.TransitionalPlan(function)
		function.Create = function.Old == nil && function.New != nil
		switch {
		case function.Old == nil && function.New == nil:
		case function.Create, function.New == nil, function.OldCron != function.NewCron:
			function.Push = true
		default:
			function.Push = !plansEqual(function.Old, transitional)
		}
		if function.Push {
			changed = true
		}
	}
	return content, changed, nil
}

func plansEqual(left, right *Plan) bool {
	return bytes.Equal([]byte(mustJSON(left)), []byte(mustJSON(right)))
}

// subjectsOfDataset 汇总已生效状态里所有函数负责的该数据集的标的。
func subjectsOfDataset(functions map[string]*PlanFunction, key string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, function := range functions {
		if function == nil {
			continue
		}
		if copy := activeCopy(findPlanDataset(function.Plan, key)); copy != nil {
			for _, subject := range copy.Subjects {
				result[subject.ID] = struct{}{}
			}
		}
	}
	return result
}

// TransitionalPlan 是函数在变更窗口内使用的计划：变化的数据集同时带旧份与从 P 起的新份。
func (c *FetchPlanContent) TransitionalPlan(function *PlanFunctionChange) *Plan {
	base := function.New
	if base == nil {
		base = function.Old
	}
	if base == nil {
		return nil
	}
	out := &Plan{V: base.V, Market: base.Market, Fetch: base.Fetch}
	// 例外表：只保留过渡计划里仍会采集的标的，新计划的映射优先于旧计划（例外被删除时旧值随之消失）。
	exceptions := map[string]string{}
	if function.Old != nil {
		for key, value := range function.Old.SymbolExceptions {
			exceptions[key] = value
		}
	}
	if function.New != nil {
		for key := range function.Old.symbolsOrNil() {
			if _, kept := function.New.SymbolExceptions[key]; !kept && function.New.hasSubject(key) {
				delete(exceptions, key)
			}
		}
		for key, value := range function.New.SymbolExceptions {
			exceptions[key] = value
		}
	}
	if len(exceptions) > 0 {
		out.SymbolExceptions = exceptions
	}
	for _, key := range planDatasetKeys(function.Old, function.New) {
		oldDataset, newDataset := findPlanDataset(function.Old, key), findPlanDataset(function.New, key)
		meta := newDataset
		if meta == nil {
			meta = oldDataset
		}
		change := c.Datasets[key]
		oldCopy, newCopy := activeCopy(oldDataset), activeCopy(newDataset)
		if change == nil || !change.Changed || copyContentEqual(oldCopy, newCopy) {
			// 本函数在该数据集上没有变化：沿用旧份（没有旧份时用新份，起点取 P）。
			source := oldDataset
			if source == nil || len(source.Copies) == 0 {
				source = newDataset
			}
			if source == nil || lastCopy(source) == nil {
				continue
			}
			dataset := cloneDataset(*source)
			dataset.LatenessS, dataset.OutputFields = meta.LatenessS, append([]string(nil), meta.OutputFields...)
			if oldDataset == nil && change != nil {
				dataset.Copies[len(dataset.Copies)-1].From = change.P
			}
			out.Datasets = append(out.Datasets, dataset)
			continue
		}
		dataset := PlanDataset{DatasetID: meta.DatasetID, Frequency: meta.Frequency, LatenessS: meta.LatenessS, OutputFields: append([]string(nil), meta.OutputFields...)}
		if oldCopy != nil {
			dataset.Copies = append(dataset.Copies, cloneCopy(*oldCopy))
		}
		next := PlanCopy{From: change.P, Hash: change.Hash, Count: change.Count}
		if newCopy != nil {
			next = cloneCopy(*newCopy)
			next.From = change.P
		}
		dataset.Copies = append(dataset.Copies, next)
		out.Datasets = append(out.Datasets, dataset)
	}
	out.pruneExceptions()
	return out
}

// SymbolAt 返回函数在某数据集上、at 时刻适用的标的交易所符号。符号例外是函数级的，过渡计划已把新旧例外合并，
// 所以必须按原始新旧计划与生效时间 P 判断：P 之前用旧计划的映射，P 及之后用新计划的映射。
// 新旧计划不同时包含该标的、或该数据集没有变更时返回 false，由调用方沿用计划自带的映射。
func (c *FetchPlanContent) SymbolAt(function *PlanFunctionChange, key, subjectID string, at time.Time) (string, bool) {
	if function == nil || function.Old == nil || function.New == nil {
		return "", false
	}
	change := c.Datasets[key]
	if change == nil || !change.Changed {
		return "", false
	}
	if at.Unix() < change.P {
		if function.Old.hasSubject(subjectID) {
			return function.Old.ExchangeSymbol(subjectID), true
		}
		return "", false
	}
	if function.New.hasSubject(subjectID) {
		return function.New.ExchangeSymbol(subjectID), true
	}
	return "", false
}

// SymbolChangedWithoutBoundary 判断该函数在这次计划里是否发生了没有明确生效边界的符号映射变更：
// 新旧计划都包含该标的、映射不同，而数据集本身没有变更（没有 P）。此时无法确定旧映射适用到何时。
func (c *FetchPlanContent) SymbolChangedWithoutBoundary(function *PlanFunctionChange, key, subjectID string) bool {
	if function == nil || function.Old == nil || function.New == nil {
		return false
	}
	if change := c.Datasets[key]; change != nil && change.Changed {
		return false
	}
	return function.Old.hasSubject(subjectID) && function.New.hasSubject(subjectID) &&
		function.Old.ExchangeSymbol(subjectID) != function.New.ExchangeSymbol(subjectID)
}

// FinalPlan 是变更窗口结束后的计划：每个数据集只留最新一份，没有标的的数据集移除；退役函数返回空。
func (c *FetchPlanContent) FinalPlan(function *PlanFunctionChange) *Plan {
	if function.New == nil {
		return nil
	}
	transitional := c.TransitionalPlan(function)
	if transitional == nil {
		return nil
	}
	out := &Plan{V: transitional.V, Market: transitional.Market, Fetch: transitional.Fetch, SymbolExceptions: transitional.SymbolExceptions}
	for _, dataset := range transitional.Datasets {
		copy := lastCopy(&dataset)
		if copy == nil || len(copy.Subjects) == 0 {
			continue
		}
		final := cloneDataset(dataset)
		final.Copies = []PlanCopy{cloneCopy(*copy)}
		out.Datasets = append(out.Datasets, final)
	}
	if len(out.Datasets) == 0 {
		return nil
	}
	out.pruneExceptions()
	return out
}

// TransitionalCron 覆盖新旧两份需要的频率；FinalCron 只服务最终计划。
func (c *FetchPlanContent) TransitionalCron(function *PlanFunctionChange) string {
	plan := c.TransitionalPlan(function)
	if plan == nil {
		return ""
	}
	return planCron(plan)
}

func (c *FetchPlanContent) FinalCron(function *PlanFunctionChange) string {
	plan := c.FinalPlan(function)
	if plan == nil {
		return ""
	}
	return planCron(plan)
}

// Applied 返回该计划生效后的函数状态（用于下一次计算期望状态时保持分配稳定）。
func (c *FetchPlanContent) Applied() (map[string]*PlanFunction, map[string]*PlanDatasetState) {
	functions := map[string]*PlanFunction{}
	for name, function := range c.Functions {
		if final := c.FinalPlan(function); final != nil {
			functions[name] = &PlanFunction{Name: name, Region: function.Region, Market: function.Market, Cron: planCron(final), Plan: final}
		}
	}
	datasets := map[string]*PlanDatasetState{}
	for key, change := range c.Datasets {
		if change.Gone {
			continue
		}
		state := change.PlanDatasetState
		datasets[key] = &state
	}
	return functions, datasets
}

// WindowEnd 是旧份的最后保留时间：所有变化数据集的 P + 允许迟到时间（旧周期在此之后不会再被合法提交）。
func (c *FetchPlanContent) WindowEnd() time.Time {
	var end time.Time
	for _, change := range c.Datasets {
		if !change.Changed || change.P <= 0 {
			continue
		}
		candidate := time.Unix(change.P, 0).UTC().Add(time.Duration(change.LatenessS) * time.Second)
		if candidate.After(end) {
			end = candidate
		}
	}
	return end
}

// SortedFunctionNames 返回按名称排序的函数名。
func (c *FetchPlanContent) SortedFunctionNames() []string {
	names := make([]string, 0, len(c.Functions))
	for name := range c.Functions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DecodeFetchPlanContent 解码采集计划内容（检查与补采还原历史标的时使用）。
func DecodeFetchPlanContent(raw string) (*FetchPlanContent, error) {
	return decodeFetchPlanContent(raw)
}

// DecodeFetchPlanProgress 解码采集计划进度。
func DecodeFetchPlanProgress(raw string) (*FetchPlanProgress, error) {
	return decodeFetchPlanProgress(raw)
}

// PlanDatasetKeyOf 返回计划中数据集的“数据集|频率”键。
func PlanDatasetKeyOf(dataset PlanDataset) string {
	return planDatasetKey(dataset.DatasetID, dataset.Frequency)
}

// symbolsOrNil 返回计划里出现过的标的集合；计划为空返回 nil。
func (p *Plan) symbolsOrNil() map[string]struct{} {
	if p == nil {
		return nil
	}
	subjects := map[string]struct{}{}
	for _, dataset := range p.Datasets {
		for _, copy := range dataset.Copies {
			for _, subject := range copy.Subjects {
				subjects[subject.ID] = struct{}{}
			}
		}
	}
	return subjects
}

func (p *Plan) hasSubject(id string) bool {
	_, ok := p.symbolsOrNil()[id]
	return ok
}

// pruneExceptions 去掉不再出现在任何份里的标的的例外。
func (p *Plan) pruneExceptions() {
	if len(p.SymbolExceptions) == 0 {
		return
	}
	subjects := p.symbolsOrNil()
	for key := range p.SymbolExceptions {
		if _, ok := subjects[key]; !ok {
			delete(p.SymbolExceptions, key)
		}
	}
	if len(p.SymbolExceptions) == 0 {
		p.SymbolExceptions = nil
	}
}
