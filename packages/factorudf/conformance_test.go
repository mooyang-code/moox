package factorudf

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func loadFixtures(t *testing.T, name string) FixtureSet {
	t.Helper()
	dir := filepath.Join("..", "..", "sdk", "rust", "examples", name, "tests", "fixtures")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	set := FixtureSet{}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		norm, err := NormalizeFixtures(raw)
		require.NoError(t, err)
		set[e.Name()] = norm
	}
	return set
}

func TestConformancePassesForExamples(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	for _, name := range []string{"ts_mean", "ema_trunc", "pair_relaxed", "faulty"} {
		t.Run(name, func(t *testing.T) {
			m, err := rt.Load(context.Background(), testWasm(t, name))
			require.NoError(t, err)
			var fx FixtureSet
			if name != "faulty" {
				fx = loadFixtures(t, name)
			}
			rep, err := RunConformance(context.Background(), rt, m, fx, ConformanceOptions{})
			require.NoError(t, err)
			require.True(t, rep.Passed, "%+v", rep.Failures())
			require.NotEmpty(t, rep.Digest)
			t.Logf("params=%d ns/point=%.0f peak=%d digest=%s skipped=%v", rep.ParamSets, rep.NSPerPoint, rep.PeakMemBytes, rep.Digest[:12], rep.Skipped)
		})
	}
}
