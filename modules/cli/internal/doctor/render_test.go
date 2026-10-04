package doctor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/require"
)

func TestRenderAndWriteAtomic(t *testing.T) {
	report := core.Report{SchemaVersion: core.ReportSchemaVersion, RunID: "run-1", Mode: core.ModeBootstrap, StartedAt: time.Now(), FinishedAt: time.Now(), Conclusion: core.ConclusionHealthy, Checks: []core.CheckResult{{ID: "bootstrap.release_contract", Status: core.StatusPass}}}
	for _, format := range []string{"json", "text", "markdown"} {
		raw, err := Render(report, format)
		require.NoError(t, err)
		require.NotEmpty(t, raw)
	}
	path := filepath.Join(t.TempDir(), "doctor.json")
	raw, _ := Render(report, "json")
	require.NoError(t, WriteAtomic(path, raw))
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, raw, written)
}
