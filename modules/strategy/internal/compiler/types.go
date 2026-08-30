package compiler

import (
	"context"

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

type CompiledStrategy struct {
	APIVersion     string                    `json:"api_version"`
	Kind           string                    `json:"kind"`
	SpaceID        string                    `json:"space_id"`
	SourceView     CompiledView              `json:"source_view"`
	InstrumentPool config.InstrumentPoolRule `json:"instrument_pool"`
	Schedule       CompiledSchedule          `json:"schedule"`
	Readiness      string                    `json:"readiness"`
	Factors        []CompiledFactor          `json:"factors"`
	Long           *config.Side              `json:"long,omitempty"`
	Short          *config.Side              `json:"short,omitempty"`
	Dependencies   DependenciesSnapshot      `json:"dependencies"`
	CompiledJSON   []byte                    `json:"-"`
	Hash           string                    `json:"-"`
}

type CompiledView struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Frequency string `json:"frequency"`
}

type CompiledSchedule struct {
	Every string `json:"every"`
}

type CompiledFactor struct {
	FactorID        string   `json:"factor_id"`
	SourceHash      string   `json:"source_hash,omitempty"`
	InputColumns    []string `json:"input_columns,omitempty"`
	ParamsJSON      string   `json:"params_json,omitempty"`
	LookbackPeriods int      `json:"lookback_periods,omitempty"`
	BindingID       string   `json:"binding_id"`
	Frequency       string   `json:"frequency"`
	ResultDatasetID string   `json:"result_dataset_id"`
	ResultViewID    string   `json:"result_view_id"`
	Output          string   `json:"output"`
	ColumnName      string   `json:"column_name"`
	SubjectMode     string   `json:"subject_mode,omitempty"`
	SubjectsJSON    string   `json:"subjects_json,omitempty"`
}

type DependenciesSnapshot struct {
	FactorResultViewIDs []string `json:"factor_result_view_ids"`
}

type Compiler struct {
	Factors FactorCatalog
	Storage StorageCatalog
}
