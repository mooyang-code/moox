// Package upstream 负责 Strategy 访问 Storage、FactorMgr 与因子引擎（EngineControl）的上游 RPC 客户端与协议数据结构。
package upstream

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrStale 表示读取过程中 View 的活动索引或修订号发生变化，已读页面作废，需要整体重读。
var ErrStale = errors.New("View 输入快照已过期")

// ErrViewNotFound 表示数据集已不存在（被删除），它的 View 随之不存在。
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

// NewStaleError 构造一个匹配 ErrStale 的中文错误，原始错误经 Unwrap 留给日志。
func NewStaleError(message string, raw error) error {
	return &staleError{message: message, raw: raw}
}

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

// ViewInfo 是一个数据集的查询索引（View）的状态：数据集的频率与状态、活动索引与列。每个数据集恰好有一个 View。
type ViewInfo struct {
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

// ViewColumn 是 View 的一列（数据集里启用的列）及其属性（因子输出列带 column_kind=output、member_key、member_kind、factor_id 等）。
type ViewColumn struct {
	Name       string
	Attributes map[string]string
}

// DatasetInfo 是数据集元数据。SubjectTags 是数据集的标的范围（标签 ID）。
type DatasetInfo struct {
	DatasetID string
	Status    string
	Frequency string
	Retention string
	// Alignment 是数据集的周期对齐方式（epoch_utc 或 session）。
	Alignment   string
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
	// Suspended 表示标的不活跃只是因为停牌（成员仍在标签里、交易状态是 suspended）：已持有的保持持仓（见 frame.go），
	// 没持有的不进全集。delisted 或被移出标签的不算。
	Suspended bool
}

// TagMember 是标签的一个活跃成员；TradingStatus 是成员在这个标签（市场）上的交易状态，空值按 trading 处理。
type TagMember struct {
	SubjectID     string
	TradingStatus string
}

// 策略要区分的成员交易状态（与 Storage 的 t_subject_tags.c_trading_status 一致；其余值含空值都按 trading 处理）。
const (
	TradingStatusSuspended = "suspended"
	TradingStatusDelisted  = "delisted"
)

// MemberIDs 返回成员的标的 ID（顺序不变），给只按标签取成员的集合划分用。
func MemberIDs(members []TagMember) []string {
	ids := make([]string, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.SubjectID)
	}
	return ids
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
	// GetView 读取数据集的 View：数据集元数据、活动索引与启用的列，不含覆盖范围；需要覆盖范围时另调 ViewCoverage（见 WithCoverage）。
	GetView(ctx context.Context, spaceID, datasetID string) (ViewInfo, error)
	// ViewGeneration 只读取 View 活动索引的代次（不读列），用于确认读取期间索引没有换代。
	ViewGeneration(ctx context.Context, spaceID, datasetID string) (string, error)
	// ViewCoverage 读取 View 活动索引的覆盖统计；exact 为 true 时统计未缓存也要求现算（代价是一次全量统计）。
	ViewCoverage(ctx context.Context, spaceID string, view ViewInfo, exact bool) (Coverage, error)
	GetDataset(ctx context.Context, spaceID, datasetID string) (DatasetInfo, error)
	ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]Subject, error)
	// ListTagMembers 返回标签的活跃成员（按标的 ID 排序），带出成员的交易状态，不按交易状态过滤。
	ListTagMembers(ctx context.Context, spaceID, tagID string) ([]TagMember, error)
	GetTag(ctx context.Context, spaceID, tagID string) (TagInfo, error)
	// QueryRows 读取全部页面并返回服务端修订号；索引或修订号变化返回 ErrStale。
	QueryRows(ctx context.Context, spaceID string, query Query) ([]Row, uint64, error)
	GetFactor(ctx context.Context, factorID string) (FactorInfo, error)
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
