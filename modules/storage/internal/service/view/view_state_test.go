package view

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 还没建索引的 View：GetViewState 回答不在目录中，WaitViewApplied 在没有跟随器时回答未就绪。
func TestViewStateBeforeIndexIsBuilt(t *testing.T) {
	svc := &Service{}
	rsp, err := svc.GetViewState(context.Background(), &pb.GetViewStateReq{SpaceId: "crypto", DatasetId: "dataset_bars"})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_FOUND {
		t.Fatalf("GetViewState = %v, %v；应回答 View 还没开始建索引", rsp.GetRetInfo(), err)
	}
	wait, err := svc.WaitViewApplied(context.Background(), &pb.WaitViewAppliedReq{SpaceId: "crypto", DatasetId: "dataset_bars"})
	if err != nil || wait.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("WaitViewApplied = %v, %v；没有跟随器时应回答 VIEW_NOT_READY", wait.GetRetInfo(), err)
	}
}
