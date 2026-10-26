// Package input 负责从一个 View 装配引擎输入：启用时解析绑定，每期划分标的集合并读取一帧数据。
package input

import (
	"context"
	"errors"
	"fmt"
	"time"
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
	// Generation 是活动索引的代次：活动索引 ID 加上激活它的构建 ID（Storage 在激活时记录）。A/B 槽位名在重建时
	// 交替复用，代次相同才说明是同一代索引（只追加、不删除行）；没有构建 ID 时退化为活动索引 ID。
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

// TagInfo 是标签元数据；MarketType 是标签唯一且不可修改的市场类型（spot | swap）。
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

// Resolved 是启用时固化的绑定解析，保存在实例与会话的 resolved_json 中。
type Resolved struct {
	// ViewID 是关联因子任务的结果 View（由任务 ID 推导；事件路由与求值都用它），FactorTaskID 是关联的因子任务。
	ViewID       string `json:"view_id"`
	FactorTaskID string `json:"factor_task_id"`
	// FactorParams 是任务各可变成员（成员键）的因子定义声明的可调参数：启用时固化，参数输出据此补齐默认值、取整与限幅。
	FactorParams    map[string][]ParamDecl   `json:"factor_params,omitempty"`
	DatasetID       string                   `json:"dataset_id"`
	SourceDatasetID string                   `json:"source_dataset_id,omitempty"`
	Bar             string                   `json:"bar"`
	Calendar        string                   `json:"calendar"`
	MarketType      string                   `json:"market_type,omitempty"`
	Spot            bool                     `json:"spot"`
	Columns         map[string]ColumnBinding `json:"columns"`
	// Factors 是引用的因子成员（成员键）及其固化的成员契约（readiness.Contract：定义哈希加成员类型）。
	Factors map[string]string `json:"factors,omitempty"`
	// PreviousFactors 是经 bars[-1] 读取的因子列所属的成员（排序）：只有它们需要上一根的版本证据。由编译结果派生
	// （Resolve 与 Compile 填入），不进快照。
	PreviousFactors []string `json:"-"`
	// CompletionKind 是触发本实例的完成事件类型：因子结果 View 为 factor_period.computed，K 线 View 为 collector.period.completed。
	CompletionKind string   `json:"completion_kind"`
	MinAgeBars     int      `json:"min_age_bars,omitempty"`
	ViewColumns    []string `json:"view_columns"`
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
