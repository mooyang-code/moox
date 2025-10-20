package impl

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/server/internal/common/crypto"
	"github.com/mooyang-code/moox/server/internal/service/auth/model"
	authutils "github.com/mooyang-code/moox/server/internal/service/auth/utils"
	pb "github.com/mooyang-code/moox/server/proto/gen"

	"trpc.group/trpc-go/trpc-go/log"
)

// GetChangePasswordSalt 获取修改密码盐值
func (s *AuthServiceImpl) GetChangePasswordSalt(ctx context.Context, req *pb.GetChangePasswordSaltReq) (*pb.GetChangePasswordSaltRsp, error) {
	// 从HTTP header获取用户信息（网关中间件已验证）
	currentUserID, _, _, err := authutils.GetUserInfoFromCtx(ctx)
	if err != nil {
		return &pb.GetChangePasswordSaltRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_NO_AUTH,
				Msg:  "用户身份验证失败",
			},
		}, nil
	}

	// 生成随机盐值和时间戳
	salt := crypto.GenerateSalt()
	timestamp := time.Now().Unix()

	// 创建盐值对象
	changePwdSalt := model.ChangePasswordSalt{
		UserID:    currentUserID,
		Salt:      salt,
		Timestamp: timestamp,
		ExpiresAt: time.Now().Add(s.cfg.Security.SaltExpired),
	}

	// 存储到缓存
	err = s.userDAO.SetChangePasswordSalt(ctx, currentUserID, changePwdSalt)
	if err != nil {
		log.ErrorContextf(ctx, "[Auth] 存储修改密码盐值失败: %v", err)
		return &pb.GetChangePasswordSaltRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INNER_ERR,
				Msg:  "获取盐值失败",
			},
		}, nil
	}

	return &pb.GetChangePasswordSaltRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumMooxErrorCode_SUCCESS,
			Msg:  "获取盐值成功",
		},
		Salt:      salt,
		Timestamp: timestamp,
		ExpiresIn: int64(s.cfg.Security.SaltExpired.Seconds()),
	}, nil
}

// ChangePassword 修改密码
func (s *AuthServiceImpl) ChangePassword(ctx context.Context, req *pb.ChangePasswordReq) (*pb.ChangePasswordRsp, error) {
	// 从HTTP header获取用户信息（网关中间件已验证）
	currentUserID, _, _, err := authutils.GetUserInfoFromCtx(ctx)
	if err != nil {
		return &pb.ChangePasswordRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_NO_AUTH,
				Msg:  "用户身份验证失败",
			},
		}, nil
	}

	// 验证盐值和时间戳
	if !s.validateChangePasswordSalt(ctx, currentUserID, req.Salt, req.Timestamp) {
		return &pb.ChangePasswordRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INVALID_PARAM,
				Msg:  "盐值或时间戳无效，请刷新页面重新登录！",
			},
		}, nil
	}

	// 查询用户信息
	user, err := s.userDAO.GetUserByID(ctx, currentUserID)
	if err != nil {
		return &pb.ChangePasswordRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_NO_AUTH,
				Msg:  "用户不存在",
			},
		}, nil
	}

	// 验证旧密码
	if !crypto.ValidateEncryptedPassword(ctx, user.PasswordHash, user.Salt, req.Salt, req.Timestamp, req.OldPasswordHash) {
		return &pb.ChangePasswordRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_NO_AUTH,
				Msg:  "旧密码错误",
			},
		}, nil
	}

	// 解密新密码
	newPassword, err := crypto.DecryptPassword(req.NewPasswordHash, req.Salt, req.Timestamp)
	if err != nil {
		log.ErrorContextf(ctx, "[Auth] 解密新密码失败: %v", err)
		return &pb.ChangePasswordRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INVALID_PARAM,
				Msg:  "新密码格式错误",
			},
		}, nil
	}

	// 生成新密码哈希
	newSalt := crypto.GenerateSalt()
	newPasswordHash := crypto.HashPassword(newPassword, newSalt)

	// 更新密码
	err = s.userDAO.UpdateUserPassword(ctx, user.UserID, newPasswordHash, newSalt)
	if err != nil {
		log.ErrorContextf(ctx, "[Auth] 更新密码失败: %v", err)
		return &pb.ChangePasswordRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INNER_ERR,
				Msg:  "修改密码失败",
			},
		}, nil
	}

	// 记录操作日志
	s.recordUserAction(ctx, user.UserID, model.ActionChangePassword, "", "密码修改成功", "", "", "success")

	return &pb.ChangePasswordRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumMooxErrorCode_SUCCESS,
			Msg:  "密码修改成功",
		},
	}, nil
}
