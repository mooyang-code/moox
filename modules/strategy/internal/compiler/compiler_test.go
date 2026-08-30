package compiler

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/stretchr/testify/require"
)

type fakeCatalog struct {
	factor   FactorDescriptor
	factors  map[string]FactorDescriptor
	binding  BindingDescriptor
	bindings []BindingDescriptor
	view     map[string]ViewDescriptor
	columns  map[string][]ViewColumn
}

func (f *fakeCatalog) GetFactor(_ context.Context, id string) (FactorDescriptor, error) {
	if factor, ok := f.factors[id]; ok {
		return factor, nil
	}
	return f.factor, nil
}
func (f *fakeCatalog) ListBindings(context.Context, string) ([]BindingDescriptor, error) {
	if len(f.bindings) > 0 {
		return append([]BindingDescriptor(nil), f.bindings...), nil
	}
	return []BindingDescriptor{f.binding}, nil
}
func (f *fakeCatalog) GetView(_ context.Context, id string) (ViewDescriptor, error) {
	view, ok := f.view[id]
	if !ok {
		return ViewDescriptor{}, errors.New("view not found")
	}
	return view, nil
}
func (f *fakeCatalog) ListViewColumns(_ context.Context, id string) ([]ViewColumn, error) {
	return f.columns[id], nil
}

func TestCompileFreezesSingleOutputFactorBinding(t *testing.T) {
	catalog := &fakeCatalog{
		factor:  FactorDescriptor{ID: "bias", Status: "enabled", InputColumns: []string{"close"}, ParamsJSON: `{"window":20}`, LookbackPeriods: 20, Outputs: []string{"bias"}},
		binding: BindingDescriptor{ID: "binding-1", FactorID: "bias", SpaceID: "crypto", SourceViewID: "source", Frequency: "1m", Status: "enabled", ResultDatasetID: "bias-dataset", ResultViewID: "bias-view"},
		view: map[string]ViewDescriptor{
			"source":    {ID: "source", Status: "active", Frequency: "1m"},
			"bias-view": {ID: "bias-view", Status: "active", Frequency: "1m"},
		},
		columns: map[string][]ViewColumn{"bias-view": {{Name: "bias_value", Attributes: map[string]string{"origin_factor_id": "bias", "factor_output": "bias"}}}},
	}
	manifest := config.Manifest{
		APIVersion: config.APIVersion, Kind: config.Kind,
		Input:          config.ManifestInput{SourceViewID: "source", DataFrequency: "1m", Factors: []config.FactorRef{{FactorID: "bias"}}},
		InstrumentPool: config.InstrumentPoolRule{Markets: []string{"spot"}},
		Long:           &config.Side{SideWeight: "1", Scores: []config.ScoreRule{{FactorID: "bias", Direction: "ascending", Weight: "1"}}, Selection: config.SelectionRule{Mode: "count", Value: 1}},
	}
	compiled, err := (Compiler{Factors: catalog, Storage: catalog}).Compile(context.Background(), manifest, "crypto")
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Factors[0].ColumnName != "bias_value" || compiled.Factors[0].BindingID != "binding-1" {
		t.Fatalf("compiled=%+v", compiled)
	}
	if compiled.Factors[0].Frequency != "1m" {
		t.Fatalf("compiled factor frequency = %q, want 1m", compiled.Factors[0].Frequency)
	}
	if compiled.Hash == "" || len(compiled.CompiledJSON) == 0 {
		t.Fatal("compiled artifact hash/json missing")
	}
	if err := (Compiler{Factors: catalog, Storage: catalog}).VerifyDependencies(context.Background(), compiled); err != nil {
		t.Fatal(err)
	}
	catalog.binding.Frequency = "5m"
	if err := (Compiler{Factors: catalog, Storage: catalog}).VerifyDependencies(context.Background(), compiled); err == nil {
		t.Fatal("frequency-mutated binding accepted")
	} else if !errors.Is(err, ErrDependencyMismatch) {
		t.Fatalf("frequency-mutated binding error = %v, want dependency mismatch", err)
	}
	catalog.binding.Frequency = "1m"
	catalog.factor.ParamsJSON = `{"window":30}`
	if err := (Compiler{Factors: catalog, Storage: catalog}).VerifyDependencies(context.Background(), compiled); err == nil {
		t.Fatal("parameter-mutated factor accepted")
	} else if !errors.Is(err, ErrDependencyMismatch) {
		t.Fatalf("parameter-mutated factor error = %v, want dependency mismatch", err)
	}
}

