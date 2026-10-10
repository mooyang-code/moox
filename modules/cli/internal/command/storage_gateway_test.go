package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/client"
)

type storageGatewayCall func(context.Context, string, string, any, any) error

func (f storageGatewayCall) Invoke(ctx context.Context, service, method string, request, response any) error {
	return f(ctx, service, method, request, response)
}

func TestStorageImportDoesNotResendAnUnknownWriteResult(t *testing.T) {
	failure := errors.New("transport lost while sending dataset write")
	var calls int
	gateway := storageGatewayCall(func(ctx context.Context, service, method string, request, response any) error {
		calls++
		require.Equal(t, accessServiceName, service)
		require.Equal(t, "UpsertFields", method)
		require.Equal(t, "crypto", gatewayclient.CallMetadataFromContext(ctx).SpaceID)
		require.Equal(t, "operator", gatewayclient.CallMetadataFromContext(ctx).UserID)
		require.Equal(t, "import-role", request.(*pb.PrimaryUpsertFieldsReq).GetAuthInfo().GetAppId())
		return failure
	})
	writer := gatewayStorageDataWriter{Gateway: gateway, SpaceID: "crypto", Auth: &pb.AuthInfo{AppId: "import-role"}}
	request := &pb.PrimaryUpsertFieldsReq{Rows: []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "crypto"}}}}
	ctx := gatewayclient.WithCallMetadata(t.Context(), gatewayclient.CallMetadata{UserID: "operator"})
	require.ErrorIs(t, writer.UpsertFields(ctx, request), failure)
	require.Equal(t, 1, calls)
	require.Nil(t, request.AuthInfo, "role injection must preserve the input request")
	request.Rows[0].Key.SpaceId = "other-space"
	require.ErrorContains(t, writer.UpsertFields(ctx, request), "selected space")
	require.Equal(t, 1, calls)
}

func TestManagementConfigurationsRemoveHTTPAndKeepNativeListeners(t *testing.T) {
	for path, required := range map[string]map[int]bool{
		"factor/config/trpc_go.yaml":               {11403: false},
		"strategy/config/trpc_go.yaml":             {11430: false},
		"storage/config/trpc_go.yaml":              {20100: false, 20101: false, 20102: false, 20103: false},
		"storage/config/trpc_go.primary.yaml":      {20100: false, 20101: false, 20102: false},
		"storage/config/storage_view/trpc_go.yaml": {20103: false},
	} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", path))
			require.NoError(t, err)
			var cfg struct {
				Server struct {
					Service []struct {
						Name     string
						IP       string
						Port     int
						Protocol string
					}
				}
			}
			require.NoError(t, yaml.Unmarshal(raw, &cfg))
			names := map[string]bool{}
			for _, service := range cfg.Server.Service {
				require.False(t, names[service.Name], "duplicate server service: %s", service.Name)
				names[service.Name] = true
				require.NotContains(t, []int{11404, 11433, 20200, 20201, 20202}, service.Port)
				if _, ok := required[service.Port]; ok {
					require.Equal(t, "127.0.0.1", service.IP)
					require.Equal(t, "trpc", service.Protocol)
					required[service.Port] = true
				}
			}
			for port, found := range required {
				require.True(t, found, "missing native port %d", port)
			}
		})
	}
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
