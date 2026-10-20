package marketfetch

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func hedgePlan(fetch PlanFetch) *Plan {
	return &Plan{V: 1, Fetch: fetch}
}

func TestHedgeForIsOffByDefaultAndHonorsHourOnly(t *testing.T) {
	hour := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	minute := hour.Add(5 * time.Minute)
	require.Zero(t, hedgePlan(PlanFetch{}).HedgeFor("moox-fetch-plan-crypto-spot-hongkong-000", hour))
	require.Zero(t, (*Plan)(nil).HedgeFor("fn", hour))

	on := hedgePlan(PlanFetch{HedgeMS: 800})
	require.Equal(t, 800, on.HedgeFor("moox-fetch-plan-crypto-spot-hongkong-000", hour))
	require.Equal(t, 800, on.HedgeFor("moox-fetch-plan-crypto-spot-hongkong-000", minute))

	hourOnly := hedgePlan(PlanFetch{HedgeMS: 800, HedgeHourOnly: true})
	require.Equal(t, 800, hourOnly.HedgeFor("moox-fetch-plan-crypto-spot-hongkong-000", hour))
	require.Zero(t, hourOnly.HedgeFor("moox-fetch-plan-crypto-spot-hongkong-000", minute))
}

func TestHedgeForSplitsFunctionsByTrailingIndexForSameRoundComparison(t *testing.T) {
	hour := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	plan := hedgePlan(PlanFetch{HedgeMS: 800, HedgePercent: 50})
	enabled := 0
	for index := 0; index < 21; index++ {
		name := "moox-fetch-plan-crypto-spot-hongkong-" + []string{"000", "001", "002", "003", "004", "005", "006", "007", "008", "009", "010", "011", "012", "013", "014", "015", "016", "017", "018", "019", "020"}[index]
		if plan.HedgeFor(name, hour) > 0 {
			enabled++
		}
	}
	require.Equal(t, 11, enabled, "个位 0～4 的函数对冲：0～4、10～14、20 共 11 个")
	require.Equal(t, 800, plan.HedgeFor("fn-004", hour))
	require.Zero(t, plan.HedgeFor("fn-005", hour))
	// 名字不以数字结尾：按哈希分组，结果稳定。
	require.Equal(t, plan.HedgeFor("no-digits", hour), plan.HedgeFor("no-digits", hour))
	// 占比 0 或 100 都是全部函数。
	require.Equal(t, 800, hedgePlan(PlanFetch{HedgeMS: 800, HedgePercent: 100}).HedgeFor("fn-009", hour))
}

func TestPlanValidateRejectsOutOfRangeHedge(t *testing.T) {
	valid := func(fetch PlanFetch) error {
		plan := testPlan()
		fetch.Concurrency = plan.Fetch.Concurrency
		plan.Fetch = fetch
		return plan.Validate()
	}
	require.NoError(t, valid(PlanFetch{}))
	require.NoError(t, valid(PlanFetch{HedgeMS: 800, HedgePercent: 50}))
	require.Error(t, valid(PlanFetch{HedgeMS: 50}), "延迟太短会让几乎所有请求都被对冲")
	require.Error(t, valid(PlanFetch{HedgeMS: MaxHedgeMS + 1}))
	require.Error(t, valid(PlanFetch{HedgeMS: 800, HedgePercent: 101}))
}

func TestPlanWithHedgeSurvivesEncodeDecode(t *testing.T) {
	plan := testPlan()
	plan.Fetch.HedgeMS, plan.Fetch.HedgePercent, plan.Fetch.HedgeHourOnly = 800, 50, true
	encoded, err := EncodePlan("crypto", plan)
	require.NoError(t, err)
	decoded, err := DecodePlan(encoded)
	require.NoError(t, err)
	require.Equal(t, plan.Fetch, decoded.Fetch)
}

func TestRequestRejectsOutOfRangeHedge(t *testing.T) {
	req := Request{
		BatchID: "batch-1", SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, BatchKind: domain.BatchKindRealtime,
		Items:   []domain.CollectionItem{{SubjectID: "600000.XSHG", Symbol: "sh600000", DatasetID: StockCNDatasetID, BarLimit: 1}},
		HedgeMS: 800,
	}
	require.NoError(t, req.validate())
	req.HedgeMS = MaxHedgeMS + 1
	require.ErrorContains(t, req.validate(), "hedge_ms")
}
