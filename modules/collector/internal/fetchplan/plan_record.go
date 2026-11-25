package fetchplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// planLeadTime 是新周期起点 P 距当前时间的最短提前量。
const planLeadTime = 2 * time.Minute

// planLeadTimePushOnly 是首份纯 WS 计划的提前量：没有云端步骤，也没有旧执行端要交接，collector-stream 心跳 10 秒一次就能接上。
const planLeadTimePushOnly = 30 * time.Second

// copyWindowMargin 是旧份窗口结束后再多留的时间，让最后一批旧周期提交落地。
const copyWindowMargin = 5 * time.Second

// 数据集在一份计划里的动作（决策表见 decideDataset）。
const (
	PlanActionKeep    = "keep"    // 内容不变（可能裁掉过期的旧份）
	PlanActionSymbols = "symbols" // 只有交易所符号变了：份不变，时间线写新符号
	PlanActionReplace = "replace" // 头份还没开始：原地换成新内容，生效点不变
	PlanActionAppend  = "append"  // 在新的生效点加一份
	PlanActionWait    = "wait"    // 已有两份且旧份未过窗口：本轮不动
)

// DatasetCopy 是一个数据集的一份（数据集层面，跨函数）：从 From 起使用的快照。
// 每一份的 Hash 都等于 Storage 生效表在它的 From 上的有效 hash：登记过的份是登记的 hash，只改分配的份沿用前一份的，
// 停止份取被替换的头份（原地替换）或前一份（加一份）的。决策表靠这条判断要不要登记。
type DatasetCopy struct {
	From  int64  `json:"from"`
	Hash  string `json:"hash"`
	Count uint32 `json:"count"`
	// LatenessS 是这一份的周期按哪个截止建立（交接时旧份记旧截止）。
	LatenessS int `json:"lateness_s"`
	// Ingest 是这一份的执行端（空按 scf）：两份执行端不同说明正在交接，生效点是后一份的 From。
	Ingest string `json:"ingest,omitempty"`
	// Stop 表示从 From 起不再采集（没有标的）。
	Stop bool `json:"stop,omitempty"`
}

func (c DatasetCopy) ingestOrSCF() string {
	if c.Ingest == "" {
		return marketfetch.PlanExecutorSCF
	}
	return c.Ingest
}

// PlanDataset 是一个数据集在本计划之后的完整状态，加上本计划对它做了什么。
type PlanDataset struct {
	// PlanDatasetState 是期望（来自任务与标签）；头份是停止份时是最后一次的期望，等待或撤回时保持已生效的值。
	PlanDatasetState
	// Copies 是本计划生效后的份（按 From 升序，1～2 份）。
	Copies []DatasetCopy `json:"copies"`
	// TimelineFrom 是这个数据集频率在标的时间线上最后一行的生效点（没有行时为 0）。
	TimelineFrom int64  `json:"timeline_from,omitempty"`
	Action       string `json:"action"`
	// P 是 replace、append、symbols 的生效点。
	P int64 `json:"p,omitempty"`
	// Register 表示要向 Storage 登记 Hash@P；Timeline 表示本计划在 P 写一行时间线（登记的在登记成功时写，其余在建计划时写）。
	Register bool `json:"register,omitempty"`
	Timeline bool `json:"timeline,omitempty"`
	// Trimmed 表示本计划去掉了过期的旧份。
	Trimmed bool               `json:"trimmed,omitempty"`
	Series  []seriesset.Series `json:"series,omitempty"`
	Added   []string           `json:"added,omitempty"`
	Removed []string           `json:"removed,omitempty"`
}

// Head 返回头份（From 最大的一份）。
func (d *PlanDataset) Head() *DatasetCopy {
	if d == nil || len(d.Copies) == 0 {
		return nil
	}
	return &d.Copies[len(d.Copies)-1]
}

// HandoffFrom 返回交接的生效点：两份执行端不同时是后一份的 From，否则为 0。
func (d *PlanDataset) HandoffFrom() int64 {
	if d == nil || len(d.Copies) != 2 || d.Copies[0].ingestOrSCF() == d.Copies[1].ingestOrSCF() {
		return 0
	}
	return d.Copies[1].From
}

// changesCopies 判断本计划改了这个数据集的头份（原地替换或加一份）。
func (d *PlanDataset) changesCopies() bool {
	return d.Action == PlanActionReplace || d.Action == PlanActionAppend
}

