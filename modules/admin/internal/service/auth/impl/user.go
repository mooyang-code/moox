package impl

import (
	"context"

	authutils "github.com/mooyang-code/moox/modules/admin/internal/service/auth/utils"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go/log"
)

// GetUserInfo 获取当前登录用户的信息。
func (s *AuthServiceImpl) GetUserInfo(ctx context.Context, req *pb.GetUserInfoReq) (*pb.GetUserInfoRsp, error) {
	log.InfoContext(ctx, "[Auth] # GetUserInfo")

	currentUserID, _, err := authutils.GetUserInfoFromCtx(ctx)
	if err != nil {
		return &pb.GetUserInfoRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.ErrorCode_NO_AUTH,
				Msg:  "用户身份验证失败." + err.Error(),
			},
		}, nil
	}

	user, err := s.userDAO.GetUserByID(ctx, currentUserID)
	if err != nil {
		return &pb.GetUserInfoRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.ErrorCode_NOT_FOUND,
				Msg:  "用户不存在",
			},
		}, nil
	}

	return &pb.GetUserInfoRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.ErrorCode_SUCCESS,
			Msg:  "获取用户信息成功",
		},
		UserInfo: authutils.BuildSafeUserInfo(user),
	}, nil
}
