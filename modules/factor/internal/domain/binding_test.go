package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFactorDef_TableName_ShouldReturnFactorDefsTable(t *testing.T) {
	assert.Equal(t, "t_factor_defs", FactorDef{}.TableName())
}

func TestFactorBinding_TableName_ShouldReturnFactorBindingsTable(t *testing.T) {
	assert.Equal(t, "t_factor_bindings", FactorBinding{}.TableName())
}

func TestNormalizeBindingSubjectsAllAlwaysStoresEmptyArray(t *testing.T) {
	got, err := NormalizeBindingSubjects(SubjectModeAll, `not-json`)
	require.NoError(t, err)
	require.Equal(t, DefaultSubjectsJSON, got)
}

func TestNormalizeBindingSubjectsIncludeTrimsDeduplicatesAndSorts(t *testing.T) {
	got, err := NormalizeBindingSubjects(SubjectModeInclude, `[" ETH ","BTC","ETH"]`)
	require.NoError(t, err)
	require.Equal(t, `["BTC","ETH"]`, got)
}

func TestNormalizeBindingSubjectsRejectsInvalidIncludeScope(t *testing.T) {
	for _, raw := range []string{"", "null", `{}`, `[""]`, `["BTC", "  "]`, `[1]`, `[]`} {
		t.Run(raw, func(t *testing.T) {
			_, err := NormalizeBindingSubjects(SubjectModeInclude, raw)
			require.Error(t, err)
		})
	}
}

func TestNormalizeBindingSubjectsRejectsUnknownMode(t *testing.T) {
	_, err := NormalizeBindingSubjects("exclude", `["BTC"]`)
	require.Error(t, err)
}

func TestBindingAllowsSubjectDefensivelyRejectsInvalidScope(t *testing.T) {
	require.False(t, BindingAllowsSubject(FactorBinding{
		SubjectMode: SubjectModeInclude, SubjectsJSON: `not-json`,
	}, "BTC"))
	require.True(t, BindingAllowsSubject(FactorBinding{
		SubjectMode: SubjectModeInclude, SubjectsJSON: `["BTC"]`,
	}, "BTC"))
}
