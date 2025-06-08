package logic

import (
	"context"

	"github.com/mooyang-code/moox/server/internal/service/auth/model"
	"github.com/mooyang-code/moox/server/internal/service/auth/util"
	pb "github.com/mooyang-code/moox/server/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// Register 用户注册
func (s *AuthServiceImpl) Register(ctx context.Context, req *pb.RegisterReq) (*pb.RegisterRsp, error) {
	log.InfoContextf(ctx, "Register called for username: %s", req.Username)

	// 1. 验证输入参数
	if req.Username == "" {
		return &pb.RegisterRsp{
			Code:    pb.EnumErrorCode_INVALID_PARAM,
			Message: "用户名不能为空",
		}, nil
	}

	if req.Password == "" {
		return &pb.RegisterRsp{
			Code:    pb.EnumErrorCode_INVALID_PARAM,
			Message: "密码不能为空",
		}, nil
	}

	// 2. 检查用户名是否已存在
	existingUser, err := s.userDAO.GetUserByUsername(ctx, req.Username)
	if err == nil && existingUser != nil {
		return &pb.RegisterRsp{
			Code:    pb.EnumErrorCode_INVALID_PARAM,
			Message: "用户名已存在",
		}, nil
	}

	// 3. 生成用户ID和密码盐值
	userID := util.GenerateUserID()
	passwordSalt := util.GenerateSalt()
	passwordHash := util.HashPassword(req.Password, passwordSalt)

	// 4. 创建用户对象
	user := &model.User{
		UserID:       userID,
		Username:     req.Username,
		Nickname:     req.Nickname,
		Email:        req.Email,
		PasswordHash: passwordHash,
		Salt:         passwordSalt,
		Status:       int32(pb.UserStatus_ACTIVE), // 默认激活状态
		Role:         int32(pb.UserRole_USER),     // 默认普通用户角色
	}

	// 如果没有提供昵称，使用用户名作为昵称
	if user.Nickname == "" {
		user.Nickname = req.Username
	}

	// 5. 保存到数据库
	err = s.userDAO.CreateUser(ctx, user)
	if err != nil {
		log.ErrorContextf(ctx, "创建用户失败: %v", err)
		return &pb.RegisterRsp{
			Code:    pb.EnumErrorCode_INNER_ERR,
			Message: "用户注册失败",
		}, nil
	}

	// 6. 记录操作日志
	s.recordUserAction(ctx, userID, model.ActionRegister, "", "用户注册成功", "", "", "success")

	// 7. 构造返回的用户信息
	userInfo := &pb.UserInfo{
		UserId:      user.UserID,
		Username:    user.Username,
		Nickname:    user.Nickname,
		Email:       user.Email,
		Avatar:      user.Avatar,
		Status:      pb.UserStatus(user.Status),
		Role:        pb.UserRole(user.Role),
		CreatedAt:   user.CreatedAt.Unix(),
		LastLoginAt: user.LastLoginAt.Unix(),
		LastLoginIp: user.LastLoginIP,
	}

	log.InfoContextf(ctx, "用户注册成功: %s", userID)
	return &pb.RegisterRsp{
		Code:     pb.EnumErrorCode_SUCCESS,
		Message:  "用户注册成功",
		UserId:   userID,
		UserInfo: userInfo,
	}, nil
}

// GetUserInfo 获取用户信息
func (s *AuthServiceImpl) GetUserInfo(ctx context.Context, req *pb.GetUserInfoReq) (*pb.GetUserInfoRsp, error) {
	// 验证访问令牌
	claims, err := util.ParseJWT(req.AccessToken, s.cfg.JWT.SecretKey)
	if err != nil {
		return &pb.GetUserInfoRsp{
			Code:    pb.EnumErrorCode_NO_AUTH,
			Message: "访问令牌无效",
		}, nil
	}

	// 确定要查询的用户ID
	targetUserID := claims.UserID
	if req.UserId != "" {
		// 检查权限：只有管理员可以查询其他用户信息
		if claims.Role < int32(pb.UserRole_ADMIN) {
			return &pb.GetUserInfoRsp{
				Code:    pb.EnumErrorCode_NO_PERMISSION,
				Message: "权限不足",
			}, nil
		}
		targetUserID = req.UserId
	}

	// 查询用户信息
	user, err := s.userDAO.GetUserByID(ctx, targetUserID)
	if err != nil {
		return &pb.GetUserInfoRsp{
			Code:    pb.EnumErrorCode_FIELD_INFO_NOT_EXIST,
			Message: "用户不存在",
		}, nil
	}

	// 记录操作日志
	s.recordUserAction(ctx, claims.UserID, model.ActionGetUserInfo, targetUserID, "获取用户信息", "", "", "success")

	// 构造用户信息
	userInfo := &pb.UserInfo{
		UserId:      user.UserID,
		Username:    user.Username,
		Nickname:    user.Nickname,
		Email:       user.Email,
		Avatar:      user.Avatar,
		Status:      pb.UserStatus(user.Status),
		Role:        pb.UserRole(user.Role),
		CreatedAt:   user.CreatedAt.Unix(),
		LastLoginAt: user.LastLoginAt.Unix(),
		LastLoginIp: user.LastLoginIP,
	}

	return &pb.GetUserInfoRsp{
		Code:     pb.EnumErrorCode_SUCCESS,
		Message:  "获取用户信息成功",
		UserInfo: userInfo,
	}, nil
}

// UpdateUserInfo 更新用户信息
func (s *AuthServiceImpl) UpdateUserInfo(ctx context.Context, req *pb.UpdateUserInfoReq) (*pb.UpdateUserInfoRsp, error) {
	// 验证访问令牌
	claims, err := util.ParseJWT(req.AccessToken, s.cfg.JWT.SecretKey)
	if err != nil {
		return &pb.UpdateUserInfoRsp{
			Code:    pb.EnumErrorCode_NO_AUTH,
			Message: "访问令牌无效",
		}, nil
	}

	// 查询用户信息
	user, err := s.userDAO.GetUserByID(ctx, claims.UserID)
	if err != nil {
		return &pb.UpdateUserInfoRsp{
			Code:    pb.EnumErrorCode_FIELD_INFO_NOT_EXIST,
			Message: "用户不存在",
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
			log.ErrorContextf(ctx, "更新用户信息失败: %v", err)
			return &pb.UpdateUserInfoRsp{
				Code:    pb.EnumErrorCode_INNER_ERR,
				Message: "更新用户信息失败",
			}, nil
		}

		// 重新查询更新后的用户信息
		user, _ = s.userDAO.GetUserByID(ctx, claims.UserID)
	}

	// 记录操作日志
	s.recordUserAction(ctx, user.UserID, model.ActionUpdateProfile, "", "更新用户信息", "", "", "success")

	// 构造用户信息
	userInfo := &pb.UserInfo{
		UserId:      user.UserID,
		Username:    user.Username,
		Nickname:    user.Nickname,
		Email:       user.Email,
		Avatar:      user.Avatar,
		Status:      pb.UserStatus(user.Status),
		Role:        pb.UserRole(user.Role),
		CreatedAt:   user.CreatedAt.Unix(),
		LastLoginAt: user.LastLoginAt.Unix(),
		LastLoginIp: user.LastLoginIP,
	}

	return &pb.UpdateUserInfoRsp{
		Code:     pb.EnumErrorCode_SUCCESS,
		Message:  "更新用户信息成功",
		UserInfo: userInfo,
	}, nil
}
