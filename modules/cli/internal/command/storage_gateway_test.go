package command

import (
	"context"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type storageGatewayCall func(context.Context, string, string, any, any) error

func (f storageGatewayCall) Invoke(ctx context.Context, service, method string, request, response any) error {
	return f(ctx, service, method, request, response)
}

func TestCanaryStorageGatewayPreservesIndependentRoleAuthentication(t *testing.T) {
	var calls []string
	access, err := newCollectorCanaryAccess(&setupconfig.SCFFetcherSpace{}, collectorCanaryTestInventory{}, storageGatewayCall(func(_ context.Context, service, method string, request, _ any) error {
		calls = append(calls, service+"/"+method)
		auth := request.(interface{ GetAuthInfo() *pb.AuthInfo }).GetAuthInfo()
		root, appID := "primary-root", "scf-market-canary"
		if method == "EnsureDatasetPeriod" || method == "GetDatasetPeriodStatus" {
			appID = "collector"
		}
		if method == "QueryTimeSeriesRows" {
			root = "view-root"
		}
		require.Equal(t, appID, auth.GetAppId())
		require.Equal(t, security.HMACSHA256Hex(root, []byte(appID)), auth.GetAppKey())
		return nil
	}), collectorSCFTrustMaterial{StoragePrimaryAuthSecret: "primary-root", StorageViewAuthSecret: "view-root"})
	require.NoError(t, err)
	_, err = access.primary.EnsureDatasetPeriod(t.Context(), &pb.PrimaryEnsureDatasetPeriodReq{AuthInfo: access.periodAuth})
	require.NoError(t, err)
	_, err = access.primary.GetDatasetPeriodStatus(t.Context(), &pb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: access.periodAuth})
	require.NoError(t, err)
	_, err = access.primary.ReadTimeSeriesRows(t.Context(), &pb.ReadTimeSeriesRowsReq{AuthInfo: access.primaryAuth})
	require.NoError(t, err)
	_, err = access.view.QueryTimeSeriesRows(t.Context(), &pb.QueryTimeSeriesRowsReq{AuthInfo: access.viewAuth})
	require.NoError(t, err)
	require.Equal(t, []string{"trpc.moox.storage.PrimaryStore/EnsureDatasetPeriod", "trpc.moox.storage.PrimaryStore/GetDatasetPeriodStatus", "trpc.moox.storage.PrimaryStore/ReadTimeSeriesRows", "trpc.moox.storage.DataView/QueryTimeSeriesRows"}, calls)
	_, err = access.primary.ReadTimeSeriesRows(t.Context(), &pb.ReadTimeSeriesRowsReq{}, client.WithTarget("ip://public.example:11003"))
	require.ErrorContains(t, err, "forbids SDK option overrides")
	require.Len(t, calls, 4)
}
