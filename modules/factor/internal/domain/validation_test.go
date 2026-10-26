package domain

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeFactorDefinitionDerivesContract(t *testing.T) {
	got, err := NormalizeFactorDefinition(FactorDef{
		FactorID: " excess_return ", Name: " 超额收益 ",
		Spec: "\n\nparams: {window: {type: int, min: 5, max: 20, default: 5}}\noutput: ts_mean(nav - benchmark_return, window)\n\n",
	})
	require.NoError(t, err)
	require.Equal(t, "excess_return", got.FactorID)
	require.Equal(t, "超额收益", got.Name)
	require.True(t, strings.HasSuffix(got.Spec, "\n") && !strings.HasPrefix(got.Spec, "\n"), "去掉首尾空白后补一个换行")
	require.Equal(t, SpecHash(got.Spec), got.SpecHash)
	require.Equal(t, FactorTypeTimeSeries, got.FactorType)
	require.Equal(t, []string{"benchmark_return", "nav"}, got.InputColumns)
	require.Equal(t, []string{"excess_return"}, got.Outputs)
	require.Equal(t, 20, got.LookbackPeriods)
	require.NotNil(t, got.Compiled())
}

func TestNormalizeFactorDefinitionRejectsInvalidValues(t *testing.T) {
	valid := FactorDef{FactorID: "f", Name: "F", Spec: "output: close"}
	tests := map[string]func(*FactorDef){
		"empty id":                      func(f *FactorDef) { f.FactorID = " " },
		"bad id":                        func(f *FactorDef) { f.FactorID = "my-factor" },
		"empty name":                    func(f *FactorDef) { f.Name = "" },
		"long name":                     func(f *FactorDef) { f.Name = strings.Repeat("名", 65) },
		"control in name":               func(f *FactorDef) { f.Name = "a\tb" },
		"empty spec":                    func(f *FactorDef) { f.Spec = " \n" },
		"syntax error":                  func(f *FactorDef) { f.Spec = "output: close +" },
		"unknown function":              func(f *FactorDef) { f.Spec = "output: nope(close)" },
		"reserved output":               func(f *FactorDef) { f.FactorID = "series_tag" },
		"output named after the factor": func(f *FactorDef) { f.Spec = "outputs:\n  f: close\n  g: open\n" },
		"partial timeseries":            func(f *FactorDef) { f.AllowPartialUniverse = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			factor := valid
			mutate(&factor)
			_, err := NormalizeFactorDefinition(factor)
			require.Error(t, err)
		})
	}
	cross := valid
	cross.Spec, cross.AllowPartialUniverse = "output: cs_rank(close)", true
	got, err := NormalizeFactorDefinition(cross)
	require.NoError(t, err)
	require.Equal(t, FactorTypeCrossSection, got.FactorType)
}

func TestValidateDefinitionRequiresMatchingSpecHash(t *testing.T) {
	def, err := NormalizeFactorDefinition(FactorDef{FactorID: "f", Name: "F", Spec: "output: close"})
	require.NoError(t, err)
	_, err = ValidateDefinition(def)
	require.NoError(t, err)
	def.SpecHash = "sha256:other"
	_, err = ValidateDefinition(def)
	require.ErrorContains(t, err, "spec_hash")
}

func TestFactorDefCarriesNoStatusOrTask(t *testing.T) {
	typeOf := reflect.TypeOf(FactorDef{})
	for _, name := range []string{"Status", "TaskID", "SourceCode", "ParamsJSON"} {
		_, has := typeOf.FieldByName(name)
		require.False(t, has, name)
	}
}