// PlanFunctionChange 是一份采集计划里的一个函数。Old 是已生效计划里它的计划（可能带旧份），New 是期望状态给出的单份计划
// （只用于组合），Target 是本计划要下发的计划，nil 表示退役（只停定时器）。
type PlanFunctionChange struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	Market string `json:"market"`
	// Executor 为空表示 SCF 函数；stream、local 由 collector-stream 心跳确认，没有云端步骤。
	Executor string            `json:"executor,omitempty"`
	Old      *marketfetch.Plan `json:"old,omitempty"`
	OldCron  string            `json:"old_cron,omitempty"`
	New      *marketfetch.Plan `json:"new,omitempty"`
	Target   *marketfetch.Plan `json:"target,omitempty"`
	Cron     string            `json:"cron,omitempty"`
	// Push：需要下发 Target 与 Cron（或停定时器）；Create：已生效计划里没有这个云函数（下发时 Ensure 会建出它），选 P 时提前量用 6 分钟。
	Push   bool `json:"push,omitempty"`
	Create bool `json:"create,omitempty"`
}

// FetchPlanContent 是 t_collector_fetch_plans.c_plan 的内容。Datasets、Functions 是生效后的完整状态
// （含没有变化的），所以最近一份已生效的计划就是完整的已生效状态。
type FetchPlanContent struct {
	Datasets  map[string]*PlanDataset        `json:"datasets"`
	Functions map[string]*PlanFunctionChange `json:"functions"`
	// LeadSeconds 是这份计划选 P 用的提前量。
	LeadSeconds int `json:"lead_seconds,omitempty"`
}

// FetchPlanProgress 是 c_progress 的内容：各步的确认状态。
type FetchPlanProgress struct {
	Registered  map[string]bool `json:"registered,omitempty"`
	Pushed      map[string]bool `json:"pushed,omitempty"`
	LoggedStart bool            `json:"logged_start,omitempty"`
	LoggedApply bool            `json:"logged_apply,omitempty"`
	// StreamPushed 表示已要求推送与本地执行端应用这份计划（collector-stream 心跳拿到新版本后拉取）。
	StreamPushed bool `json:"stream_pushed,omitempty"`
}

