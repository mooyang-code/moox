package compiler

import (
	"context"
	"reflect"

	"github.com/expr-lang/expr/vm"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
)

type FactorDescriptor struct {
	ID              string
	Status          string
	SourceHash      string
	InputColumns    []string
	ParamsJSON      string
	LookbackPeriods int
	Outputs         []string
}

type BindingDescriptor struct {
	ID              string
	FactorID        string
	SpaceID         string
	SourceViewID    string
	Frequency       string
	Status          string
	ResultDatasetID string
	ResultViewID    string
	SubjectMode     string
	SubjectsJSON    string
}

type ViewDescriptor struct {
	ID           string
	Status       string
	SourceViewID string
	Frequency    string
}

type ViewColumn struct {
	Name       string
	Attributes map[string]string
}

type FactorCatalog interface {
	GetFactor(context.Context, string) (FactorDescriptor, error)
	ListBindings(context.Context, string) ([]BindingDescriptor, error)
}

type StorageCatalog interface {
	GetView(context.Context, string) (ViewDescriptor, error)
	ListViewColumns(context.Context, string) ([]ViewColumn, error)
}

type Dependencies interface {
	FactorCatalog
	StorageCatalog
}

// ExpressionStage controls the type and function contract for an expression.
// pct_rank and zscore are deliberately available only in StageScore.
type ExpressionStage string

const (
	StageFilterBefore ExpressionStage = "filter_before"
	StageScore        ExpressionStage = "score"
	StageSelectWhere  ExpressionStage = "select.where"
	StageSignalEntry  ExpressionStage = "signals.entry"
	StageSignalExit   ExpressionStage = "signals.exit"
	StageFilterAfter  ExpressionStage = "filter_after"
)

type ExpressionDependencies struct {
	Fields    []string
	Bars      map[int][]string
	UsesScore bool
}

type CompiledExpression struct {
	Source       string
	Stage        ExpressionStage
	Program      *vm.Program
	Dependencies ExpressionDependencies
}

type CompiledRule struct {
	Name         string
	Definition   config.Rule
	FilterBefore *CompiledExpression
	Score        *CompiledExpression
	SelectWhere  *CompiledExpression
	SignalEntry  *CompiledExpression
	SignalExit   *CompiledExpression
	FilterAfter  *CompiledExpression
}

// CompiledStrategy is an in-memory artifact.  It intentionally has no JSON or
// hash field: DSL text is persisted by the strategy definition, and programs
// are rebuilt when an instance is enabled or the process restarts.
type CompiledStrategy struct {
	Name         string
	SpaceID      string
	Data         config.Data
	Triggers     config.Triggers
	Rules        []CompiledRule
	Factors      []CompiledFactor
	Dependencies DependenciesSnapshot
	InputFields  map[string]reflect.Type
	// Deprecated fields below are compile adapters for older in-tree callers.
	// New code must use Name/Data/Triggers/Rules and rebuild programs from DSL.
	APIVersion     string                    `json:"api_version,omitempty"`
	Kind           string                    `json:"kind,omitempty"`
	CompiledJSON   []byte                    `json:"-"`
	SourceView     CompiledView              `json:"source_view,omitempty"`
	InstrumentPool config.InstrumentPoolRule `json:"instrument_pool,omitempty"`
	Schedule       CompiledSchedule          `json:"schedule,omitempty"`
	Readiness      string                    `json:"readiness,omitempty"`
	Long           *config.Side              `json:"long,omitempty"`
	Short          *config.Side              `json:"short,omitempty"`
}

type CompiledView struct {
	ID        string `json:"id,omitempty"`
	Status    string `json:"status,omitempty"`
	Frequency string `json:"frequency,omitempty"`
}
type CompiledSchedule struct {
	Every string `json:"every,omitempty"`
}

type CompiledFactor struct {
	FactorID        string
	SourceHash      string
	InputColumns    []string
	ParamsJSON      string
	LookbackPeriods int
	BindingID       string
	Frequency       string
	ResultDatasetID string
	ResultViewID    string
	Output          string
	ColumnName      string
	SubjectMode     string
	SubjectsJSON    string
}

type DependenciesSnapshot struct {
	FactorResultViewIDs []string
}

type Compiler struct {
	Factors     FactorCatalog
	Storage     StorageCatalog
	InputFields map[string]reflect.Type
}
