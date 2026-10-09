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

// SkipError 表示本期不能求值但不是基础设施问题：记录为 skipped 并 ACK。
type SkipError struct {
	Reason string
	Detail string
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
	// SeriesBars 是 View 每个序列至少保留的最近根数；0 表示未知。活跃序列可回溯的起点见 CoverageStart。
	SeriesBars int
	Columns    []ViewColumn
}

// ViewColumn 是 View 的一列及其属性（因子列带 origin_factor_id 与 factor_output）。
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
}

// TagInfo 是标签元数据；MarketType 是标签唯一且不可修改的市场类型（spot | swap）。
type TagInfo struct {
	TagID      string
	MarketType string
}

// Subject 是数据集绑定的一个标的；策略的标的 ID 就是 subject_id。
type Subject struct {
	SubjectID  string
	SeriesTag  string
	Active     bool
	Attributes map[string]string
}

// FactorInfo 是因子定义中策略关心的部分。
type FactorInfo struct {
	FactorID       string
	DefinitionHash string
	Outputs        []string
}

// Row 是 View 的一行：数值列已转为 float64，无法解析的列不出现。
type Row struct {
	SubjectID string
	SeriesTag string
	DataTime  time.Time
	Values    map[string]float64
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

// ColumnBinding 是一个被引用列的来源。
type ColumnBinding struct {
	Source         string `json:"source"`
	FactorID       string `json:"factor_id,omitempty"`
	FactorOutput   string `json:"factor_output,omitempty"`
	DefinitionHash string `json:"definition_hash,omitempty"`
}

// 列来源。
const (
	SourceDataset = "dataset"
	SourceFactor  = "factor"
)

// Resolved 是启用时固化的绑定解析，保存在实例与会话的 resolved_json 中。
type Resolved struct {
	ViewID          string                   `json:"view_id"`
	DatasetID       string                   `json:"dataset_id"`
	SourceDatasetID string                   `json:"source_dataset_id,omitempty"`
	Bar             string                   `json:"bar"`
	Calendar        string                   `json:"calendar"`
	MarketType      string                   `json:"market_type,omitempty"`
	Spot            bool                     `json:"spot"`
	Columns         map[string]ColumnBinding `json:"columns"`
	Factors         map[string]string        `json:"factors,omitempty"`
	UsesPreviousBar bool                     `json:"uses_previous_bar"`
	// PreviousFactors 是经 bars[-1] 读取的因子列所属的因子（排序）：只有它们需要上一根的版本证据。
	PreviousFactors []string `json:"previous_factors,omitempty"`
	// CompletionKind 是触发本实例的完成事件类型：因子结果 View 为 factor_period.computed，K 线 View 为 collector.period.completed。
	CompletionKind string   `json:"completion_kind,omitempty"`
	MinAgeBars     int      `json:"min_age_bars,omitempty"`
	ViewColumns    []string `json:"view_columns"`
}

// FactorIDs 返回引用的因子 ID（排序）。
func (r Resolved) FactorIDs() []string {
	return sortedKeys(r.Factors)
}

// ColumnsOfFactor 返回由某个因子产出的被引用列。
func (r Resolved) ColumnsOfFactor(factorID string) []string {
	columns := make([]string, 0)
	for name, binding := range r.Columns {
		if binding.FactorID == factorID {
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
