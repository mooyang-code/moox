// Package input 负责从一个 View 装配引擎输入：启用时解析绑定，每期划分标的集合并读取一帧数据。
package input

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// ErrStale 表示读取过程中 View 的活动索引或修订号发生变化，已读页面作废，需要整体重读。
var ErrStale = errors.New("View 输入快照已过期")

// ErrViewNotFound 表示 View 已不存在（被删除）。
var ErrViewNotFound = errors.New("View 不存在")

// staleError 是读取期间活动索引或修订号变化的错误：Error 只给中文说明，Storage 的原文经 Unwrap 留给日志；
// errors.Is(err, ErrStale) 成立。
type staleError struct {
	message string
	raw     error
}

func (e *staleError) Error() string        { return e.message }
func (e *staleError) Is(target error) bool { return target == ErrStale }
func (e *staleError) Unwrap() error        { return e.raw }

// describedError 是换成中文说明的错误：Error 只给说明，原错误（可能是英文）经 Unwrap 留给日志与 errors.Is 判断。
type describedError struct {
	message string
	cause   error
}

func (e *describedError) Error() string { return e.message }
func (e *describedError) Unwrap() error { return e.cause }

// SkipError 表示本期不能求值但不是基础设施问题：记录为 skipped 并 ACK。Cause 是服务端的原文（可能是英文），
// 只经 Unwrap 留给日志，不进入说明。
type SkipError struct {
	Reason string
	Detail string
	Cause  error
}

func (e *SkipError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Error 只返回说明：同一个错误也会出现在启用与回放的报错里，那里没有“本期”的语境。
func (e *SkipError) Error() string {
	if e == nil {
		return "本期跳过"
	}
	if e.Detail == "" {
		return fmt.Sprintf("本期跳过（%s）", e.Reason)
	}
	return e.Detail
}

// 跳过原因。
const (
	SkipConfigError     = "config_error"
	SkipAmbiguousSeries = "ambiguous_series"
	// SkipMarketFailed 表示多市场策略里选币市场、或所有市场本期整体失败（某个因子在市场里失败的标的占比超过 max_missing）：
	// 没有可用的市场。属于数据类原因，不计实例故障；它不是“数据还在积累”，持续出现要走数据集新鲜度告警，不归入积累中。
	SkipMarketFailed = "market_failed"
	// SkipHistoryInsufficient 表示 View 当前覆盖的历史不足以判断 min_age_bars，不能把标的当作新上市剔除。
	SkipHistoryInsufficient = "history_insufficient"
)

// ViewInfo 是 View 的元数据。
type ViewInfo struct {
	ViewID        string
	DatasetID     string
	Frequency     string
	Status        string
	ActiveIndexID string
	// Generation 是索引的代次：索引 ID 加上 Storage 建成时记录的代次。索引 ID 对一个 View 是固定的，代次相同才说明
	// 是同一代索引（行只会被定时删除最旧数据删掉）；没有代次记录时退化为索引 ID。
	Generation string
	// IndexedFrom 与 IndexedTo 是活动索引全部行的最早与最晚 bar_start（取自 DataView 的索引统计，至多每 5 分钟
	// 刷新，最晚一根可能略滞后；最早时间只会提前，可能来自已停更的序列）；只有经 WithCoverage 读取后才有值，零值表示未知。
	IndexedFrom time.Time
	IndexedTo   time.Time
	// SeriesBars 是 View 每个序列至少保留的最近根数；0 表示未知。活跃序列可回溯的起点见 CoverageStartAt。
	SeriesBars int
	Columns    []ViewColumn
}

// ViewColumn 是 View 的一列及其属性（因子输出列带 column_kind=output、member_key、member_kind、factor_id 等）。
type ViewColumn struct {
	Name       string
	Attributes map[string]string
}

// DatasetInfo 是数据集元数据。SubjectTags 是数据集的标的范围（标签 ID）。
type DatasetInfo struct {
	DatasetID   string
	Status      string
	Frequency   string
	Retention   string
	Attributes  map[string]string
	SubjectTags []string
	// SubjectsRevision 是所属空间的标的修订号，标的、标签成员或数据集标的范围变化时递增。
	SubjectsRevision uint64
}

// TagInfo 是标签元数据；MarketType 是标签唯一且不可修改的市场类型（spot | swap | equity）。
type TagInfo struct {
	MarketType string
}

// Subject 是数据集绑定的一个标的；策略的标的 ID 就是 subject_id。
type Subject struct {
	SubjectID string
	SeriesTag string
	Active    bool
}

// FactorInfo 是因子定义中策略关心的部分。
type FactorInfo struct {
	FactorID       string
	DefinitionHash string
	// Params 是定义声明的可调参数。
	Params []ParamDecl
}

// Row 是 View 的一行：数值列已转为 float64，无法解析的列不出现。Params 是 __params 列解析出的
// 各成员这一行实际用的参数（成员键 → 参数名 → 值），查询没有读 __params 或这一行没有值时为空。
type Row struct {
	SubjectID string
	SeriesTag string
	DataTime  time.Time
	Values    map[string]float64
	Params    map[string]map[string]float64
}

// Query 是一次固定索引的分页读取。
type Query struct {
	ViewID           string
	DatasetID        string
	Frequency        string
	Subjects         []Subject
	Start            time.Time
	End              time.Time
	Columns          []string
	ExpectedIndexID  string
	ExpectedRevision uint64
	// Limit 大于 0 时只读第一页、最多 Limit 行（用于存在性检查）。
	Limit int
}

// Coverage 是 View 活动索引的覆盖统计：全部行的最早与最晚 bar_start、每个序列至少保留的根数；零值表示未知。
type Coverage struct {
	IndexedFrom time.Time
	IndexedTo   time.Time
	SeriesBars  int
}

// Client 是 Storage 与 Factor 的窄适配。
type Client interface {
	// GetView 读取 View 元数据与列，不含覆盖范围；需要覆盖范围时另调 ViewCoverage（见 WithCoverage）。
	GetView(ctx context.Context, spaceID, viewID string) (ViewInfo, error)
	// ViewGeneration 只读取 View 活动索引的代次（不读列），用于确认读取期间索引没有换代。
	ViewGeneration(ctx context.Context, spaceID, viewID string) (string, error)
	// ViewCoverage 读取 View 活动索引的覆盖统计；exact 为 true 时统计未缓存也要求现算（代价是一次全量统计）。
	ViewCoverage(ctx context.Context, spaceID string, view ViewInfo, exact bool) (Coverage, error)
	GetDataset(ctx context.Context, spaceID, datasetID string) (DatasetInfo, error)
	ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]Subject, error)
	ListTagMembers(ctx context.Context, spaceID, tagID string) ([]string, error)
	GetTag(ctx context.Context, spaceID, tagID string) (TagInfo, error)
	// QueryRows 读取全部页面并返回服务端修订号；索引或修订号变化返回 ErrStale。
	QueryRows(ctx context.Context, spaceID string, query Query) ([]Row, uint64, error)
	GetFactor(ctx context.Context, factorID string) (FactorInfo, error)
}

