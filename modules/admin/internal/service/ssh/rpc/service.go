// Package rpc 提供 ssh 对外的 trpc 普通 RPC 服务实现，
// 承载 ssh 管理 API（主机配置/会话/SFTP 操作/会话管理），
// 由统一 HTTP 转发层（/api/admin/ssh/{method}）调度。
//
// WebSocket 终端、SFTP 流式上传/下载（multipart）不经本 RPC 服务，
// 由统一网关 rawhandler 分派（session_id 鉴权）。
package rpc

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	ssh "github.com/mooyang-code/moox/modules/admin/internal/service/ssh"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/conn"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

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

// ========== 主机配置 ==========

// ListHosts 列出 SSH 主机。
func (s *Service) ListHosts(ctx context.Context, req *pb.ListHostsReq) (*pb.ListHostsRsp, error) {
	hosts, total, err := s.svc.ListHosts(ctx, req.GetKeyword(), int(req.GetOffset()), int(req.GetLimit()))
	if err != nil {
		log.ErrorContextf(ctx, "[SSH] ListHosts failed: %v", err)
		return &pb.ListHostsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询主机列表失败")}, nil
	}
	pbHosts := make([]*pb.SSHHost, 0, len(hosts))
	for i := range hosts {
		pbHosts = append(pbHosts, sshHostModelToPB(&hosts[i]))
	}
	return &pb.ListHostsRsp{
		RetInfo: retOK(),
		Hosts:   pbHosts,
		Total:   total,
	}, nil
}

// CreateHost 创建 SSH 主机。
func (s *Service) CreateHost(ctx context.Context, req *pb.CreateHostReq) (*pb.CreateHostRsp, error) {
	host := sshHostPBToModel(req.GetHost())
	if host == nil {
		return &pb.CreateHostRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "主机配置不能为空")}, nil
	}
	if err := s.svc.CreateHost(ctx, host); err != nil {
		log.ErrorContextf(ctx, "[SSH] CreateHost failed: %v", err)
		return &pb.CreateHostRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "创建主机失败")}, nil
	}
	return &pb.CreateHostRsp{RetInfo: retOK(), Id: int32(host.ID)}, nil
}

// UpdateHost 更新 SSH 主机。
func (s *Service) UpdateHost(ctx context.Context, req *pb.UpdateHostReq) (*pb.UpdateHostRsp, error) {
	host := sshHostPBToModel(req.GetHost())
	if host == nil || host.ID == 0 {
		return &pb.UpdateHostRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "主机ID不能为空")}, nil
	}
	if err := s.svc.UpdateHost(ctx, host); err != nil {
		log.ErrorContextf(ctx, "[SSH] UpdateHost failed: %v", err)
		return &pb.UpdateHostRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "更新主机失败")}, nil
	}
	return &pb.UpdateHostRsp{RetInfo: retOK()}, nil
}

// DeleteHost 删除 SSH 主机。
func (s *Service) DeleteHost(ctx context.Context, req *pb.DeleteHostReq) (*pb.DeleteHostRsp, error) {
	if req.GetId() == 0 {
		return &pb.DeleteHostRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "主机ID不能为空")}, nil
	}
	if err := s.svc.DeleteHost(ctx, int(req.GetId())); err != nil {
		log.ErrorContextf(ctx, "[SSH] DeleteHost failed: %v", err)
		return &pb.DeleteHostRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "删除主机失败")}, nil
	}
	return &pb.DeleteHostRsp{RetInfo: retOK()}, nil
}

// GetHost 获取 SSH 主机详情。
func (s *Service) GetHost(ctx context.Context, req *pb.GetHostReq) (*pb.GetHostRsp, error) {
	if req.GetId() == 0 {
		return &pb.GetHostRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "主机ID不能为空")}, nil
	}
	host, err := s.svc.GetHost(ctx, int(req.GetId()))
	if err != nil {
		log.ErrorContextf(ctx, "[SSH] GetHost failed: %v", err)
		return &pb.GetHostRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询主机失败")}, nil
	}
	return &pb.GetHostRsp{RetInfo: retOK(), Host: sshHostModelToPB(host)}, nil
}

// ========== SSH 会话 ==========

