package logic

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/auth/model"
	"github.com/mooyang-code/moox/server/internal/service/auth/util"
	pb "github.com/mooyang-code/moox/server/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// GetChangePasswordSalt 获取修改密码盐值
func (s *AuthServiceImpl) GetChangePasswordSalt(ctx context.Context, req *pb.GetChangePasswordSaltReq) (*pb.GetChangePasswordSaltRsp, error) {
	// 验证访问令牌
	claims, err := util.ParseJWT(req.AccessToken, s.cfg.JWT.SecretKey)
	if err != nil {
		return &pb.GetChangePasswordSaltRsp{
			Code:    pb.EnumMooxErrorCode_NO_AUTH,
			Message: "访问令牌无效",
		}, nil
	}

	// 生成随机盐值和时间戳
	salt := util.GenerateSalt()
	timestamp := time.Now().Unix()

	// 创建盐值对象
	changePwdSalt := model.ChangePasswordSalt{
		UserID:    claims.UserID,
		Salt:      salt,
		Timestamp: timestamp,
		ExpiresAt: time.Now().Add(s.cfg.Security.SaltExpired),
	}

	// 存储到缓存
	err = s.userDAO.SetChangePasswordSalt(ctx, claims.UserID, changePwdSalt)
	if err != nil {
		log.ErrorContextf(ctx, "存储修改密码盐值失败: %v", err)
		return &pb.GetChangePasswordSaltRsp{
			Code:    pb.EnumMooxErrorCode_INNER_ERR,
			Message: "获取盐值失败",
		}, nil
	}

	return &pb.GetChangePasswordSaltRsp{
		Code:      pb.EnumMooxErrorCode_SUCCESS,
		Message:   "获取盐值成功",
		Salt:      salt,
		Timestamp: timestamp,
		ExpiresIn: int64(s.cfg.Security.SaltExpired.Seconds()),
	}, nil
}

// ChangePassword 修改密码
func (s *AuthServiceImpl) ChangePassword(ctx context.Context, req *pb.ChangePasswordReq) (*pb.ChangePasswordRsp, error) {
	// 验证访问令牌
	claims, err := util.ParseJWT(req.AccessToken, s.cfg.JWT.SecretKey)
	if err != nil {
		return &pb.ChangePasswordRsp{
			Code:    pb.EnumMooxErrorCode_NO_AUTH,
			Message: "访问令牌无效",
		}, nil
	}

	// 验证盐值和时间戳
	if !s.validateChangePasswordSalt(ctx, claims.UserID, req.Salt, req.Timestamp) {
		return &pb.ChangePasswordRsp{
			Code:    pb.EnumMooxErrorCode_INVALID_PARAM,
			Message: "盐值或时间戳无效",
		}, nil
	}

	// 查询用户信息
	user, err := s.userDAO.GetUserByID(ctx, claims.UserID)
	if err != nil {
		return &pb.ChangePasswordRsp{
			Code:    pb.EnumMooxErrorCode_NO_AUTH,
			Message: "用户不存在",
		}, nil
	}

	// 验证旧密码
	if !util.ValidateEncryptedPassword(user.PasswordHash, user.Salt, req.Salt, req.Timestamp, req.OldPasswordHash) {
		return &pb.ChangePasswordRsp{
			Code:    pb.EnumMooxErrorCode_NO_AUTH,
			Message: "旧密码错误",
		}, nil
	}

	// 解密新密码
	newPassword, err := util.DecryptPassword(req.NewPasswordHash, req.Salt, req.Timestamp)
	if err != nil {
		log.ErrorContextf(ctx, "解密新密码失败: %v", err)
		return &pb.ChangePasswordRsp{
			Code:    pb.EnumMooxErrorCode_INVALID_PARAM,
			Message: "新密码格式错误",
		}, nil
	}

	// 生成新密码哈希
	newSalt := util.GenerateSalt()
	newPasswordHash := util.HashPassword(newPassword, newSalt)

	// 更新密码
	err = s.userDAO.UpdateUserPassword(ctx, user.UserID, newPasswordHash, newSalt)
	if err != nil {
		log.ErrorContextf(ctx, "更新密码失败: %v", err)
		return &pb.ChangePasswordRsp{
			Code:    pb.EnumMooxErrorCode_INNER_ERR,
			Message: "修改密码失败",
		}, nil
	}

	// 记录操作日志
	s.recordUserAction(ctx, user.UserID, model.ActionChangePassword, "", "密码修改成功", "", "", "success")

	return &pb.ChangePasswordRsp{
		Code:    pb.EnumMooxErrorCode_SUCCESS,
		Message: "密码修改成功",
	}, nil
}
