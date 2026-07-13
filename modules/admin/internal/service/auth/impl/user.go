package impl

import (
	"context"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	authutils "github.com/mooyang-code/moox/modules/admin/internal/service/auth/utils"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	mooxcrypto "github.com/mooyang-code/moox/packages/crypto"

	"trpc.group/trpc-go/trpc-go/log"
)

// GetUserInfo 获取用户信息
func (s *AuthServiceImpl) GetUserInfo(ctx context.Context, req *pb.GetUserInfoReq) (*pb.GetUserInfoRsp, error) {
	log.InfoContextf(ctx, "[Auth] # GetUserInfo enter:%+v", req)

	// 优先使用网关注入的用户上下文；兼容网关纯 HTTP 转发时仅在请求体传入 access_token 的场景。
	currentUserID, _, role, err := s.getUserInfoCaller(ctx, req)
	if err != nil {
		return &pb.GetUserInfoRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.ErrorCode_NO_AUTH,
				Msg:  "用户身份验证失败." + err.Error(),
			},
		}, nil
	}

	// 确定要查询的用户ID
	targetUserID := currentUserID
	if req.UserId != "" {
		// 检查权限：只有管理员可以查询其他用户信息
		if role < int32(pb.UserRole_USER_ROLE_ADMIN) {
			return &pb.GetUserInfoRsp{
				RetInfo: &pb.RetInfo{
					Code: pb.ErrorCode_NO_PERMISSION,
					Msg:  "权限不足",
				},
			}, nil
		}
		targetUserID = req.UserId
	}

	// 查询用户信息
	user, err := s.userDAO.GetUserByID(ctx, targetUserID)
	if err != nil {
		return &pb.GetUserInfoRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.ErrorCode_NOT_FOUND,
				Msg:  "用户不存在",
			},
		}, nil
	}

	// 记录操作日志
	s.logUserAction(ctx, currentUserID, model.ActionGetUserInfo, targetUserID, "获取用户信息", "", "", "success")

	// 构造用户信息（安全转义）
	userInfo := authutils.BuildSafeUserInfo(user)

	return &pb.GetUserInfoRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.ErrorCode_SUCCESS,
			Msg:  "获取用户信息成功",
		},
		UserInfo: userInfo,
	}, nil
}

func (s *AuthServiceImpl) getUserInfoCaller(ctx context.Context, req *pb.GetUserInfoReq) (userID string, username string, role int32, err error) {
	userID, username, role, err = authutils.GetUserInfoFromCtx(ctx)
	if err == nil {
		return userID, username, role, nil
	}
	if req.GetAccessToken() == "" {
		return "", "", 0, err
	}
	if s == nil || s.cfg == nil || s.cfg.JWT.SecretKey == "" {
		return "", "", 0, err
	}
	claims, tokenErr := mooxcrypto.ParseToken(req.GetAccessToken(), s.cfg.JWT.SecretKey)
	if tokenErr != nil {
		return "", "", 0, tokenErr
	}
	if claims["iss"] != "moox-admin" || claims["token_type"] != "access" {
		return "", "", 0, err
	}
	claimUserID, ok := claims["user_id"].(string)
	if !ok || claimUserID == "" {
		return "", "", 0, err
	}
	claimUsername, _ := claims["username"].(string)
	claimRole, _ := claims["role"].(float64)
	return claimUserID, claimUsername, int32(claimRole), nil
}

// UpdateUserInfo 更新用户信息
func (s *AuthServiceImpl) UpdateUserInfo(ctx context.Context, req *pb.UpdateUserInfoReq) (*pb.UpdateUserInfoRsp, error) {
	// 从HTTP header获取用户信息（网关中间件已验证）
	currentUserID, _, _, err := authutils.GetUserInfoFromCtx(ctx)
	if err != nil {
		return &pb.UpdateUserInfoRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.ErrorCode_NO_AUTH,
				Msg:  "用户身份验证失败:" + err.Error(),
			},
		}, nil
	}

	// 查询用户信息
	user, err := s.userDAO.GetUserByID(ctx, currentUserID)
	if err != nil {
		return &pb.UpdateUserInfoRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.ErrorCode_NOT_FOUND,
				Msg:  "用户不存在",
			},
		}, nil
	}

	// 更新用户信息
	updateData := map[string]interface{}{}
	if req.Nick != "" {
		updateData["c_nickname"] = req.Nick
	}
	if req.Email != "" {
		updateData["c_email"] = req.Email
	}
	if req.Avatar != "" {
		updateData["c_avatar"] = req.Avatar
	}

	if len(updateData) > 0 {
		err = s.userDAO.UpdateUser(ctx, user.UserID, updateData)
		if err != nil {
			log.ErrorContextf(ctx, "[Auth] 更新用户信息失败: %v", err)
			return &pb.UpdateUserInfoRsp{
				RetInfo: &pb.RetInfo{
					Code: pb.ErrorCode_INNER_ERR,
					Msg:  "更新用户信息失败",
				},
			}, nil
		}

		// 重新查询更新后的用户信息
		user, _ = s.userDAO.GetUserByID(ctx, currentUserID)
	}

	// 记录操作日志
	s.logUserAction(ctx, user.UserID, model.ActionUpdateProfile, "", "更新用户信息", "", "", "success")

	return &pb.UpdateUserInfoRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.ErrorCode_SUCCESS,
			Msg:  "更新用户信息成功",
		},
		UserInfo: authutils.BuildSafeUserInfo(user), // 构造用户信息（安全转义）
	}, nil
}
