// Package input 负责从数据集的查询索引（View）装配引擎输入：启用时解析绑定，每期划分标的集合并读取一帧数据。
package input

import (
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/upstream"
)

// describedError 是换成中文说明的错误：Error 只给说明，原错误（可能是英文）经 Unwrap 留给日志与 errors.Is 判断。
type describedError struct {
	message string
	cause   error
}

func (e *describedError) Error() string { return e.message }
func (e *describedError) Unwrap() error { return e.cause }

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

// TaskBinding 是一个任务键绑定的因子任务及其结果数据集。
type TaskBinding struct {
	// FactorTaskID 是因子任务；DatasetID 是它的结果数据集（由任务 ID 推导；事件路由与求值都用它）。
	FactorTaskID    string `json:"factor_task_id"`
	DatasetID       string `json:"dataset_id"`
	SourceDatasetID string `json:"source_dataset_id,omitempty"`
}

// Resolved 是启用时固化的绑定解析，保存在实例与会话的 resolved_json 中。
type Resolved struct {
	// Pool 为真表示这是一个策略池的绑定解析。
	Pool bool `json:"pool,omitempty"`
	// Market 是有效的市场模式：DSL 写出的，或没写时按唯一绑定的因子任务的市场取的默认值。
	Market dsl.Market `json:"market"`
	// Tasks 是绑定的因子任务与结果数据集：键是市场（spot、swap 或 equity），键集合恰好是 Market.Markets()。
	Tasks map[string]TaskBinding `json:"tasks"`
	// FactorParams 是任务各可变成员（成员键）的因子定义声明的可调参数：启用时固化，参数输出据此补齐默认值、取整与限幅。
	FactorParams map[string][]upstream.ParamDecl `json:"factor_params,omitempty"`
	Bar          string                          `json:"bar"`
	Calendar     string                          `json:"calendar"`
	Columns      map[string]ColumnBinding        `json:"columns"`
	// Factors 是引用的因子成员（成员键）及其固化的成员契约（readiness.Contract：定义哈希加成员类型）；
	// 绑定了多个任务时，每个任务里同名成员的契约必须一致。
	Factors map[string]string `json:"factors,omitempty"`
	// PreviousFactors 是经 bars[-1] 读取的因子列所属的成员（排序）：只有它们需要上一根的版本证据。由编译结果派生
	// （Resolve 与 Compile 填入），不进快照。
	PreviousFactors []string `json:"-"`
	// MinAgeBars 是策略级 universe.min_age_bars：没写规则级年龄字段的规则按它判断，双市场按市场剔除行也只按它（见 age.go）。
	MinAgeBars int `json:"min_age_bars,omitempty"`
	// RuleAges 是各规则的年龄要求；只在有规则的要求不同于“策略级 min_age_bars、按下单市场判断”时填写（规则级年龄字段、max_age_bars、
	// age_markets: all、规则级 market），否则为空，各规则按 MinAgeBars。
	RuleAges map[string]AgeRule `json:"rule_ages,omitempty"`
	// ViewColumns 是各 View 共有的列（排序）：DSL 引用的列必须在每个绑定的 View 里都存在。
	ViewColumns []string `json:"view_columns"`
	// References 是参考市场（市场 → 任务）：只用于 age_markets: all 的年龄判定，不在 Tasks 里、不读行、不参与选择与下单（见 sources.go）。
	References map[string]TaskBinding `json:"references,omitempty"`
	// Sources 是附加列来源（来源名 → 绑定，见 sources.go）。
	Sources map[string]SourceBinding `json:"sources,omitempty"`
}

// TaskKeys 返回主帧绑定的任务键（现货、永续在前，其余按名字）：键就是市场，一个键对应一个因子任务、一个结果数据集；
// 附加来源与参考市场不在其中（见 AllTasks）。
func (r Resolved) TaskKeys() []string {
	keys := sortedKeys(r.Tasks)
	sort.SliceStable(keys, func(i, j int) bool { return marketOrder(keys[i]) < marketOrder(keys[j]) })
	return keys
}

// Markets 返回策略涉及的市场（现货在前、永续其次）：Frame 按市场分别带行，每个市场有自己的任务。
func (r Resolved) Markets() []string {
	markets := make([]string, 0, len(r.Tasks))
	for _, market := range r.Market.Markets() {
		if _, ok := r.Tasks[market]; ok {
			markets = append(markets, market)
		}
	}
	return markets
}

// TaskOf 返回某个市场的任务绑定。
func (r Resolved) TaskOf(market string) TaskBinding { return r.Tasks[market] }

// Primary 返回第一个任务的绑定：只绑一个任务时就是唯一的绑定（回放、试算展示等只读一个 View 的地方使用）。
func (r Resolved) Primary() TaskBinding {
	for _, key := range r.TaskKeys() {
		return r.Tasks[key]
	}
	return TaskBinding{}
}

// DatasetIDs 返回绑定的结果数据集（按任务键先后）。
func (r Resolved) DatasetIDs() []string {
	ids := make([]string, 0, len(r.Tasks))
	for _, key := range r.TaskKeys() {
		ids = append(ids, r.Tasks[key].DatasetID)
	}
	return ids
}

// TaskKeyOfDataset 返回结果数据集所属的任务键。
func (r Resolved) TaskKeyOfDataset(datasetID string) (string, bool) {
	for key, task := range r.Tasks {
		if task.DatasetID == datasetID {
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
	// AgedOutReasons 是 AgedOut 里不按 min_age_bars 剔除的标的的明细原因（见 engine.Frame.AgedOutReasons）。
	AgedOutReasons map[string]map[string]string
	Subjects       map[string]upstream.Subject
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