// ColumnBinding 是一个被引用列的来源。因子列以成员键标识（同一因子可以用不同参数出现多次），FactorID 是它引用的定义。
type ColumnBinding struct {
	Source     string `json:"source"`
	Member     string `json:"member,omitempty"`
	FactorID   string `json:"factor_id,omitempty"`
	OutputName string `json:"output_name,omitempty"`
	// MemberKind 是成员类型：fixed 或 var。
	MemberKind string `json:"member_kind,omitempty"`
	// DefinitionHash 是启用时成员的定义哈希（成员契约由它和成员类型组成，见 readiness.Contract）。
	DefinitionHash string `json:"definition_hash,omitempty"`
}

// 列来源。
const (
	SourceDataset = "dataset"
	SourceFactor  = "factor"
)

// TaskBinding 是一个任务键绑定的因子任务及其结果 View。
type TaskBinding struct {
	// ViewID 是因子任务的结果 View（由任务 ID 推导；事件路由与求值都用它），FactorTaskID 是因子任务。
	ViewID          string `json:"view_id"`
	FactorTaskID    string `json:"factor_task_id"`
	DatasetID       string `json:"dataset_id"`
	SourceDatasetID string `json:"source_dataset_id,omitempty"`
}

// Resolved 是启用时固化的绑定解析，保存在实例与会话的 resolved_json 中。
type Resolved struct {
	// Market 是有效的市场模式：DSL 写出的，或没写时按唯一绑定的因子任务的市场取的默认值。
	Market dsl.Market `json:"market"`
	// Tasks 是绑定的因子任务与结果 View。方式 A 的键是市场（spot、swap 或 equity），键集合恰好是 Market.Markets()；
	// 方式 B（统一数据集）只有一个键 multi。
	Tasks map[string]TaskBinding `json:"tasks"`
	// FactorParams 是任务各可变成员（成员键）的因子定义声明的可调参数：启用时固化，参数输出据此补齐默认值、取整与限幅。
	FactorParams map[string][]ParamDecl   `json:"factor_params,omitempty"`
	Bar          string                   `json:"bar"`
	Calendar     string                   `json:"calendar"`
	Columns      map[string]ColumnBinding `json:"columns"`
	// Factors 是引用的因子成员（成员键）及其固化的成员契约（readiness.Contract：定义哈希加成员类型）；
	// 绑定了多个任务时，每个任务里同名成员的契约必须一致。
	Factors map[string]string `json:"factors,omitempty"`
	// PreviousFactors 是经 bars[-1] 读取的因子列所属的成员（排序）：只有它们需要上一根的版本证据。由编译结果派生
	// （Resolve 与 Compile 填入），不进快照。
	PreviousFactors []string `json:"-"`
	MinAgeBars      int      `json:"min_age_bars,omitempty"`
	// ViewColumns 是各 View 共有的列（排序）：DSL 引用的列必须在每个绑定的 View 里都存在。
	ViewColumns []string `json:"view_columns"`
}

