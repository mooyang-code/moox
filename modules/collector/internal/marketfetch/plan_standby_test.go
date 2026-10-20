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
	require.Zero(t, standbyPlan(PlanFetch{}).StandbyFor("moox-fetch-plan-crypto-spot-hongkong-000", hour))
	require.Zero(t, (*Plan)(nil).StandbyFor("fn", hour))

	on := standbyPlan(PlanFetch{StandbyMS: 800})
	require.Equal(t, 800, on.StandbyFor("moox-fetch-plan-crypto-spot-hongkong-000", hour))
	require.Equal(t, 800, on.StandbyFor("moox-fetch-plan-crypto-spot-hongkong-000", minute))

	hourOnly := standbyPlan(PlanFetch{StandbyMS: 800, StandbyHourOnly: true})
	require.Equal(t, 800, hourOnly.StandbyFor("moox-fetch-plan-crypto-spot-hongkong-000", hour))
	require.Zero(t, hourOnly.StandbyFor("moox-fetch-plan-crypto-spot-hongkong-000", minute))
}

func TestStandbyForSplitsFunctionsByTrailingIndexForSameRoundComparison(t *testing.T) {
	hour := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	plan := standbyPlan(PlanFetch{StandbyMS: 800, StandbyPercent: 50})
	enabled := 0
	for index := 0; index < 21; index++ {
		name := "moox-fetch-plan-crypto-spot-hongkong-" + []string{"000", "001", "002", "003", "004", "005", "006", "007", "008", "009", "010", "011", "012", "013", "014", "015", "016", "017", "018", "019", "020"}[index]
		if plan.StandbyFor(name, hour) > 0 {
			enabled++
		}
	}
	require.Equal(t, 11, enabled, "个位 0～4 的函数备援：0～4、10～14、20 共 11 个")
	require.Equal(t, 800, plan.StandbyFor("fn-004", hour))
	require.Zero(t, plan.StandbyFor("fn-005", hour))
	// 名字不以数字结尾：按哈希分组，结果稳定。
	require.Equal(t, plan.StandbyFor("no-digits", hour), plan.StandbyFor("no-digits", hour))
	// 占比 0 或 100 都是全部函数。
	require.Equal(t, 800, standbyPlan(PlanFetch{StandbyMS: 800, StandbyPercent: 100}).StandbyFor("fn-009", hour))
}

func TestPlanValidateRejectsOutOfRangeStandby(t *testing.T) {
	valid := func(fetch PlanFetch) error {
		plan := testPlan()
		fetch.Concurrency = plan.Fetch.Concurrency
		plan.Fetch = fetch
		return plan.Validate()
	}
	require.NoError(t, valid(PlanFetch{}))
	require.NoError(t, valid(PlanFetch{StandbyMS: 800, StandbyPercent: 50}))
	require.Error(t, valid(PlanFetch{StandbyMS: 50}), "延迟太短会让几乎所有请求都被备援")
	require.Error(t, valid(PlanFetch{StandbyMS: MaxStandbyMS + 1}))
	require.Error(t, valid(PlanFetch{StandbyMS: 800, StandbyPercent: 101}))
}

func TestPlanWithStandbySurvivesEncodeDecode(t *testing.T) {
	plan := testPlan()
	plan.Fetch.StandbyMS, plan.Fetch.StandbyPercent, plan.Fetch.StandbyHourOnly = 800, 50, true
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
