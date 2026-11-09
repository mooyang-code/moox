package engine

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/observability"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

type warningRunner struct{ outcome pipeline.Outcome }

func (r warningRunner) Run(context.Context, pipeline.Plan) (pipeline.Outcome, error) {
	return r.outcome, nil
}

// 截面回归奇异、组数超限按原因计入 factor_failures_total，计数是截面数；成员状态是 complete 时不另记状态失败。
func TestMeasuredRunnerCountsCrossSectionWarnings(t *testing.T) {
	metrics, err := observability.NewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	runner := &measuredRunner{
		inner: warningRunner{outcome: pipeline.Outcome{
			Status:  "complete",
			Factors: []storageio.FactorState{{MemberKey: "resid", Status: "complete"}},
			Warnings: map[string]map[string]int{
				"resid": {"cs_resid_singular": 3, "gs_too_many_groups": 1},
			},
		}},
		metrics: metrics, health: NewHealth(time.Minute), runs: newRunTracker(),
	}
	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: domain.FactorTask{TaskID: "ftask_x", SpaceID: "crypto", Freq: "1m"},
		TargetStart: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), TargetEnd: time.Date(2026, 10, 4, 0, 5, 0, 0, time.UTC)}

	_, err = runner.Run(context.Background(), plan)

	require.NoError(t, err)
	require.Equal(t, 3.0, testutil.ToFloat64(metrics.Failures.WithLabelValues("ftask_x", "resid", "cs_resid_singular")))
	require.Equal(t, 1.0, testutil.ToFloat64(metrics.Failures.WithLabelValues("ftask_x", "resid", "gs_too_many_groups")))
	require.Equal(t, 0.0, testutil.ToFloat64(metrics.Failures.WithLabelValues("ftask_x", "resid", "complete")))
}