// IsSCF 判断该项是 SCF 函数（需要云端创建、下发环境与定时器）。
func (f *PlanFunctionChange) IsSCF() bool {
	return f.Executor == "" || f.Executor == marketfetch.PlanExecutorSCF
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

func lastCopy(dataset *marketfetch.PlanDataset) *marketfetch.PlanCopy {
	if dataset == nil || len(dataset.Copies) == 0 {
		return nil
	}
	return &dataset.Copies[len(dataset.Copies)-1]
}

// copyAt 返回函数计划里这个数据集从 from 起的那一份。
func copyAt(dataset *marketfetch.PlanDataset, from int64) *marketfetch.PlanCopy {
	if dataset == nil {
		return nil
	}
	for i := range dataset.Copies {
		if dataset.Copies[i].From == from {
			return &dataset.Copies[i]
		}
	}
	return nil
}

func findPlanDataset(plan *marketfetch.Plan, key string) *marketfetch.PlanDataset {
	if plan == nil {
		return nil
	}
	for i := range plan.Datasets {
		if marketfetch.PlanDatasetKey(plan.Datasets[i].DatasetID, plan.Datasets[i].Frequency) == key {
			return &plan.Datasets[i]
		}
	}
	return nil
}

func planDatasetKeys(plans ...*marketfetch.Plan) []string {
	seen := map[string]struct{}{}
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		for _, dataset := range plan.Datasets {
			seen[marketfetch.PlanDatasetKey(dataset.DatasetID, dataset.Frequency)] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// activeCopy 返回最后一份；没有标的的份视为不存在。
func activeCopy(dataset *marketfetch.PlanDataset) *marketfetch.PlanCopy {
	copy := lastCopy(dataset)
	if copy == nil || len(copy.Subjects) == 0 {
		return nil
	}
	return copy
}

func sameSubjects(left, right []marketfetch.PlanSubject) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func cloneCopy(copy marketfetch.PlanCopy) marketfetch.PlanCopy {
	copy.Subjects = append([]marketfetch.PlanSubject(nil), copy.Subjects...)
	return copy
}

// PlanTiming 是建计划时决策表用到的时间：当前时间、提前量，以及本轮交接截止已改成功的数据集（两份截止不同的数据集裁旧份的前提）。
type PlanTiming struct {
	Now        time.Time
	Lead       time.Duration
	LatenessOK map[string]bool
}

// BuildPlanContent 按数据集查决策表（decideDataset），再组合每个函数要下发的计划（composeTarget），生成采集计划的内容。
// previousDatasets 是已生效状态（Applied）里的数据集。changed 为 false 表示下一状态与已生效状态相同，不用建计划；
// waiting 是本轮等待的数据集与原因。
func BuildPlanContent(previous map[string]*PlanFunction, previousDatasets map[string]*PlanDataset, desired *DesiredState, timing PlanTiming) (*FetchPlanContent, bool, []string, error) {
	content := &FetchPlanContent{Datasets: map[string]*PlanDataset{}, Functions: map[string]*PlanFunctionChange{}, LeadSeconds: int(timing.Lead / time.Second)}
	keys := map[string]struct{}{}
	for key := range previousDatasets {
		keys[key] = struct{}{}
	}
	for key := range desired.Datasets {
		keys[key] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Strings(sorted)
	var waiting []string
	for _, key := range sorted {
		dataset, reason, err := decideDataset(key, previousDatasets[key], desired.Datasets[key], previous, desired.Functions, timing)
		if err != nil {
			return nil, false, nil, err
		}
		if reason != "" {
			waiting = append(waiting, reason)
		}
		if dataset != nil {
			content.Datasets[key] = dataset
		}
	}
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
			function.Region, function.Market, function.Executor, function.Old, function.OldCron = old.Region, old.Market, old.Executor, old.Plan, old.Cron
		}
		if target != nil {
			function.Region, function.Market, function.Executor, function.New = target.Region, target.Market, target.Executor, target.Plan
		}
		content.Functions[name] = function
	}
	content.composeFunctions()
	return content, content.changedFrom(previousDatasets), waiting, nil
}

// composeFunctions 按数据集决策后的份重新组合每个函数的 Target、Cron、Push、Create；
// 已生效计划里没有、本计划也不下发的函数（数据集全在等待的新函数）不放进计划。
func (c *FetchPlanContent) composeFunctions() {
	for name, function := range c.Functions {
		function.Target = c.composeTarget(function)
		function.Cron = ""
		if function.Target != nil && function.IsSCF() {
			function.Cron = planCron(function.Target)
		}
		if function.Old == nil && function.Target == nil {
			delete(c.Functions, name)
			continue
		}
		function.Create = function.Old == nil && function.IsSCF()
		function.Push = function.Create || function.Target == nil || !plansEqual(function.Old, function.Target) || function.OldCron != function.Cron
	}
}

// changedFrom 判断本计划的下一状态与已生效状态不同：有数据集动作不是 keep/wait、有裁剪、有数据集消失、或有函数要下发。
func (c *FetchPlanContent) changedFrom(previousDatasets map[string]*PlanDataset) bool {
	for _, dataset := range c.Datasets {
		if (dataset.Action != PlanActionKeep && dataset.Action != PlanActionWait) || dataset.Trimmed {
			return true
		}
	}
	for key := range previousDatasets {
		if c.Datasets[key] == nil {
			return true
		}
	}
	for _, function := range c.Functions {
		if function.Push {
			return true
		}
	}
	return false
}

// decideDataset 是每个数据集每轮一次的决策表：先裁剪，再判断内容有没有变，最后查表（K、S、R、A、W）。
// 返回 nil 表示数据集从已生效状态里消失（只剩已到生效点的停止份，或从来没有过）。
func decideDataset(key string, prev *PlanDataset, want *DesiredDataset, previous, desired map[string]*PlanFunction, timing PlanTiming) (*PlanDataset, string, error) {
	now := timing.Now.UTC()
	out := &PlanDataset{Action: PlanActionKeep}
	var frequencyName string
	if prev != nil {
		out.PlanDatasetState = prev.PlanDatasetState
		out.Copies = append([]DatasetCopy(nil), prev.Copies...)
		out.TimelineFrom = prev.TimelineFrom
		frequencyName = prev.Frequency
	}
	if want != nil {
		frequencyName = want.Frequency
	}
	frequency, err := marketdata.ParseFrequency(frequencyName)
	if err != nil {
		return nil, "", fmt.Errorf("dataset %s: %w", key, err)
	}
	// 第 1 步：裁剪。旧份过了窗口（后一份 From + 两份截止里大的那个 + 余量）就去掉；两份截止不同时，本轮交接截止要先改成功。
	blocked := false
	if len(out.Copies) == 2 {
		first, second := out.Copies[0], out.Copies[1]
		lateness := max(first.LatenessS, second.LatenessS)
		if !now.Before(time.Unix(second.From, 0).Add(time.Duration(lateness)*time.Second + copyWindowMargin)) {
			if first.LatenessS != second.LatenessS && !timing.LatenessOK[key] {
				blocked = true
			} else {
				out.Copies, out.Trimmed = out.Copies[1:], true
			}
		}
	}
	// 只剩已到生效点的停止份：数据集从已生效状态里消失。停止份在生效点之前要留着，删了又马上加回时才能原地替换。
	if len(out.Copies) == 1 && out.Copies[0].Stop && out.Copies[0].From <= now.Unix() {
		out.Copies, out.Trimmed, out.TimelineFrom = nil, true, 0
	}
	if len(out.Copies) == 0 && want == nil {
		return nil, "", nil
	}
	if len(out.Copies) == 0 {
		// 从零开始的数据集：没有已生效的期望可沿用。
		out.PlanDatasetState = PlanDatasetState{}
	}

	// 第 2 步：内容有没有变。
	head := out.Head()
	same := false
	switch {
	case want == nil:
		same = head == nil || head.Stop
	case head != nil && !head.Stop && want.Hash == head.Hash:
		same = allocationSame(key, head.From, previous, desired)
	}
	stateFromWant := func() {
		out.PlanDatasetState = want.PlanDatasetState
		out.Stopped = false
	}

	// 第 3 步：查表。
	pNew := nextBoundary(frequency, now.Add(timing.Lead)).Unix()
	q := max(pNew, out.TimelineFrom)
	switch {
	case same && (want == nil || prev == nil || sameSymbols(prev.Symbols, want.Symbols)):
		// K：只做裁剪。截止、输出字段可能变了（函数 Target 随之变化），沿用期望。
		if want != nil {
			stateFromWant()
		}
		return out, "", nil
	case same:
		// S：只有符号变了。份不变，时间线在 Q 写一行（同一生效点覆盖最后一行）。
		stateFromWant()
		out.Action, out.P, out.Timeline, out.TimelineFrom = PlanActionSymbols, q, true, q
		out.Series = append([]seriesset.Series(nil), want.Series...)
		return out, "", nil
	case blocked:
		return out, fmt.Sprintf("%s 等交接截止改成功后再去掉旧份", key), nil
	}
	lastStop, lastHash, lastSymbols := false, "", map[string]string(nil)
	if prev != nil && out.TimelineFrom > 0 {
		if prevHead := prev.Head(); prevHead != nil {
			lastStop, lastHash, lastSymbols = prevHead.Stop, prevHead.Hash, prev.Symbols
		}
	}
	rowDiffers := func() bool {
		if out.TimelineFrom == 0 {
			return true
		}
		if want == nil {
			return !lastStop
		}
		return lastStop || lastHash != want.Hash || !sameSymbols(lastSymbols, want.Symbols)
	}
	oldSubjects := subjectsAt(previous, key, head)
	switch {
	case head != nil && head.From >= pNew && head.From >= out.TimelineFrom:
		// R：头份还没开始，原地换成新内容，生效点不变（重选的 P 可能更早，Storage 不接受）。
		replaced := *head
		writeRow := rowDiffers()
		if want == nil {
			out.Copies[len(out.Copies)-1] = DatasetCopy{From: replaced.From, Hash: replaced.Hash, Count: replaced.Count, LatenessS: replaced.LatenessS, Ingest: replaced.Ingest, Stop: true}
			out.Stopped = true
		} else {
			out.Copies[len(out.Copies)-1] = DatasetCopy{From: replaced.From, Hash: want.Hash, Count: want.Count, LatenessS: want.LatenessS, Ingest: want.IngestOrSCF()}
			out.Register = want.Hash != replaced.Hash
			stateFromWant()
		}
		out.Action, out.P = PlanActionReplace, replaced.From
		if writeRow {
			out.Timeline, out.TimelineFrom = true, out.P
		}
	case len(out.Copies) <= 1:
		// A：在 Q 加一份。Storage 的最后一条不晚于头份的 From，而头份的 From 早于 Q，登记一定严格更晚。
		writeRow := rowDiffers()
		if want == nil {
			out.Copies = append(out.Copies, DatasetCopy{From: q, Hash: head.Hash, Count: head.Count, LatenessS: head.LatenessS, Ingest: head.Ingest, Stop: true})
			out.Stopped = true
		} else {
			out.Copies = append(out.Copies, DatasetCopy{From: q, Hash: want.Hash, Count: want.Count, LatenessS: want.LatenessS, Ingest: want.IngestOrSCF()})
			out.Register = head == nil || want.Hash != head.Hash
			stateFromWant()
		}
		out.Action, out.P = PlanActionAppend, q
		if writeRow {
			out.Timeline, out.TimelineFrom = true, q
		}
	default:
		// W：已有两份、旧份未过窗口，本轮不动；下一轮重新查表。
		first := out.Copies[0]
		lateness := max(first.LatenessS, out.Copies[1].LatenessS)
		end := time.Unix(out.Copies[1].From, 0).UTC().Add(time.Duration(lateness)*time.Second + copyWindowMargin)
		return out, fmt.Sprintf("%s 等旧份窗口结束（约 %s UTC）", key, end.Format("01-02 15:04")), nil
	}
	if want != nil && (out.Register || out.Timeline) {
		out.Series = append([]seriesset.Series(nil), want.Series...)
	}
	newSubjects := map[string]struct{}{}
	if want != nil {
		for _, series := range want.Series {
			newSubjects[series.SubjectID] = struct{}{}
		}
	}
	for subject := range newSubjects {
		if _, ok := oldSubjects[subject]; !ok {
			out.Added = append(out.Added, subject)
		}
	}
	for subject := range oldSubjects {
		if _, ok := newSubjects[subject]; !ok {
			out.Removed = append(out.Removed, subject)
		}
	}
	sort.Strings(out.Added)
	sort.Strings(out.Removed)
	return out, "", nil
}

// allocationSame 判断每个函数在头份（从 from 起）上分到的标的与期望分配相同。
func allocationSame(key string, from int64, previous, desired map[string]*PlanFunction) bool {
	names := map[string]struct{}{}
	for name := range previous {
		names[name] = struct{}{}
	}
	for name := range desired {
		names[name] = struct{}{}
	}
	for name := range names {
		var old, current []marketfetch.PlanSubject
		if function := previous[name]; function != nil {
			if copy := copyAt(findPlanDataset(function.Plan, key), from); copy != nil {
				old = copy.Subjects
			}
		}
		if function := desired[name]; function != nil {
			if copy := activeCopy(findPlanDataset(function.Plan, key)); copy != nil {
				current = copy.Subjects
			}
		}
		if !sameSubjects(old, current) {
			return false
		}
	}
	return true
}

// subjectsAt 汇总已生效状态里各函数在这一份上负责的标的（活动日志的新增与移除用）。
func subjectsAt(functions map[string]*PlanFunction, key string, head *DatasetCopy) map[string]struct{} {
	result := map[string]struct{}{}
	if head == nil || head.Stop {
		return result
	}
	for _, function := range functions {
		if function == nil {
			continue
		}
		if copy := copyAt(findPlanDataset(function.Plan, key), head.From); copy != nil {
			for _, subject := range copy.Subjects {
				result[subject.ID] = struct{}{}
			}
		}
	}
	return result
}

func plansEqual(left, right *marketfetch.Plan) bool {
	return bytes.Equal([]byte(mustJSON(left)), []byte(mustJSON(right)))
}

// composeTarget 组合函数要下发的计划：每个数据集按决策后的份逐份定出本函数的标的。本计划换上或加上的头份用 New 里
// 这个函数的那部分，原来就有的份用 Old 里的那部分，停止份没有标的；去掉开头那些本函数没有标的的份，全都没有时不再带这个数据集。
// 没有任何数据集时返回 nil（退役）。
func (c *FetchPlanContent) composeTarget(function *PlanFunctionChange) *marketfetch.Plan {
	base := function.New
	if base == nil {
		base = function.Old
	}
	if base == nil {
		return nil
	}
	out := &marketfetch.Plan{V: base.V, Executor: base.Executor, Market: base.Market, Fetch: base.Fetch}
	// 例外表：新计划的映射优先；新计划里有这个标的却没写例外的，删掉旧例外；旧映射只为仍留在计划里的标的保留（最后 PruneExceptions）。
	exceptions := map[string]string{}
	if function.Old != nil {
		for key, value := range function.Old.SymbolExceptions {
			exceptions[key] = value
		}
	}
	if function.New != nil {
		for key := range function.Old.SymbolsOrNil() {
			if _, kept := function.New.SymbolExceptions[key]; !kept && function.New.HasSubject(key) {
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
	// 交易对：新计划优先，旧计划补上仍留在计划里的标的。
	pairs := map[string]string{}
	for _, plan := range []*marketfetch.Plan{function.Old, function.New} {
		if plan != nil {
			for key, value := range plan.Pairs {
				pairs[key] = value
			}
		}
	}
	if len(pairs) > 0 {
		out.Pairs = pairs
	}
	for _, key := range planDatasetKeys(function.Old, function.New) {
		dataset := c.Datasets[key]
		if dataset == nil {
			continue
		}
		oldDataset, newDataset := findPlanDataset(function.Old, key), findPlanDataset(function.New, key)
		meta := newDataset
		if meta == nil {
			meta = oldDataset
		}
		var copies []marketfetch.PlanCopy
		for i, item := range dataset.Copies {
			next := marketfetch.PlanCopy{From: item.From, Hash: item.Hash, Count: item.Count}
			switch {
			case item.Stop:
			case dataset.changesCopies() && i == len(dataset.Copies)-1:
				if fresh := activeCopy(newDataset); fresh != nil {
					next.Subjects = append([]marketfetch.PlanSubject(nil), fresh.Subjects...)
				}
			default:
				if kept := copyAt(oldDataset, item.From); kept != nil {
					next = cloneCopy(*kept)
				}
			}
			if len(copies) == 0 && len(next.Subjects) == 0 {
				continue // 开头没有标的的份不下发
			}
			if next.Hash == "" && len(copies) > 0 {
				// 重新登记清掉了 hash 的份：函数计划要求每份都有 hash，没有标的的份取前一份的（不会用它提交）。
				next.Hash, next.Count = copies[len(copies)-1].Hash, copies[len(copies)-1].Count
			}
			copies = append(copies, next)
		}
		if len(copies) == 0 {
			continue
		}
		out.Datasets = append(out.Datasets, marketfetch.PlanDataset{
			DatasetID: meta.DatasetID, Frequency: meta.Frequency, LatenessS: meta.LatenessS,
			OutputFields: append([]string(nil), meta.OutputFields...), Copies: copies,
		})
	}
	if len(out.Datasets) == 0 {
		return nil
	}
	out.PruneExceptions()
	return out
}

// Applied 返回该计划生效后的状态：有 Target 的函数，与仍在已生效状态里的数据集（Stopped 表示头份是停止份）。
func (c *FetchPlanContent) Applied() (map[string]*PlanFunction, map[string]*PlanDataset) {
	functions := map[string]*PlanFunction{}
	for name, function := range c.Functions {
		if function.Target != nil {
			functions[name] = &PlanFunction{Name: name, Region: function.Region, Market: function.Market, Executor: function.Executor, Cron: function.Cron, Plan: function.Target}
		}
	}
	datasets := map[string]*PlanDataset{}
	for key, dataset := range c.Datasets {
		if dataset == nil || len(dataset.Copies) == 0 {
			continue
		}
		clone := *dataset
		clone.Copies = append([]DatasetCopy(nil), dataset.Copies...)
		clone.Stopped = clone.Head().Stop
		datasets[key] = &clone
	}
	return functions, datasets
}

// activeStates 返回已生效状态里仍在采集的数据集的期望（跳过头份是停止份的）：交接、消失防抖、首份推送计划都只看它们。
func activeStates(datasets map[string]*PlanDataset) map[string]*PlanDatasetState {
	states := make(map[string]*PlanDatasetState, len(datasets))
	for key, dataset := range datasets {
		if dataset == nil || dataset.Stopped {
			continue
		}
		state := dataset.PlanDatasetState
		states[key] = &state
	}
	return states
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

// sortedDatasetKeys 返回按“数据集|频率”排序的数据集键。
func (c *FetchPlanContent) sortedDatasetKeys() []string {
	keys := make([]string, 0, len(c.Datasets))
	for key := range c.Datasets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// DecodeFetchPlanContent 解码采集计划内容（运行状态与采集方式切换读取未生效或已生效的计划时使用）。
func DecodeFetchPlanContent(raw string) (*FetchPlanContent, error) {
	return decodeFetchPlanContent(raw)
}

// DecodeFetchPlanProgress 解码采集计划进度。
func DecodeFetchPlanProgress(raw string) (*FetchPlanProgress, error) {
	return decodeFetchPlanProgress(raw)
}
