package archivetasks

import (
	"context"
	"errors"
	"testing"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

// pagedGateway 按页返回归档任务；每页条数由服务端决定，与请求的 size 无关。
type pagedGateway struct {
	pages    [][]string
	requests []*archivepb.ListArchiveTasksReq
	err      error
}

func (g *pagedGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	if g.err != nil {
		return g.err
	}
	if service != archiveMgrService || method != "ListArchiveTasks" {
		return errors.New("unexpected method")
	}
	request := req.(*archivepb.ListArchiveTasksReq)
	g.requests = append(g.requests, request)
	out := rsp.(*archivepb.ListArchiveTasksRsp)
	out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
	index := int(request.GetPage().GetPage()) - 1
	if index < len(g.pages) {
		for _, dataset := range g.pages[index] {
			out.Tasks = append(out.Tasks, &archivepb.ArchiveTask{SpaceId: request.GetSpaceId(), DatasetId: dataset})
		}
	}
	out.PageResult = &commonpb.PageResult{Page: request.GetPage().GetPage(), HasMore: index+1 < len(g.pages)}
	return nil
}

func TestArchivedDatasetsFollowsHasMore(t *testing.T) {
	gateway := &pagedGateway{pages: [][]string{{"dataset_a", "dataset_b"}, {"dataset_c"}}}
	datasets, err := (&Client{Gateway: gateway}).ArchivedDatasets(context.Background(), "crypto")
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"dataset_a": true, "dataset_b": true, "dataset_c": true}, datasets)
	require.Len(t, gateway.requests, 2)
	for _, request := range gateway.requests {
		require.Equal(t, "crypto", request.GetSpaceId())
	}
}

func TestArchivedDatasetsReportsUnavailableArchive(t *testing.T) {
	_, err := (&Client{Gateway: &pagedGateway{err: errors.New("no route")}}).ArchivedDatasets(context.Background(), "crypto")
	require.ErrorContains(t, err, "查询归档任务失败")
}
