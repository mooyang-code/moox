package placement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type invocation func(context.Context, string, string, any, any) error

func (f invocation) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	return f(ctx, service, method, req, rsp)
}

func TestClientSourceOnlyUsesV2AndReadsAllPages(t *testing.T) {
	var methods []string
	gateway := invocation(func(ctx context.Context, service, method string, request, response any) error {
		require.Equal(t, "trpc.moox.ops.SysDeploy", service)
		methods = append(methods, method)
		switch method {
		case "GetCatalog":
			raw := servicecatalog.EmbeddedYAML()
			sum := sha256.Sum256(raw)
			*response.(*adminpb.GetCatalogRsp) = adminpb.GetCatalogRsp{RetInfo: &commonpb.RetInfo{}, CatalogYaml: string(raw), Sha256: hex.EncodeToString(sum[:])}
		case "ListHosts":
			page := request.(*adminpb.ListDeploymentHostsReq).GetPage().GetPage()
			*response.(*adminpb.ListDeploymentHostsRsp) = adminpb.ListDeploymentHostsRsp{RetInfo: &commonpb.RetInfo{}, Hosts: []*adminpb.DeploymentHost{{HostId: fmt.Sprint(page)}}, PageResult: &commonpb.PageResult{HasMore: page < 2}}
		case "ListPlacements":
			page := request.(*adminpb.ListPlacementsReq).GetPage().GetPage()
			*response.(*adminpb.ListPlacementsRsp) = adminpb.ListPlacementsRsp{RetInfo: &commonpb.RetInfo{}, Placements: []*adminpb.ComponentPlacement{{HostId: fmt.Sprint(page)}}, PageResult: &commonpb.PageResult{HasMore: page < 2}}
		default:
			t.Fatalf("unexpected discovery RPC %s", method)
		}
		return nil
	})
	snapshot, err := NewClientSource(gateway).Snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, snapshot.Hosts, 2)
	require.Len(t, snapshot.Placements, 2)
	require.Equal(t, []string{"GetCatalog", "ListHosts", "ListHosts", "ListPlacements", "ListPlacements"}, methods)
}

func TestClientSourceRejectsUnverifiedCatalogAndFailedResponses(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *adminpb.GetCatalogRsp
	}{
		{"missing_status", &adminpb.GetCatalogRsp{}},
		{"unavailable", &adminpb.GetCatalogRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR}}},
		{"hash_mismatch", &adminpb.GetCatalogRsp{RetInfo: &commonpb.RetInfo{}, CatalogYaml: string(servicecatalog.EmbeddedYAML()), Sha256: "invalid"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			_, err := NewClientSource(invocation(func(_ context.Context, _ string, _ string, _, response any) error {
				calls++
				proto.Merge(response.(proto.Message), test.response)
				return nil
			})).Snapshot(t.Context())
			require.Error(t, err)
			require.Equal(t, 1, calls)
		})
	}
}