// MarketMulti 是统一数据集上的因子任务登记的“市场”：一个任务同时覆盖现货与永续，不是真实的市场，也不会出现在 Frame 里。
const MarketMulti = seriesset.MarketMulti

// Unified 报告实例绑定的是统一数据集（方式 B）上的一个因子任务：同一个 View 里现货、永续用 series_tag 区分。
func (r Resolved) Unified() bool {
	_, ok := r.Tasks[MarketMulti]
	return ok
}

// TaskKeys 返回绑定的任务键（现货、永续、股票、统一数据集的先后）：方式 A 的键是市场，方式 B 只有一个键 multi。
// 一个键对应一个因子任务、一个结果 View。
func (r Resolved) TaskKeys() []string {
	keys := make([]string, 0, len(r.Tasks))
	for _, key := range []string{dsl.MarketSpot, dsl.MarketSwap, dsl.MarketEquity, MarketMulti} {
		if _, ok := r.Tasks[key]; ok {
			keys = append(keys, key)
		}
	}
	return keys
}

// Markets 返回策略涉及的市场（现货在前、永续其次）：Frame 按市场分别带行。方式 A 里每个市场有自己的任务；
// 方式 B 里它们共用统一数据集的那一个 View。
func (r Resolved) Markets() []string {
	if r.Unified() {
		return r.Market.Markets()
	}
	markets := make([]string, 0, len(r.Tasks))
	for _, market := range r.Market.Markets() {
		if _, ok := r.Tasks[market]; ok {
			markets = append(markets, market)
		}
	}
	return markets
}

// TaskKey 返回某个市场的行所在的任务键：方式 A 就是市场自己，方式 B 是 multi。
func (r Resolved) TaskKey(market string) string {
	if r.Unified() {
		return MarketMulti
	}
	return market
}

// TaskOf 返回某个市场的行所在的任务绑定。
func (r Resolved) TaskOf(market string) TaskBinding { return r.Tasks[r.TaskKey(market)] }

// Primary 返回第一个任务的绑定：只绑一个任务时就是唯一的绑定（回放、试算展示等只读一个 View 的地方使用）。
func (r Resolved) Primary() TaskBinding {
	for _, key := range r.TaskKeys() {
		return r.Tasks[key]
	}
	return TaskBinding{}
}

// ViewIDs 返回绑定的结果 View（按任务键先后）。
func (r Resolved) ViewIDs() []string {
	ids := make([]string, 0, len(r.Tasks))
	for _, key := range r.TaskKeys() {
		ids = append(ids, r.Tasks[key].ViewID)
	}
	return ids
}

// SpotOnly 报告策略只输出现货或股票：闭环回放目前只支持这种策略（研究回放已支持永续与混合市场）。
func (r Resolved) SpotOnly() bool { return r.Market.Single() && dsl.SpotLike(r.Market.Select) }

// TaskKeyOfView 返回 View 所属的任务键。
func (r Resolved) TaskKeyOfView(viewID string) (string, bool) {
	for key, task := range r.Tasks {
		if task.ViewID == viewID {
			return key, true
		}
	}
	return "", false
}

// ColumnsOfFactor 返回由某个因子成员（成员键）产出的被引用列。
func (r Resolved) ColumnsOfFactor(member string) []string {
	columns := make([]string, 0)
	for name, binding := range r.Columns {
		if binding.Source == SourceFactor && binding.Member == member {
			columns = append(columns, name)
		}
	}
	sortStrings(columns)
	return columns
}

// Sets 是一期的标的集合划分：基础集合 U、各规则的预期集合与年龄剔除集合。
type Sets struct {
	Universe []string
	Expected map[string][]string
	AgedOut  map[string][]string
	Subjects map[string]Subject
	// Notes 记录装配过程中的说明（例如事件未携带名单）。
	Notes []string
}

// Instruments 返回 ∪E(r) 中未被年龄剔除的标的（排序）。
func (s Sets) Instruments() []string {
	aged := make(map[string]struct{})
	for _, ids := range s.AgedOut {
		for _, id := range ids {
			aged[id] = struct{}{}
		}
	}
	set := make(map[string]struct{})
	for _, ids := range s.Expected {
		for _, id := range ids {
			if _, out := aged[id]; !out {
				set[id] = struct{}{}
			}
		}
	}
	return sortedKeys(set)
}