// CreateSession 创建 SSH 会话。
func (s *Service) CreateSession(ctx context.Context, req *pb.CreateSessionReq) (*pb.CreateSessionRsp, error) {
	if req.GetHostId() == 0 {
		return &pb.CreateSessionRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "host_id不能为空")}, nil
	}
	clientIP := extractClientIP(ctx)
	sessionID, err := s.svc.CreateSession(ctx, int(req.GetHostId()), clientIP)
	if err != nil {
		log.ErrorContextf(ctx, "[SSH] CreateSession failed: %v", err)
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

// ExecCommand 执行命令。
func (s *Service) ExecCommand(ctx context.Context, req *pb.ExecCommandReq) (*pb.ExecCommandRsp, error) {
	if req.GetSessionId() == "" || req.GetCmd() == "" {
		return &pb.ExecCommandRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "session_id/cmd不能为空")}, nil
	}
	output, err := s.svc.ExecCommand(ctx, req.GetSessionId(), req.GetCmd())
	if err != nil {
		log.ErrorContextf(ctx, "[SSH] ExecCommand failed: %v", err)
		return &pb.ExecCommandRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "执行命令失败")}, nil
	}
	return &pb.ExecCommandRsp{RetInfo: retOK(), Output: output}, nil
}

// ========== SFTP 操作 ==========

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

// ========== 会话管理 ==========

// GetOnlineSessions 获取在线会话列表。
func (s *Service) GetOnlineSessions(ctx context.Context, req *pb.GetOnlineSessionsReq) (*pb.GetOnlineSessionsRsp, error) {
	sessions := s.svc.GetOnlineSessions(ctx)
	pbSessions := make([]*pb.SessionInfo, 0, len(sessions))
	for i := range sessions {
		pbSessions = append(pbSessions, sessionInfoToPB(&sessions[i]))
	}
	return &pb.GetOnlineSessionsRsp{RetInfo: retOK(), Sessions: pbSessions}, nil
}

// ForceDisconnect 强制断开会话。
func (s *Service) ForceDisconnect(ctx context.Context, req *pb.ForceDisconnectReq) (*pb.ForceDisconnectRsp, error) {
	if req.GetSessionId() == "" {
		return &pb.ForceDisconnectRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "session_id不能为空")}, nil
	}
	if err := s.svc.ForceDisconnect(ctx, req.GetSessionId()); err != nil {
		log.ErrorContextf(ctx, "[SSH] ForceDisconnect failed: %v", err)
		return &pb.ForceDisconnectRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "强制断开失败")}, nil
	}
	return &pb.ForceDisconnectRsp{RetInfo: retOK()}, nil
}

// ========== 辅助 ==========

// retOK 成功 RetInfo。
func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}
}

// retErr 错误 RetInfo。
func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}

// extractClientIP 从 ctx 提取客户端IP（网关 authorize 注入 metadata 或 fallback）。
func extractClientIP(ctx context.Context) string {
	// 网关 dispatcher 未统一注入 client_ip metadata，此处返回空由 service 兜底
	return ""
}

// sshHostModelToPB model.SSHHost → pb.SSHHost。
func sshHostModelToPB(h *model.SSHHost) *pb.SSHHost {
	if h == nil {
		return nil
	}
	return &pb.SSHHost{
		Id:          int32(h.ID),
		Name:        h.Name,
		Address:     h.Address,
		Port:        int32(h.Port),
		User:        h.User,
		Password:    h.Password,
		AuthType:    h.AuthType,
		NetType:     h.NetType,
		CertData:    h.CertData,
		CertPwd:     h.CertPwd,
		FontSize:    int32(h.FontSize),
		Background:  h.Background,
		Foreground:  h.Foreground,
		CursorColor: h.CursorColor,
		FontFamily:  h.FontFamily,
		CursorStyle: h.CursorStyle,
		Shell:       h.Shell,
		PtyType:     h.PtyType,
		InitCmd:     h.InitCmd,
		Creator:     h.Creator,
		CreateTime:  formatTime(h.CreateTime),
		ModifyTime:  formatTime(h.ModifyTime),
	}
}

