package impl

import (
	"context"

	"github.com/mooyang-code/moox/server/internal/common/crypto"
	"github.com/mooyang-code/moox/server/internal/service/auth/model"
	authutils "github.com/mooyang-code/moox/server/internal/service/auth/utils"
	pb "github.com/mooyang-code/moox/server/proto/gen"

	"trpc.group/trpc-go/trpc-go/log"
)

// Register 用户注册
func (s *AuthServiceImpl) Register(ctx context.Context, req *pb.RegisterReq) (*pb.RegisterRsp, error) {
	log.InfoContextf(ctx, "[Auth] #Register called for username: %s", req.Username)

	// 1. 验证输入参数
	if req.Username == "" {
		return &pb.RegisterRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INVALID_PARAM,
				Msg:  "用户名不能为空",
			},
		}, nil
	}
	if req.Password == "" {
		return &pb.RegisterRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INVALID_PARAM,
				Msg:  "密码不能为空",
			},
		}, nil
	}

	// 2. 检查用户名是否已存在
	existingUser, err := s.userDAO.GetUserByUsername(ctx, req.Username)
	if err == nil && existingUser != nil {
		return &pb.RegisterRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INVALID_PARAM,
				Msg:  "用户名已存在",
			},
		}, nil
	}

	// 3. 生成用户ID和密码盐值
	userID := crypto.GenerateUserID()
	passwordSalt := crypto.GenerateSalt()
	passwordHash := crypto.HashPassword(req.Password, passwordSalt)

	// 4. 创建用户对象
	user := &model.User{
		UserID:       userID,
		Username:     req.Username,
		Nickname:     req.Nickname,
		Email:        req.Email,
		PasswordHash: passwordHash,
		Salt:         passwordSalt,
		Status:       int32(pb.UserStatus_ACTIVE), // 默认激活状态
		Role:         int32(pb.UserRole_ADMIN),    // 默认管理员角色
	}

	// 如果没有提供昵称，使用用户名作为昵称
	if user.Nickname == "" {
		user.Nickname = req.Username
	}
	// 5. 保存到数据库
	err = s.userDAO.CreateUser(ctx, user)
	if err != nil {
		log.ErrorContextf(ctx, "[Auth] 创建用户失败: %v", err)
		return &pb.RegisterRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INNER_ERR,
				Msg:  "用户注册失败",
			},
		}, nil
	}

	// 6. 记录操作日志
	s.recordUserAction(ctx, userID, model.ActionRegister, "", "用户注册成功", "", "", "success")

	log.InfoContextf(ctx, "[Auth] 用户注册成功: %s", userID)
	return &pb.RegisterRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumMooxErrorCode_SUCCESS,
			Msg:  "用户注册成功",
		},
		UserId:   userID,
		UserInfo: authutils.BuildSafeUserInfo(user), // 构造返回的用户信息（安全转义）
	}, nil
}

// GetUserInfo 获取用户信息
func (s *AuthServiceImpl) GetUserInfo(ctx context.Context, req *pb.GetUserInfoReq) (*pb.GetUserInfoRsp, error) {
	log.InfoContextf(ctx, "[Auth] # GetUserInfo enter:%+v", req)

	// 从HTTP header获取用户信息（网关中间件已验证）
	currentUserID, _, role, err := authutils.GetUserInfoFromCtx(ctx)
	if err != nil {
		return &pb.GetUserInfoRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_NO_AUTH,
				Msg:  "用户身份验证失败." + err.Error(),
			},
		}, nil
	}

	// 确定要查询的用户ID
	targetUserID := currentUserID
	if req.UserId != "" {
		// 检查权限：只有管理员可以查询其他用户信息
		if role < int32(pb.UserRole_ADMIN) {
			return &pb.GetUserInfoRsp{
				RetInfo: &pb.RetInfo{
					Code: pb.EnumMooxErrorCode_NO_PERMISSION,
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
				Code: pb.EnumMooxErrorCode_FIELD_INFO_NOT_EXIST,
				Msg:  "用户不存在",
			},
		}, nil
	}

	// 记录操作日志
	s.recordUserAction(ctx, currentUserID, model.ActionGetUserInfo, targetUserID, "获取用户信息", "", "", "success")

	// 构造用户信息（安全转义）
	userInfo := authutils.BuildSafeUserInfo(user)

	return &pb.GetUserInfoRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumMooxErrorCode_SUCCESS,
			Msg:  "获取用户信息成功",
		},
		UserInfo: userInfo,
	}, nil
}

// UpdateUserInfo 更新用户信息
func (s *AuthServiceImpl) UpdateUserInfo(ctx context.Context, req *pb.UpdateUserInfoReq) (*pb.UpdateUserInfoRsp, error) {
	// 从HTTP header获取用户信息（网关中间件已验证）
	currentUserID, _, _, err := authutils.GetUserInfoFromCtx(ctx)
	if err != nil {
		return &pb.UpdateUserInfoRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_NO_AUTH,
				Msg:  "用户身份验证失败:" + err.Error(),
			},
		}, nil
	}

	// 查询用户信息
	user, err := s.userDAO.GetUserByID(ctx, currentUserID)
	if err != nil {
		return &pb.UpdateUserInfoRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_FIELD_INFO_NOT_EXIST,
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
					Code: pb.EnumMooxErrorCode_INNER_ERR,
					Msg:  "更新用户信息失败",
				},
			}, nil
		}

		// 重新查询更新后的用户信息
		user, _ = s.userDAO.GetUserByID(ctx, currentUserID)
	}

	// 记录操作日志
	s.recordUserAction(ctx, user.UserID, model.ActionUpdateProfile, "", "更新用户信息", "", "", "success")

	return &pb.UpdateUserInfoRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumMooxErrorCode_SUCCESS,
			Msg:  "更新用户信息成功",
		},
		UserInfo: authutils.BuildSafeUserInfo(user), // 构造用户信息（安全转义）
	}, nil
}
