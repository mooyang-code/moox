package factorudf

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func testWasm(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".wasm"))
	require.NoError(t, err)
	return b
}

func newTestRuntime(t *testing.T, opts Options) *Runtime {
	t.Helper()
	rt, err := NewRuntime(context.Background(), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	return rt
}

func TestLoadExampleModules(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	for _, name := range []string{"ts_mean", "ema_trunc", "pair_relaxed", "faulty"} {
		m, err := rt.Load(context.Background(), testWasm(t, name))
		require.NoError(t, err, name)
		require.Equal(t, 1, m.Info.Manifest.ABI)
		t.Logf("%s: %+v", name, *m.Info.Manifest)
	}
}

func TestEvalTsMean(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	m, err := rt.Load(context.Background(), testWasm(t, "ts_mean"))
	require.NoError(t, err)
	b := &Batch{NSeries: 1, InLen: 6, OutLen: 4, Inputs: [][]float64{{1, 2, 3, 4, 5, 6}}, Params: []float64{3}, Out: make([]float64, 4)}
	require.NoError(t, m.Eval(context.Background(), b))
	require.Equal(t, []float64{2, 3, 4, 5}, b.Out)
	nan := math.NaN()
	b = &Batch{NSeries: 1, InLen: 5, OutLen: 3, Inputs: [][]float64{{1, nan, 3, 4, 5}}, Params: []float64{3}, Out: make([]float64, 3)}
	require.NoError(t, m.Eval(context.Background(), b))
	require.True(t, math.IsNaN(b.Out[0]) && math.IsNaN(b.Out[1]))
	require.Equal(t, 4.0, b.Out[2])
}
