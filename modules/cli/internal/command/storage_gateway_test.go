package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
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

// 导入结束后用写入身份等指定 View 跟上：View 正在建索引（VIEW_NOT_READY）、还没跟上都在总时长内再等。
func TestStorageImportFinishWaitsForViewApplied(t *testing.T) {
	answers := []*pb.WaitViewAppliedRsp{
		{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_VIEW_NOT_READY}},
		{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}},
		{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Applied: true},
	}
	var calls int
	gateway := storageGatewayCall(func(_ context.Context, service, method string, request, response any) error {
		require.Equal(t, dataViewSvc, service)
		require.Equal(t, "WaitViewApplied", method)
		req := request.(*pb.WaitViewAppliedReq)
		require.Equal(t, "import-role", req.GetAuthInfo().GetAppId())
		require.Equal(t, "bars_view", req.GetViewId())
		proto.Merge(response.(*pb.WaitViewAppliedRsp), answers[calls])
		calls++
		return nil
	})
	target := &importStorageTarget{writer: gatewayStorageDataWriter{Gateway: gateway, SpaceID: "crypto", Auth: &pb.AuthInfo{AppId: "import-role"}}, space: "crypto", view: "bars_view", written: true}
	require.NoError(t, target.Finish(t.Context()))
	require.Equal(t, 3, calls)

	answers = []*pb.WaitViewAppliedRsp{{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NO_PERMISSION, Msg: "无权限"}}}
	calls = 0
	require.ErrorContains(t, target.Finish(t.Context()), "无权限")
	target.view = ""
	require.NoError(t, target.Finish(t.Context()), "没指定 View 时不等")
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

// --view 指向别的数据集的 View 时直接报错，不去等它。
func TestImportViewMustReadTheImportedDataset(t *testing.T) {
	gateway := storageGatewayCall(func(_ context.Context, service, method string, request, response any) error {
		require.Equal(t, metadataServiceName, service)
		require.Equal(t, "GetView", method)
		require.Equal(t, "bars_view", request.(*pb.GetViewReq).GetViewId())
		proto.Merge(response.(*pb.GetViewRsp), &pb.GetViewRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, View: &pb.View{ViewId: "bars_view", DatasetId: "bars"}})
		return nil
	})
	meta := gatewayStorageImportMetadataClient{Gateway: gateway}
	require.NoError(t, checkImportView(t.Context(), meta, "crypto", "bars_view", "bars"))
	require.ErrorContains(t, checkImportView(t.Context(), meta, "crypto", "bars_view", "factors"), "不是导入的数据集")
}
