package domain

import (
	"time"

	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorname"
)

const (
	FactorTypeTimeSeries   = "timeseries"
	FactorTypeCrossSection = "cross_section"
	MemberStatusEnabled    = "enabled"
	MemberStatusDisabled   = "disabled"

	// 成员类型：固定成员的参数在加成员时写定；可变成员的参数由关联的策略逐期提交。
	MemberKindFixed = factorname.KindFixed
	MemberKindVar   = factorname.KindVar

	TaskStatusPending  = "pending"
	TaskStatusEnabled  = "enabled"
	TaskStatusDisabled = "disabled"
)

// DefaultCategoryID 是没有指定分类的因子所属的分类。
const DefaultCategoryID = "uncategorized"

// FactorTask 是一个因子计算任务：一个源数据集和频率，产出一个结果数据集。
// BoundInstanceID 是关联的策略实例：独占、永久，写入后不能修改；为空表示还没有关联。
// 任务的标的就是源数据集当前的标的，没有单独的对象范围。
type FactorTask struct {
	TaskID          string    `json:"task_id" gorm:"column:c_task_id;primaryKey"`
	SpaceID         string    `json:"space_id" gorm:"column:c_space_id"`
	Name            string    `json:"name" gorm:"column:c_name"`
	SourceDatasetID string    `json:"source_dataset_id" gorm:"column:c_source_dataset_id"`
	Freq            string    `json:"freq" gorm:"column:c_freq"`
	ResultDatasetID string    `json:"result_dataset_id" gorm:"column:c_result_dataset_id"`
	BoundInstanceID string    `json:"bound_instance_id" gorm:"column:c_bound_instance_id"`
	Status          string    `json:"status" gorm:"column:c_status"`
	CreatedAt       time.Time `json:"created_at" gorm:"column:c_ctime"`
	UpdatedAt       time.Time `json:"updated_at" gorm:"column:c_mtime"`
}

func (FactorTask) TableName() string { return "t_factor_tasks" }

// ComputeGroup 是任务所属的计算组：同一源数据集、同一频率的任务在引擎里一起计算。
func (s FactorTask) ComputeGroup() string { return s.SpaceID + "/" + s.SourceDatasetID + "/" + s.Freq }

// FactorCategory 是因子分类（例如动量类、波动类）：只是元数据，用于因子库查找与按类展示，不进 definition_hash。
type FactorCategory struct {
	CategoryID string    `json:"category_id" gorm:"column:c_category_id;primaryKey"`
	Name       string    `json:"name" gorm:"column:c_name"`
	SortOrder  int       `json:"sort_order" gorm:"column:c_sort_order"`
	CreatedAt  time.Time `json:"created_at" gorm:"column:c_ctime"`
	UpdatedAt  time.Time `json:"updated_at" gorm:"column:c_mtime"`
}

func (FactorCategory) TableName() string { return "t_factor_categories" }

// FactorDef describes an algorithm only; 它与因子任务无关，也没有运行状态.
// 持久化的只有 FactorID、Name、CategoryID、Spec、SpecHash、AllowPartialUniverse 与时间；FactorType、InputColumns、
// Outputs、LookbackPeriods 由编译 Spec 派生（CompileDefinition），不落库。
type FactorDef struct {
	FactorID             string    `json:"factor_id" gorm:"column:c_factor_id;primaryKey"`
	Name                 string    `json:"name" gorm:"column:c_name"`
	CategoryID           string    `json:"category_id" gorm:"column:c_category_id"`
	Spec                 string    `json:"spec" gorm:"column:c_spec"`
	SpecHash             string    `json:"spec_hash" gorm:"column:c_spec_hash"`
	AllowPartialUniverse bool      `json:"allow_partial_universe" gorm:"column:c_allow_partial_universe"`
	CreatedAt            time.Time `json:"created_at" gorm:"column:c_ctime"`
	UpdatedAt            time.Time `json:"updated_at" gorm:"column:c_mtime"`

	FactorType      string   `json:"factor_type,omitempty" gorm:"-"`
	InputColumns    []string `json:"input_columns,omitempty" gorm:"-"`
	Outputs         []string `json:"outputs,omitempty" gorm:"-"`
	LookbackPeriods int      `json:"lookback_periods,omitempty" gorm:"-"`
	// UDFs 是这份定义引用的自定义算子在本次编译所用的版本与元数据（名称 → 版本）：读取定义时按启用版本（或补算作业固化的绑定）
	// 解析，随目录快照与补算任务一起传到引擎。它不落库，也不进 spec_hash，但版本哈希会进规范文本与 definition_hash。
	UDFs        map[string]factorexpr.UDF `json:"-" gorm:"-"`
	compiled    *factorexpr.Factor
	compiledKey string
}

func (FactorDef) TableName() string { return "t_factor_defs" }

