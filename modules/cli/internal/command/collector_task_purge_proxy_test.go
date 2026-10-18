package command

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

func TestCollectorStorageMetadataProxyClonesListRequests(t *testing.T) {
	for _, method := range []string{"ListDatasets", "ListViews"} {
		t.Run(method, func(t *testing.T) {
			page := &commonpb.Page{Page: 1, Size: 20}
			originalAuth := &storagepb.AuthInfo{AppId: "original"}
			var seenAuth *storagepb.AuthInfo
			transport := storageGatewayCall(func(_ context.Context, service, method string, request, response any) error {
				require.Equal(t, metadataServiceName, service)
				switch req := request.(type) {
				case *storagepb.ListDatasetsReq:
					seenAuth = req.AuthInfo
					req.Page.Page = 99
				case *storagepb.ListViewsReq:
					seenAuth = req.AuthInfo
					req.Page.Page = 99
				default:
					t.Fatalf("unexpected request: %T", req)
				}
				return nil
			})
			proxy := &collectorStorageMetadataProxy{
				gateway: transport, auth: &storagepb.AuthInfo{AppId: "collector"},
			}
			switch method {
			case "ListDatasets":
				req := &storagepb.ListDatasetsReq{SpaceId: "crypto", Page: page, AuthInfo: originalAuth}
				_, err := proxy.ListDatasets(context.Background(), req)
				require.NoError(t, err)
				require.Same(t, originalAuth, req.GetAuthInfo())
			case "ListViews":
				req := &storagepb.ListViewsReq{SpaceId: "crypto", Page: page, AuthInfo: originalAuth}
				_, err := proxy.ListViews(context.Background(), req)
				require.NoError(t, err)
				require.Same(t, originalAuth, req.GetAuthInfo())
			}
			require.Equal(t, uint32(1), page.GetPage(), "transport must not mutate the caller's nested protobuf")
			require.Equal(t, "collector", seenAuth.GetAppId())
		})
	}
}