func TestCompileRejectsMissingMatchingBinding(t *testing.T) {
	catalog := &fakeCatalog{factor: FactorDescriptor{ID: "bias", Status: "enabled", Outputs: []string{"bias"}}, binding: BindingDescriptor{ID: "b", FactorID: "bias", Status: "enabled"}, view: map[string]ViewDescriptor{"source": {ID: "source", Status: "active", Frequency: "1m"}}}
	manifest := config.Manifest{APIVersion: config.APIVersion, Kind: config.Kind, Input: config.ManifestInput{SourceViewID: "source", DataFrequency: "1m", Factors: []config.FactorRef{{FactorID: "bias"}}}, Long: &config.Side{SideWeight: "1", Scores: []config.ScoreRule{{FactorID: "bias", Direction: "ascending", Weight: "1"}}, Selection: config.SelectionRule{Mode: "count", Value: 1}}}
	_, err := (Compiler{Factors: catalog, Storage: catalog}).Compile(context.Background(), manifest, "crypto")
	if err == nil {
		t.Fatal("missing binding accepted")
	}
}

func TestCompileSelectsExplicitOutputFromMultiOutputFactor(t *testing.T) {
	catalog := &fakeCatalog{
		factor:  FactorDescriptor{ID: "bias", Status: "enabled", Outputs: []string{"bias_20", "bias_96"}},
		binding: BindingDescriptor{ID: "binding-1", FactorID: "bias", SpaceID: "crypto", SourceViewID: "source", Frequency: "1m", Status: "enabled", ResultDatasetID: "bias-dataset", ResultViewID: "bias-view"},
		view:    map[string]ViewDescriptor{"source": {ID: "source", Status: "active", Frequency: "1m"}, "bias-view": {ID: "bias-view", Status: "active", Frequency: "1m"}},
		columns: map[string][]ViewColumn{"bias-view": {
			{Name: "bias_20_value", Attributes: map[string]string{"origin_factor_id": "bias", "factor_output": "bias_20"}},
			{Name: "bias_96_value", Attributes: map[string]string{"origin_factor_id": "bias", "factor_output": "bias_96"}},
		}},
	}
	manifest := config.Manifest{APIVersion: config.APIVersion, Kind: config.Kind, Input: config.ManifestInput{SourceViewID: "source", DataFrequency: "1m", Factors: []config.FactorRef{{FactorID: "bias", Output: "bias_96"}}}, InstrumentPool: config.InstrumentPoolRule{Markets: []string{"spot"}}, Long: &config.Side{SideWeight: "1", Scores: []config.ScoreRule{{FactorID: "bias", Direction: "ascending", Weight: "1"}}, Selection: config.SelectionRule{Mode: "count", Value: 1}}}
	compiled, err := (Compiler{Factors: catalog, Storage: catalog}).Compile(context.Background(), manifest, "crypto")
	require.NoError(t, err)
	require.Equal(t, "bias_96", compiled.Factors[0].Output)
	require.Equal(t, "bias_96_value", compiled.Factors[0].ColumnName)
}

func TestCompileRejectsMultipleFactorResultViews(t *testing.T) {
	catalog := &fakeCatalog{
		factors: map[string]FactorDescriptor{
			"bias-a": {ID: "bias-a", Status: "enabled", Outputs: []string{"bias"}},
			"bias-b": {ID: "bias-b", Status: "enabled", Outputs: []string{"bias"}},
		},
		bindings: []BindingDescriptor{
			{ID: "binding-a", FactorID: "bias-a", SpaceID: "crypto", SourceViewID: "source", Frequency: "1m", Status: "enabled", ResultDatasetID: "bias-a", ResultViewID: "view-a"},
			{ID: "binding-b", FactorID: "bias-b", SpaceID: "crypto", SourceViewID: "source", Frequency: "1m", Status: "enabled", ResultDatasetID: "bias-b", ResultViewID: "view-b"},
		},
		view: map[string]ViewDescriptor{
			"source": {ID: "source", Status: "active", Frequency: "1m"},
			"view-a": {ID: "view-a", Status: "active", Frequency: "1m"},
			"view-b": {ID: "view-b", Status: "active", Frequency: "1m"},
		},
		columns: map[string][]ViewColumn{
			"view-a": {{Name: "bias_a", Attributes: map[string]string{"origin_factor_id": "bias", "factor_output": "bias"}}},
			"view-b": {{Name: "bias_b", Attributes: map[string]string{"origin_factor_id": "bias", "factor_output": "bias"}}},
		},
	}
	manifest := config.Manifest{
		APIVersion: config.APIVersion, Kind: config.Kind,
		Input: config.ManifestInput{SourceViewID: "source", DataFrequency: "1m", Factors: []config.FactorRef{{FactorID: "bias-a"}, {FactorID: "bias-b"}}},
		Long:  &config.Side{SideWeight: "1", Scores: []config.ScoreRule{{FactorID: "bias-a", Direction: "ascending", Weight: "1"}, {FactorID: "bias-b", Direction: "ascending", Weight: "1"}}, Selection: config.SelectionRule{Mode: "count", Value: 1}},
	}
	_, err := (Compiler{Factors: catalog, Storage: catalog}).Compile(context.Background(), manifest, "crypto")
	if err == nil {
		t.Fatal("multiple factor result Views accepted")
	}
}
