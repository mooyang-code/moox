package logic

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/auth/model"
	"github.com/mooyang-code/moox/server/internal/service/auth/utils"
	pb "github.com/mooyang-code/moox/server/proto/gen"

	"trpc.group/trpc-go/trpc-go/log"
)

// Login 用户登录
func (s *AuthServiceImpl) Login(ctx context.Context, req *pb.LoginReq) (*pb.LoginRsp, error) {
	log.InfoContextf(ctx, "***** Login username: %s; req: %+v *****", req.Username, req)

	// 0. 验证用户名和密码格式
	if err := s.validateCredentialFormat(req.Username, req.PasswordHash); err != nil {
		return &pb.LoginRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INVALID_PARAM,
				Msg:  err.Error(),
			},
		}, nil
	}

	// 1. 验证盐值和时间戳
	if !s.validateLoginSalt(ctx, req.Username, req.Salt, req.Timestamp) {
		return &pb.LoginRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INVALID_PARAM,
				Msg:  "盐值或时间戳无效，请刷新页面重新登录",
			},
		}, nil
	}

	// 2. 检查登录尝试次数
	if s.isUserLocked(ctx, req.Username, req.ClientIp) {
		return &pb.LoginRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_NO_AUTH,
				Msg:  "账户已被锁定，请稍后再试",
			},
		}, nil
	}

	// 3. 查询用户信息
	user, err := s.userDAO.GetUserByUsername(ctx, req.Username)
	if err != nil {
		s.recordLoginAttempt(ctx, req.Username, req.ClientIp, false)
		return &pb.LoginRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_NO_AUTH,
				Msg:  "用户名或密码错误。",
			},
		}, nil
	}
	log.InfoContextf(ctx, "user Info:%+v", user)

	// 4. 验证密码哈希
	if !utils.ValidateEncryptedPassword(user.PasswordHash, user.Salt, req.Salt, req.Timestamp, req.PasswordHash) {
		s.recordLoginAttempt(ctx, req.Username, req.ClientIp, false)
		return &pb.LoginRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_NO_AUTH,
				Msg:  "用户名或密码错误",
			},
		}, nil
	}

	// 5. 登录成功处理
	s.recordLoginAttempt(ctx, req.Username, req.ClientIp, true)

	// 更新用户登录信息
	s.userDAO.UpdateUserLoginInfo(ctx, user.UserID, req.ClientIp)

	// 记录登录历史
	s.recordLoginHistory(ctx, user, req, model.LoginResultSuccess, "")

	// 生成API访问令牌
	accessToken, err := utils.GenerateAccessToken(
		user.UserID,
		user.Username,
		user.Role,
		s.cfg.JWT.SecretKey,
		s.cfg.JWT.AccessExpired,
	)
	if err != nil {
		log.ErrorContextf(ctx, "生成JWT令牌失败: %v", err)
		return &pb.LoginRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INNER_ERR,
				Msg:  "登录失败",
			},
		}, nil
	}

	// 构造用户信息（安全转义）
	userInfo := utils.BuildSafeUserInfo(user)

	return &pb.LoginRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumMooxErrorCode_SUCCESS,
			Msg:  "登录成功",
		},
		AccessToken: accessToken,
		ExpiresIn:   int64(s.cfg.JWT.AccessExpired.Seconds()),
		UserInfo:    userInfo,
	}, nil
}

// GetLoginSalt 获取登录盐值（前端在请求Login接口前调用本接口）
func (s *AuthServiceImpl) GetLoginSalt(ctx context.Context, req *pb.GetLoginSaltReq) (*pb.GetLoginSaltRsp, error) {
	log.InfoContextf(ctx, "**** GetLoginSalt called for username: %s ****", req.Username)

	// 先尝试获取现有的有效盐值
	existingSalt, err := s.userDAO.GetLoginSalt(ctx, req.Username)
	if err == nil && time.Now().Before(existingSalt.ExpiresAt) {
		// 检查盐值剩余时间，如果太短则重新生成
		remainingTime := time.Until(existingSalt.ExpiresAt)
		minRemainingTime := s.cfg.Security.SaltExpired / 3 // 剩余时间少于1/3时重新生成

		if remainingTime > minRemainingTime {
			// 如果现有盐值还有足够时间，直接返回
			log.InfoContextf(ctx, "返回现有有效盐值 for username: %s, 剩余时间: %v", req.Username, remainingTime)
			return &pb.GetLoginSaltRsp{
				RetInfo: &pb.RetInfo{
					Code: pb.EnumMooxErrorCode_SUCCESS,
					Msg:  "获取盐值成功",
				},
				Salt:      existingSalt.Salt,
				Timestamp: existingSalt.Timestamp,
				ExpiresIn: int64(remainingTime.Seconds()),
			}, nil
		}
		log.InfoContextf(ctx, "现有盐值剩余时间不足，重新生成 for username: %s", req.Username)
	}

	// 生成新的随机盐值和时间戳
	salt := utils.GenerateSalt()
	timestamp := time.Now().Unix()

	// 创建盐值对象
	loginSalt := model.LoginSalt{
		Username:  req.Username,
		Salt:      salt,
		Timestamp: timestamp,
		ExpiresAt: time.Now().Add(s.cfg.Security.SaltExpired),
	}

	// 存储到缓存
	err = s.userDAO.SetLoginSalt(ctx, req.Username, loginSalt)
	if err != nil {
		log.ErrorContextf(ctx, "存储登录盐值失败: %v", err)
		return &pb.GetLoginSaltRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumMooxErrorCode_INNER_ERR,
				Msg:  "获取登录盐值失败",
			},
		}, nil
	}

	log.InfoContextf(ctx, "生成新盐值 for username: %s; loginSalt: %+v; SaltExpired:%d",
		req.Username, loginSalt, int64(s.cfg.Security.SaltExpired.Seconds()))
	return &pb.GetLoginSaltRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumMooxErrorCode_SUCCESS,
			Msg:  "获取盐值成功",
		},
		Salt:      salt,
		Timestamp: timestamp,
		ExpiresIn: int64(s.cfg.Security.SaltExpired.Seconds()),
	}, nil
}

// validateCredentialFormat 验证用户名和密码格式
func (s *AuthServiceImpl) validateCredentialFormat(username, passwordHash string) error {
	// 验证用户名格式
	if err := utils.ValidateStringFormat(username, "用户名"); err != nil {
		return err
	}

	// 验证密码哈希格式（这里验证的是客户端传来的密码哈希）
	// 密码哈希通常是固定长度的十六进制字符串，但为了安全起见，也做基本格式检查
	if passwordHash == "" {
		return fmt.Errorf("密码不能为空")
	}

	// 密码哈希长度检查（通常SHA256哈希是64个字符）
	if len(passwordHash) < 32 || len(passwordHash) > 128 {
		return fmt.Errorf("密码格式无效")
	}
	return nil
}
