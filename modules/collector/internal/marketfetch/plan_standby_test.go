package marketfetch

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func standbyPlan(fetch PlanFetch) *Plan {
	return &Plan{V: 1, Fetch: fetch}
}

func TestStandbyForIsOffByDefaultAndHonorsHourOnly(t *testing.T) {
	hour := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	minute := hour.Add(5 * time.Minute)
	require.Zero(t, standbyPlan(PlanFetch{}).StandbyFor(hour))
	require.Zero(t, (*Plan)(nil).StandbyFor(hour))

	on := standbyPlan(PlanFetch{StandbyMS: 800})
	require.Equal(t, 800, on.StandbyFor(hour))
	require.Equal(t, 800, on.StandbyFor(minute))

	hourOnly := standbyPlan(PlanFetch{StandbyMS: 800, StandbyHourOnly: true})
	require.Equal(t, 800, hourOnly.StandbyFor(hour))
	require.Zero(t, hourOnly.StandbyFor(minute))
}

func TestPlanValidateRejectsOutOfRangeStandby(t *testing.T) {
	valid := func(fetch PlanFetch) error {
		plan := testPlan()
		fetch.Concurrency = plan.Fetch.Concurrency
		plan.Fetch = fetch
		return plan.Validate()
	}
	require.NoError(t, valid(PlanFetch{}))
	require.NoError(t, valid(PlanFetch{StandbyMS: 800, ConnReuse: true}))
	require.Error(t, valid(PlanFetch{StandbyMS: 50}), "延迟太短会让几乎所有请求都发出备援")
	require.Error(t, valid(PlanFetch{StandbyMS: MaxStandbyMS + 1}))
}

func TestPlanWithStandbyAndConnReuseSurvivesEncodeDecode(t *testing.T) {
	plan := testPlan()
	plan.Fetch.StandbyMS, plan.Fetch.StandbyHourOnly, plan.Fetch.ConnReuse = 800, true, true
	encoded, err := EncodePlan("crypto", plan)
	require.NoError(t, err)
	decoded, err := DecodePlan(encoded)
	require.NoError(t, err)
	require.Equal(t, plan.Fetch, decoded.Fetch)
}

func TestRequestRejectsOutOfRangeStandby(t *testing.T) {
	req := Request{
		BatchID: "batch-1", SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, BatchKind: domain.BatchKindRealtime,
		Items:     []domain.CollectionItem{{SubjectID: "600000.XSHG", Symbol: "sh600000", DatasetID: StockCNDatasetID, BarLimit: 1}},
		StandbyMS: 800,
	}
	require.NoError(t, req.validate())
	req.StandbyMS = MaxStandbyMS + 1
	require.ErrorContains(t, req.validate(), "standby_ms")
}
