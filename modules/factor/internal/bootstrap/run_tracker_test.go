package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestRunTrackerKeepsNewestPeriodAndComputesLag(t *testing.T) {
	tracker := newRunTracker()
	t1 := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	tracker.record("fset_a", t1, "complete")
	tracker.record("fset_a", t1.Add(-time.Minute), "degraded")
	tracker.record("fset_b", t1, "degraded")

	summary := tracker.latest("fset_a", t1.Add(30*time.Second))
	require.Equal(t, t1.Unix(), summary.LastPeriodTime)
	require.Equal(t, "complete", summary.LastStatus)
	require.EqualValues(t, 30, summary.LagSeconds)
	require.Len(t, tracker.all(t1), 2)
	require.Equal(t, "fset_a", tracker.all(t1)[0].SetID)
	require.Zero(t, tracker.latest("missing", t1).LastPeriodTime)
}

func TestPeriodFailureReasonClassifiesAbortedPeriods(t *testing.T) {
	require.Equal(t, "storage_unavailable", periodFailureReason(fmt.Errorf("write: %w", storageio.ErrInfra)))
	require.Equal(t, "timeout", periodFailureReason(fmt.Errorf("compute: %w", context.DeadlineExceeded)))
	require.Equal(t, "timeout", periodFailureReason(context.Canceled))
	require.Equal(t, "internal", periodFailureReason(errors.New("assemble failed")))
}
