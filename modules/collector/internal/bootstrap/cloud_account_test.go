package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/stretchr/testify/require"
)

// listingFunctions 只实现 List：返回预设的函数或错误。
type listingFunctions struct {
	cloud.Functions
	refs []cloud.FunctionRef
	err  error
}

func (f listingFunctions) List(_ context.Context, accountID, region, prefix string) ([]cloud.FunctionRef, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.refs, nil
}

// 删云账号之前：被 scf_plan.cloud_account_id 引用、云上还有它建的函数、读不了云上，都拒绝；都没有才放行。
func TestCloudAccountInUseChecksTheConfigAndTheCloud(t *testing.T) {
	cfg := Default()
	cfg.SCFRegions = map[string]fetchplan.PlanNetwork{"ap-nanjing": {VPCID: "vpc-1", SubnetID: "subnet-1"}}
	cfg.SCFPlan.CloudAccountID = "account-a"
	ctx := context.Background()

	require.ErrorIs(t, cfg.cloudAccountInUse(ctx, listingFunctions{}, "account-a"), cloud.ErrAccountInUse, "被 scf_plan 引用")
	err := cfg.cloudAccountInUse(ctx, listingFunctions{refs: []cloud.FunctionRef{{Name: "moox-fetch-plan-crypto-spot-nanjing-000"}}}, "account-b")
	require.ErrorIs(t, err, cloud.ErrAccountInUse)
	require.ErrorContains(t, err, "moox-fetch-plan-crypto-spot-nanjing-000", "云上还有它建的函数")
	require.ErrorIs(t, cfg.cloudAccountInUse(ctx, listingFunctions{err: errors.New("AuthFailure")}, "account-b"), cloud.ErrAccountInUse, "读不了云上也拒绝")
	require.NoError(t, cfg.cloudAccountInUse(ctx, listingFunctions{}, "account-b"))
}