// FactorTaskMember 是定义在某个因子任务里的运行实例，身份是任务内唯一的成员键（因子 ID 加参数段，规则见 packages/factorname）。
// 固定成员的参数（Params）在加成员时写定；可变成员的 Params 是默认参数，策略逐期提交的值记在 LastSubmitted*。
// LastSubmitted* 是运行状态，不属于配置：目录同步、目录指纹都不看它们，提交时也不更新 UpdatedAt。
type FactorTaskMember struct {
	TaskID    string             `json:"task_id" gorm:"column:c_task_id;primaryKey"`
	MemberKey string             `json:"member_key" gorm:"column:c_member_key;primaryKey"`
	FactorID  string             `json:"factor_id" gorm:"column:c_factor_id"`
	Kind      string             `json:"kind" gorm:"column:c_kind"`
	Status    string             `json:"status" gorm:"column:c_status"`
	Params    map[string]float64 `json:"params,omitempty" gorm:"column:c_params;serializer:json"`
	// LastSubmittedParams 是各标的最近一次提交的参数（按标的合并后的结果）。
	LastSubmittedParams ParamSnapshot `json:"last_submitted_params,omitempty" gorm:"column:c_last_submitted_params;serializer:json"`
	// LastSubmittedPeriod 是最近一次被接受的提交的目标期（Unix 秒）；0 表示从未提交。
	LastSubmittedPeriod int64      `json:"last_submitted_period,omitempty" gorm:"column:c_last_submitted_period"`
	LastRequestID       string     `json:"last_request_id,omitempty" gorm:"column:c_last_request_id"`
	LastSubmittedAt     *time.Time `json:"last_submitted_at,omitempty" gorm:"column:c_last_submitted_at"`
	CreatedAt           time.Time  `json:"created_at" gorm:"column:c_ctime"`
	UpdatedAt           time.Time  `json:"updated_at" gorm:"column:c_mtime"`
}

func (FactorTaskMember) TableName() string { return "t_factor_task_members" }

// TaskMember 是任务一侧的成员视图：成员加上它的定义。
type TaskMember struct {
	FactorTaskMember
	Factor FactorDef
}

// Def 返回成员的计算视图。
func (m TaskMember) Def() MemberDef {
	return MemberDef{Key: m.MemberKey, Factor: m.Factor, Kind: m.Kind, Params: m.Params}
}

// AsMembers 把定义按“参数全取默认值的固定成员”转成成员，用于命令行工具和测试里不关心成员参数的场合。
// 定义没有编译或成员键推导失败时 panic：调用方给的是已校验过的定义。
func AsMembers(defs ...FactorDef) []MemberDef {
	members := make([]MemberDef, 0, len(defs))
	for _, def := range defs {
		key, err := DeriveMemberKey(def, MemberKindFixed, nil)
		if err != nil {
			panic(err)
		}
		members = append(members, MemberDef{Key: key, Factor: def, Kind: MemberKindFixed})
	}
	return members
}

// MemberDef 是成员在计算时的样子：成员键加定义。结果列名与程序里的内部身份都由它推出。
type MemberDef struct {
	Key    string    `json:"key"`
	Factor FactorDef `json:"factor"`
	// Kind 是成员类型：fixed 或 var。
	Kind string `json:"kind"`
	// Params 是成员的固定参数，或可变成员的默认参数（参数名 → 取值，加成员时给定的完整参数，之后不变）。
	Params map[string]float64 `json:"params,omitempty"`
}

// IsVar 报告成员是否是可变成员。
func (m MemberDef) IsVar() bool { return m.Kind == MemberKindVar }

// OutputColumns 返回成员在结果数据集里的全部输出列名，与 Factor.Outputs 一一对应。
// 成员键和输出名在加成员时已校验过；无法推导（只会出现在没有校验的测试数据里）时返回 nil。
func (m MemberDef) OutputColumns() []string {
	columns, err := factorname.Columns(m.Factor.FactorID, m.Key, m.Factor.Outputs)
	if err != nil {
		return nil
	}
	return columns
}

// ParamNames 返回成员定义声明的参数名（按声明顺序）；定义没有编译时为空。
func (m MemberDef) ParamNames() []string {
	compiled := m.Factor.Compiled()
	if compiled == nil {
		return nil
	}
	names := make([]string, len(compiled.Params))
	for i, param := range compiled.Params {
		names[i] = param.Name
	}
	return names
}

// ProgramKey 返回成员在计算组程序里的内部身份。
func (m MemberDef) ProgramKey(taskID string) string { return taskID + "/" + m.Key }

// FactorUsage 是引用某个定义的一个任务成员。
type FactorUsage struct {
	TaskID    string
	MemberKey string
	Status    string
	// Params 是成员的固定参数，或可变成员的默认参数。
	Params map[string]float64
}

// FactorInfo 是定义一侧的视图：定义和它被用在哪里。
type FactorInfo struct {
	Factor FactorDef
	Usages []FactorUsage
}
