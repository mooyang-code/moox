package view

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestRemoteMetadataListViewsByDatasetUsesActivePagedContract(t *testing.T) {
	proxy := &listViewsMetadataProxy{}
	metadata := &RemoteMetadata{proxy: proxy}

	views, err := metadata.ListViewsByDataset(context.Background(), "crypto", "kline")
	if err != nil {
		t.Fatalf("ListViewsByDataset: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("views = %d, want both pages", len(views))
	}
	if len(proxy.requests) != 2 {
		t.Fatalf("requests = %d, want two pages", len(proxy.requests))
	}
	for i, req := range proxy.requests {
		if req.GetStatus() != "active" || req.GetPage().GetPage() != uint32(i+1) || req.GetPage().GetSize() != 1000 {
			t.Fatalf("request[%d] = %+v", i, req)
		}
	}
}

type listViewsMetadataProxy struct {
	pb.MetadataClientProxy
	requests []*pb.ListViewsReq
}

func (p *listViewsMetadataProxy) ListViews(_ context.Context, req *pb.ListViewsReq, _ ...client.Option) (*pb.ListViewsRsp, error) {
	p.requests = append(p.requests, req)
	pageNo := req.GetPage().GetPage()
	return &pb.ListViewsRsp{
		RetInfo:    &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
		Views:      []*pb.View{{SpaceId: req.GetSpaceId(), ViewId: "view-" + string(rune('0'+pageNo)), Status: "active"}},
		PageResult: &pb.PageResult{Page: pageNo, Size: 1000, HasMore: pageNo == 1},
	}, nil
}
