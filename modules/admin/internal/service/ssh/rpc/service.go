// Package rpc 提供 ssh 对外的 trpc 普通 RPC 服务实现，
// 承载 ssh 管理 API（会话/SFTP 操作；主机与凭据随主机登记，由 moox-cli 维护），
// 由控制台进程内 LocalDispatcher 调度。
//
// WebSocket 终端、SFTP 流式上传/下载（multipart）不经本 RPC 服务，
// 由统一网关 rawhandler 分派并在 SSH rawhandler 中校验 session_id 归属。
package rpc

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	ssh "github.com/mooyang-code/moox/modules/admin/internal/service/ssh"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/dao"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// Service 实现 pb.SshService，承载 ssh 管理 API 的业务逻辑。
type Service struct {
	pb.UnimplementedSsh
	svc ssh.Service
}

// NewService 创建 Ssh RPC 实现。
func NewService(svc ssh.Service) *Service {
	return &Service{svc: svc}
}

// CreateSession 创建 SSH 会话。
func (s *Service) CreateSession(ctx context.Context, req *pb.CreateSessionReq) (*pb.CreateSessionRsp, error) {
	if req.GetHostId() == "" {
		return &pb.CreateSessionRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "host_id不能为空")}, nil
	}
	userID := string(trpc.GetMetaData(ctx, auth.CtxUserID))
	if userID == "" {
		return &pb.CreateSessionRsp{RetInfo: retErr(pb.ErrorCode_NO_AUTH, "用户身份验证失败")}, nil
	}
	sessionID, err := s.svc.CreateSession(ctx, req.GetHostId(), "", userID)
	if err != nil {
		log.ErrorContextf(ctx, "[SSH] CreateSession failed: %v", err)
		switch {
		case errors.Is(err, dao.ErrHostNotFound):
			return &pb.CreateSessionRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
		case errors.Is(err, dao.ErrSSHUnavailable):
			return &pb.CreateSessionRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
		}
		return &pb.CreateSessionRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "创建会话失败")}, nil
	}
	return &pb.CreateSessionRsp{RetInfo: retOK(), SessionId: sessionID}, nil
}

// DisconnectSession 断开 SSH 会话。
func (s *Service) DisconnectSession(ctx context.Context, req *pb.DisconnectSessionReq) (*pb.DisconnectSessionRsp, error) {
	if req.GetSessionId() == "" {
		return &pb.DisconnectSessionRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "session_id不能为空")}, nil
	}
	if err := s.svc.DisconnectSession(ctx, req.GetSessionId()); err != nil {
		log.ErrorContextf(ctx, "[SSH] DisconnectSession failed: %v", err)
		return &pb.DisconnectSessionRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "断开会话失败")}, nil
	}
	return &pb.DisconnectSessionRsp{RetInfo: retOK()}, nil
}

// ResizeWindow 调整终端窗口大小。
func (s *Service) ResizeWindow(ctx context.Context, req *pb.ResizeWindowReq) (*pb.ResizeWindowRsp, error) {
	if req.GetSessionId() == "" {
		return &pb.ResizeWindowRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "session_id不能为空")}, nil
	}
	if err := s.svc.ResizeWindow(ctx, req.GetSessionId(), int(req.GetW()), int(req.GetH())); err != nil {
		log.ErrorContextf(ctx, "[SSH] ResizeWindow failed: %v", err)
		return &pb.ResizeWindowRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "调整窗口失败")}, nil
	}
	return &pb.ResizeWindowRsp{RetInfo: retOK()}, nil
}

// SftpList 列出 SFTP 目录。
func (s *Service) SftpList(ctx context.Context, req *pb.SftpListReq) (*pb.SftpListRsp, error) {
	if req.GetSessionId() == "" || req.GetPath() == "" {
		return &pb.SftpListRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "session_id/path不能为空")}, nil
	}
	data, err := s.svc.SftpList(ctx, req.GetSessionId(), req.GetPath())
	if err != nil {
		log.ErrorContextf(ctx, "[SSH] SftpList failed: %v", err)
		return &pb.SftpListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "读取目录失败")}, nil
	}
	return sftpListResultToPB(data, req.GetPath()), nil
}

// SftpMkdir 创建 SFTP 目录。
func (s *Service) SftpMkdir(ctx context.Context, req *pb.SftpMkdirReq) (*pb.SftpMkdirRsp, error) {
	if req.GetSessionId() == "" || req.GetPath() == "" {
		return &pb.SftpMkdirRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "session_id/path不能为空")}, nil
	}
	if err := s.svc.SftpMkdir(ctx, req.GetSessionId(), req.GetPath()); err != nil {
		log.ErrorContextf(ctx, "[SSH] SftpMkdir failed: %v", err)
		return &pb.SftpMkdirRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "创建目录失败")}, nil
	}
	return &pb.SftpMkdirRsp{RetInfo: retOK()}, nil
}

// SftpDelete 删除 SFTP 文件或目录。
func (s *Service) SftpDelete(ctx context.Context, req *pb.SftpDeleteReq) (*pb.SftpDeleteRsp, error) {
	if req.GetSessionId() == "" || req.GetPath() == "" {
		return &pb.SftpDeleteRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "session_id/path不能为空")}, nil
	}
	if err := s.svc.SftpDelete(ctx, req.GetSessionId(), req.GetPath()); err != nil {
		log.ErrorContextf(ctx, "[SSH] SftpDelete failed: %v", err)
		return &pb.SftpDeleteRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "删除失败")}, nil
	}
	return &pb.SftpDeleteRsp{RetInfo: retOK()}, nil
}

func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}
}

func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}

// sftpListResultToPB 将强类型的 SftpListResult 转换为 PB 响应。
func sftpListResultToPB(result *ssh.SftpListResult, currentDir string) *pb.SftpListRsp {
	rsp := &pb.SftpListRsp{RetInfo: retOK(), CurrentDir: currentDir}
	if result == nil {
		return rsp
	}
	if result.CurrentDir != "" {
		rsp.CurrentDir = result.CurrentDir
	}
	rsp.FileCount = int32(result.FileCount)
	rsp.DirCount = int32(result.DirCount)
	rsp.Files = make([]*pb.SftpFileItem, 0, len(result.Files))
	for _, entry := range result.Files {
		rsp.Files = append(rsp.Files, &pb.SftpFileItem{
			Path:       entry.Path,
			Name:       entry.Name,
			Mode:       entry.Mode,
			Size:       entry.Size,
			ModifiedAt: entry.ModifiedAt,
			Type:       entry.Type,
		})
	}
	rsp.Paths = make([]*pb.PathBreadcrumb, 0, len(result.Paths))
	for _, crumb := range result.Paths {
		rsp.Paths = append(rsp.Paths, &pb.PathBreadcrumb{
			Name: crumb.Name,
			Dir:  crumb.Dir,
		})
	}
	return rsp
}
