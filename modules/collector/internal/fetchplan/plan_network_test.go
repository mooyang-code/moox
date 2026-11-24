package fetchplan

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// public 地域的定时函数与补采函数不绑 VPC；地域网络写错时两者都拒绝。
func TestPublicRegionSpecsHaveNoVPC(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	name := h.cloudTimers()[0]

	r.Deploy.Networks = map[string]PlanNetwork{"ap-nanjing": {Network: NetworkPublic}}
	spec, err := r.desiredSpec(functionTarget{Name: name, Region: "ap-nanjing", Plan: h.cloudPlan(name), Cron: "0 * * * * * *"})
	require.NoError(t, err)
	require.Empty(t, spec.VPCID)
	require.Empty(t, spec.SubnetID)
	backfill, err := r.backfillSpec(r.backfillRef("spot", "ap-nanjing"), nil)
	require.NoError(t, err)
	require.Empty(t, backfill.VPCID)
	require.Empty(t, backfill.SubnetID)

	r.Deploy.Networks = map[string]PlanNetwork{"ap-nanjing": {Network: NetworkPublic, VPCID: "vpc-1", SubnetID: "subnet-1"}}
	_, err = r.desiredSpec(functionTarget{Name: name, Region: "ap-nanjing", Plan: h.cloudPlan(name), Cron: "0 * * * * * *"})
	require.ErrorContains(t, err, "must not set vpc_id")
	_, err = r.backfillSpec(r.backfillRef("spot", "ap-nanjing"), nil)
	require.ErrorContains(t, err, "must not set vpc_id")
}
