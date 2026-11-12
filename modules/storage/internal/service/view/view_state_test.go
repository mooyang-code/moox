package view

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/serviceauth"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 写入方（Collector、moox-cli 导入）只有 Primary 角色身份：GetViewState、WaitViewApplied 接受它，查询仍只接受 View 角色身份。
func TestViewStateAcceptsWriterIdentity(t *testing.T) {
	svc := &Service{authSecret: "view-secret"}
	svc.SetWriterAuthSecret("primary-secret")
	writer := &pb.AuthInfo{AppId: "moox-collector", AppKey: serviceauth.Key("primary-secret", "moox-collector")}
	viewRole := &pb.AuthInfo{AppId: "strategy", AppKey: serviceauth.Key("view-secret", "strategy")}
	stranger := &pb.AuthInfo{AppId: "moox-collector", AppKey: serviceauth.Key("other-secret", "moox-collector")}

	for name, auth := range map[string]*pb.AuthInfo{"writer": writer, "view": viewRole} {
		rsp, err := svc.GetViewState(context.Background(), &pb.GetViewStateReq{AuthInfo: auth, SpaceId: "crypto", ViewId: "bars"})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_FOUND {
			t.Fatalf("%s: GetViewState = %v, %v；应通过鉴权并回答 View 还没开始建索引", name, rsp.GetRetInfo(), err)
		}
	}
	rsp, err := svc.WaitViewApplied(context.Background(), &pb.WaitViewAppliedReq{AuthInfo: writer, SpaceId: "crypto", ViewId: "bars"})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("WaitViewApplied = %v, %v；没有跟随器时应回答 VIEW_NOT_READY", rsp.GetRetInfo(), err)
	}
	if rsp, _ := svc.GetViewState(context.Background(), &pb.GetViewStateReq{AuthInfo: stranger, SpaceId: "crypto", ViewId: "bars"}); rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("其他密钥派生的身份应被拒绝：%v", rsp.GetRetInfo())
	}
	if err := svc.authorize(writer); err == nil {
		t.Fatal("查询接口不应接受写入身份")
	}
}
