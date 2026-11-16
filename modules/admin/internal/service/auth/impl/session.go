package impl

import (
	"context"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"trpc.group/trpc-go/trpc-go"
)

// Logout 删除当前会话；会话 ID 由控制台按 Cookie 查到后写进 metadata，请求体里没有。Cookie 由控制台清除。
func (s *AuthServiceImpl) Logout(ctx context.Context, _ *pb.LogoutReq) (*pb.LogoutRsp, error) {
	sid := string(trpc.GetMetaData(ctx, model.CtxSessionID))
	if sid == "" {
		return &pb.LogoutRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NO_AUTH, Msg: "用户身份验证失败"}}, nil
	}
	s.sessions.Delete(sid)
	return &pb.LogoutRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "退出成功"}}, nil
}
