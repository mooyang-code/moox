package factorio

import (
	"context"
	"testing"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type factorMgrProxyFake struct {
	factorpb.FactorMgrClientProxy
	setRequests    []*factorpb.ListFactorSetsReq
	factorRequests []*factorpb.ListFactorsReq
}

func (f *factorMgrProxyFake) ListFactorSets(_ context.Context, req *factorpb.ListFactorSetsReq, _ ...client.Option) (*factorpb.ListFactorSetsRsp, error) {
	f.setRequests = append(f.setRequests, req)
	setID, resultDatasetID := "set-1", "result-1"
	if req.GetPage().GetPage() == 2 {
		setID, resultDatasetID = "set-2", "result-2"
	}
	return &factorpb.ListFactorSetsRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		FactorSets: []*factorpb.FactorSetInfo{{FactorSet: &factorpb.FactorSet{
			SetId: setID, ResultDatasetId: resultDatasetID, Status: "enabled", Freq: "1m",
		}}},
		PageResult: &commonpb.PageResult{HasMore: req.GetPage().GetPage() == 1},
	}, nil
}

func (f *factorMgrProxyFake) ListFactors(_ context.Context, req *factorpb.ListFactorsReq, _ ...client.Option) (*factorpb.ListFactorsRsp, error) {
	f.factorRequests = append(f.factorRequests, req)
	return &factorpb.ListFactorsRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Factors: []*factorpb.FactorInfo{
			{
				Factor: &factorpb.FactorDef{
					FactorId: "momentum", Outputs: []string{"score"},
					SourceHash: "hash-1", InputColumns: []string{"close"}, ParamsJson: "{}", LookbackPeriods: 4,
				},
				// The same definition is used by another set too; only the requested set's usage counts.
				Usages: []*factorpb.FactorUsage{{SetId: "other-set", Status: "disabled"}, {SetId: req.GetSetId(), Status: "enabled"}},
			},
			{
				Factor: &factorpb.FactorDef{FactorId: "stray", Outputs: []string{"stray"}, SourceHash: "hash-2"},
				Usages: []*factorpb.FactorUsage{{SetId: "other-set", Status: "enabled"}},
			},
			nil,
		},
		PageResult: &commonpb.PageResult{},
	}, nil
}

func TestListFactorsUsesUsageStatusForRequestedSet(t *testing.T) {
	proxy := &factorMgrProxyFake{}
	client := &RPCClient{Proxy: proxy, PageSize: 1}

	sets, err := client.ListFactorSets(context.Background())
	require.NoError(t, err)
	require.Equal(t, []compiler.FactorSetDescriptor{
		{SetID: "set-1", Status: "enabled", ResultDatasetID: "result-1", Frequency: "1m"},
		{SetID: "set-2", Status: "enabled", ResultDatasetID: "result-2", Frequency: "1m"},
	}, sets)
	require.Len(t, proxy.setRequests, 2)
	require.Equal(t, uint32(1), proxy.setRequests[0].GetPage().GetPage())
	require.Equal(t, uint32(2), proxy.setRequests[1].GetPage().GetPage())
	require.Equal(t, uint32(1), proxy.setRequests[0].GetPage().GetSize())

	factors, err := client.ListFactors(context.Background(), sets[0])
	require.NoError(t, err)
	require.Equal(t, []compiler.FactorDescriptor{{
		FactorID: "momentum", SetID: "set-1", Outputs: []string{"score"}, Status: "enabled", ResultDatasetID: "result-1",
		SourceHash: "hash-1", InputColumns: []string{"close"}, ParamsJSON: "{}", LookbackPeriods: 4,
	}}, factors)
	require.Len(t, proxy.factorRequests, 1)
	require.Equal(t, "set-1", proxy.factorRequests[0].GetSetId())
	// Strategy only needs hashes and column contracts, never the factor source.
	require.False(t, proxy.factorRequests[0].GetIncludeSource())
}

func TestListFactorsIgnoresDefinitionsWithoutUsageInRequestedSet(t *testing.T) {
	client := &RPCClient{Proxy: &factorMgrProxyFake{}, PageSize: 1}
	factors, err := client.ListFactors(context.Background(), compiler.FactorSetDescriptor{SetID: "set-9", ResultDatasetID: "result-9"})
	require.NoError(t, err)
	require.Len(t, factors, 1)
	require.Equal(t, "momentum", factors[0].FactorID)
	require.Equal(t, "set-9", factors[0].SetID)
	require.Equal(t, "enabled", factors[0].Status)
}
