package command

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type mutatingPurgeListProxy struct {
	storagepb.MetadataClientProxy
	seenAuth *storagepb.AuthInfo
}

func (p *mutatingPurgeListProxy) ListDatasets(_ context.Context, req *storagepb.ListDatasetsReq, _ ...client.Option) (*storagepb.ListDatasetsRsp, error) {
	p.seenAuth = req.GetAuthInfo()
	req.Page.Page = 99
	return &storagepb.ListDatasetsRsp{}, nil
}

func (p *mutatingPurgeListProxy) ListViews(_ context.Context, req *storagepb.ListViewsReq, _ ...client.Option) (*storagepb.ListViewsRsp, error) {
	p.seenAuth = req.GetAuthInfo()
	req.Page.Page = 99
	return &storagepb.ListViewsRsp{}, nil
}

func TestCollectorStorageMetadataProxyClonesListRequests(t *testing.T) {
	for _, method := range []string{"ListDatasets", "ListViews"} {
		t.Run(method, func(t *testing.T) {
			page := &commonpb.Page{Page: 1, Size: 20}
			originalAuth := &storagepb.AuthInfo{AppId: "original"}
			transport := &mutatingPurgeListProxy{}
			proxy := &collectorStorageMetadataProxy{
				proxy: transport, auth: &storagepb.AuthInfo{AppId: "collector"},
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
			require.Equal(t, "collector", transport.seenAuth.GetAppId())
		})
	}
}
