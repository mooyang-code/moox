package compiler

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBarsExpressionContract(t *testing.T) {
	fields := map[string]reflect.Type{
		"close":       reflect.TypeOf(float64(0)),
		"ma20":        reflect.TypeOf(float64(0)),
		"return_20":   reflect.TypeOf(float64(0)),
		"turnover_20": reflect.TypeOf(float64(0)),
	}
	cases := []struct {
		name       string
		source     string
		stage      ExpressionStage
		wantError  bool
		wantBool   bool
		wantFields []string
		wantBars   map[int][]string
	}{
		{
			name:     "crosses previous and current bars",
			source:   "bars[-1].ma20 <= bars[-1].close && bars[0].ma20 > bars[0].close",
			stage:    StageSignalEntry,
			wantBool: true,
			wantBars: map[int][]string{-1: {"close", "ma20"}, 0: {"close", "ma20"}},
		},
		{
			name:     "current bar only",
			source:   "bars[0].close > 0",
			stage:    StageFilterBefore,
			wantBool: true,
			wantBars: map[int][]string{0: {"close"}},
		},
		{name: "positive bar index", source: "bars[1].close > 0", stage: StageSignalEntry, wantError: true},
		{name: "older bar index", source: "bars[-2].close > 0", stage: StageSignalEntry, wantError: true},
		{name: "dynamic bar index", source: "bars[offset].close > 0", stage: StageSignalEntry, wantError: true},
		{name: "legacy previous alias", source: "prev.close > 0", stage: StageSignalEntry, wantError: true},
		{name: "unknown bar field", source: "bars[0].unknown > 0", stage: StageSignalEntry, wantError: true},
		{name: "now function", source: "now() > 0", stage: StageFilterBefore, wantError: true},
		{name: "random function", source: "rand() > 0", stage: StageFilterBefore, wantError: true},
		{name: "unregistered function", source: "custom(close)", stage: StageFilterBefore, wantError: true},
		{name: "rank field", source: "pct_rank(return_20)", stage: StageScore, wantFields: []string{"return_20"}},
		{name: "rank expression argument", source: "pct_rank(return_20 + 1)", stage: StageScore, wantError: true},
		{name: "rank outside score", source: "pct_rank(return_20) > 0.5", stage: StageFilterBefore, wantError: true},
		{name: "score in signal", source: "score > 0", stage: StageSignalEntry, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compiled, err := CompileExpression(tc.source, tc.stage, fields)
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, compiled.Program)
			require.Equal(t, tc.wantBool, tc.stage != StageScore)
			require.Equal(t, tc.wantFields, compiled.Dependencies.Fields)
			require.Equal(t, tc.wantBars, compiled.Dependencies.Bars)
		})
	}
}