// sshHostPBToModel pb.SSHHost → model.SSHHost。
func sshHostPBToModel(h *pb.SSHHost) *model.SSHHost {
	if h == nil {
		return nil
	}
	return &model.SSHHost{
		ID:          int(h.GetId()),
		Name:        h.GetName(),
		Address:     h.GetAddress(),
		Port:        int(h.GetPort()),
		User:        h.GetUser(),
		Password:    h.GetPassword(),
		AuthType:    h.GetAuthType(),
		NetType:     h.GetNetType(),
		CertData:    h.GetCertData(),
		CertPwd:     h.GetCertPwd(),
		FontSize:    int(h.GetFontSize()),
		Background:  h.GetBackground(),
		Foreground:  h.GetForeground(),
		CursorColor: h.GetCursorColor(),
		FontFamily:  h.GetFontFamily(),
		CursorStyle: h.GetCursorStyle(),
		Shell:       h.GetShell(),
		PtyType:     h.GetPtyType(),
		InitCmd:     h.GetInitCmd(),
	}
}

// sessionInfoToPB conn.SessionInfo → pb.SessionInfo。
func sessionInfoToPB(s *conn.SessionInfo) *pb.SessionInfo {
	if s == nil {
		return nil
	}
	return &pb.SessionInfo{
		SessionId:      s.SessionID,
		HostId:         int32(s.HostID),
		HostName:       s.HostName,
		Address:        s.Address,
		Port:           int32(s.Port),
		User:           s.User,
		ClientIp:       s.ClientIP,
		StartTime:      s.StartTime,
		LastActiveTime: s.LastActiveTime,
	}
}

// sftpListResultToPB 将 SftpList 返回的 map[string]interface{} 转换为 PB。
// service 层 SftpList 当前返回 map，此处做兼容性转换。
func sftpListResultToPB(data interface{}, currentDir string) *pb.SftpListRsp {
	rsp := &pb.SftpListRsp{RetInfo: retOK(), CurrentDir: currentDir}
	m, ok := data.(map[string]interface{})
	if !ok {
		return rsp
	}
	if v, ok := m["files"].([]map[string]interface{}); ok {
		for _, f := range v {
			item := &pb.SftpFileItem{}
			if s, ok := f["path"].(string); ok {
				item.Path = s
			}
			if s, ok := f["name"].(string); ok {
				item.Name = s
			}
			if s, ok := f["mode"].(string); ok {
				item.Mode = s
			}
			if s, ok := f["size"].(int64); ok {
				item.Size = s
			} else if s, ok := f["size"].(int); ok {
				item.Size = int64(s)
			}
			if s, ok := f["mod_time"].(string); ok {
				item.ModTime = s
			}
			if s, ok := f["type"].(string); ok {
				item.Type = s
			}
			rsp.Files = append(rsp.Files, item)
		}
	}
	if v, ok := m["file_count"].(int); ok {
		rsp.FileCount = int32(v)
	}
	if v, ok := m["dir_count"].(int); ok {
		rsp.DirCount = int32(v)
	}
	if v, ok := m["paths"].([]map[string]string); ok {
		for _, p := range v {
			rsp.Paths = append(rsp.Paths, &pb.PathBreadcrumb{
				Name: p["name"],
				Dir:  p["dir"],
			})
		}
	} else {
		rsp.Paths = parsePathsPB(currentDir)
	}
	return rsp
}

// parsePathsPB 复刻 impl.parsePaths，将目录路径解析为面包屑。
func parsePathsPB(dirPath string) []*pb.PathBreadcrumb {
	parts := strings.Split(dirPath, "/")
	var dirs []string
	if strings.HasPrefix(dirPath, "/") {
		dirs = append(dirs, "/")
	}
	for _, item := range parts {
		name := strings.TrimSpace(item)
		if len(name) > 0 {
			dirs = append(dirs, name)
		}
	}
	var paths []*pb.PathBreadcrumb
	for i, item := range dirs {
		fullPath := path.Join(dirs[:i+1]...)
		paths = append(paths, &pb.PathBreadcrumb{Name: item, Dir: fullPath})
	}
	return paths
}

// formatTime 格式化 time.Time 为字符串。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

// ensure fmt used (避免 import 未使用报错占位，后续若 extractClientIP 扩展可移除)
var _ = fmt.Sprintf
