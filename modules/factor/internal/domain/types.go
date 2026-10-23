package domain

import (
	"time"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

const (
	FactorTypeTimeSeries   = "timeseries"
	FactorTypeCrossSection = "cross_section"
	MemberStatusEnabled    = "enabled"
	MemberStatusDisabled   = "disabled"

	SubjectModeAll     = "all"
	SubjectModeInclude = "include"

	SetStatusPending  = "pending"
	SetStatusEnabled  = "enabled"
	SetStatusDisabled = "disabled"
	SetStatusDeleting = "deleting"
)

// DefaultCategoryID 是没有指定分类的因子所属的分类。
const DefaultCategoryID = "uncategorized"

// DefaultCarryAlertPeriods 是成员连续沿用参数多少期后告警（因子动态调参执行计划 2.10）。
const DefaultCarryAlertPeriods = 10

// FactorSet groups member definitions over one source dataset and frequency.
// OwnerInstanceID 非空时是某个策略实例的私有集合：该实例是唯一的参数写入方；为空时没有写入方，成员永远用默认参数。
type FactorSet struct {
	SetID           string    `json:"set_id" gorm:"column:c_set_id;primaryKey"`
	SpaceID         string    `json:"space_id" gorm:"column:c_space_id"`
	SourceDatasetID string    `json:"source_dataset_id" gorm:"column:c_source_dataset_id"`
	Freq            string    `json:"freq" gorm:"column:c_freq"`
	SubjectMode     string    `json:"subject_mode" gorm:"column:c_subject_mode"`
	Subjects        []string  `json:"subjects" gorm:"column:c_subjects_json;serializer:json"`
	ResultDatasetID string    `json:"result_dataset_id" gorm:"column:c_result_dataset_id"`
	OwnerInstanceID string    `json:"owner_instance_id" gorm:"column:c_owner_instance_id"`
	Status          string    `json:"status" gorm:"column:c_status"`
	CreatedAt       time.Time `json:"created_at" gorm:"column:c_ctime"`
	UpdatedAt       time.Time `json:"updated_at" gorm:"column:c_mtime"`
}

func (FactorSet) TableName() string { return "t_factor_sets" }

// ComputeGroup 是集合所属的计算组：同一源数据集、同一频率的集合在引擎里一起计算。
func (s FactorSet) ComputeGroup() string { return s.SpaceID + "/" + s.SourceDatasetID + "/" + s.Freq }

// FactorCategory 是因子分类（例如动量类、波动类）：只是元数据，用于因子库查找与按类展示，不进 definition_hash。
type FactorCategory struct {
	CategoryID string    `json:"category_id" gorm:"column:c_category_id;primaryKey"`
	Name       string    `json:"name" gorm:"column:c_name"`
	SortOrder  int       `json:"sort_order" gorm:"column:c_sort_order"`
	CreatedAt  time.Time `json:"created_at" gorm:"column:c_ctime"`
	UpdatedAt  time.Time `json:"updated_at" gorm:"column:c_mtime"`
}

func (FactorCategory) TableName() string { return "t_factor_categories" }

// FactorDef describes an algorithm only; it has no set binding and no run state.
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
	compiled        *factorexpr.Factor
}

func (FactorDef) TableName() string { return "t_factor_defs" }

// FactorSetMember is a definition's runtime instance inside one factor set. 成员的身份是集合内唯一的别名：
// 同一个定义可以用不同别名加入同一集合（例如 fast、slow 各自调参）。
type FactorSetMember struct {
	SetID       string `json:"set_id" gorm:"column:c_set_id;primaryKey"`
	MemberAlias string `json:"member_alias" gorm:"column:c_member_alias;primaryKey"`
	FactorID    string `json:"factor_id" gorm:"column:c_factor_id"`
	Status      string `json:"status" gorm:"column:c_status"`
	// CarryAlertPeriods 是连续沿用参数多少期后告警。
	CarryAlertPeriods int       `json:"carry_alert_periods" gorm:"column:c_carry_alert_periods"`
	CreatedAt         time.Time `json:"created_at" gorm:"column:c_ctime"`
	UpdatedAt         time.Time `json:"updated_at" gorm:"column:c_mtime"`
}

func (FactorSetMember) TableName() string { return "t_factor_set_members" }

// SetMember is the set-side view: a member together with its definition.
type SetMember struct {
	FactorSetMember
	Factor FactorDef
}

// Def 返回成员的计算视图。
func (m SetMember) Def() MemberDef { return MemberDef{Alias: m.MemberAlias, Factor: m.Factor} }

// AsMembers 把定义按“别名即定义 ID”转成成员，用于命令行工具和测试里不需要别名的场合。
func AsMembers(defs ...FactorDef) []MemberDef {
	members := make([]MemberDef, 0, len(defs))
	for _, def := range defs {
		members = append(members, MemberDef{Alias: def.FactorID, Factor: def})
	}
	return members
}

// MemberDef 是成员在计算时的样子：别名加定义。结果列名、参数列名与程序里的内部身份都由它推出。
type MemberDef struct {
	Alias  string    `json:"alias"`
	Factor FactorDef `json:"factor"`
}

// OutputColumn 返回输出 output 在结果数据集里的列名：别名与定义 ID 相同时就是输出名，否则是“别名_输出名”。
func (m MemberDef) OutputColumn(output string) string {
	if m.Alias == m.Factor.FactorID {
		return output
	}
	return m.Alias + "_" + output
}

// OutputColumns 返回全部输出列名，与 Factor.Outputs 一一对应。
func (m MemberDef) OutputColumns() []string {
	columns := make([]string, len(m.Factor.Outputs))
	for i, output := range m.Factor.Outputs {
		columns[i] = m.OutputColumn(output)
	}
	return columns
}

// ParamColumn 返回参数 name 的实际取值在结果数据集里的列名：“别名__参数名”，成员的全部输出共用。
func (m MemberDef) ParamColumn(name string) string { return m.Alias + "__" + name }

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

// ParamColumns 返回全部参数列名，与 ParamNames 一一对应。
func (m MemberDef) ParamColumns() []string {
	names := m.ParamNames()
	columns := make([]string, len(names))
	for i, name := range names {
		columns[i] = m.ParamColumn(name)
	}
	return columns
}

// Key 返回成员在计算组程序里的内部身份。
func (m MemberDef) Key(setID string) string { return setID + "/" + m.Alias }

// FactorUsage reports one set member that references a definition.
type FactorUsage struct {
	SetID       string
	MemberAlias string
	Status      string
}

// FactorInfo is the definition-side view: a definition and where it is used.
type FactorInfo struct {
	Factor FactorDef
	Usages []FactorUsage
}
