package impl

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"trpc.group/trpc-go/trpc-go"
)

var rawSessionOperations = map[string]struct{}{
	"ssh_ws": {}, "sftp_download": {}, "sftp_upload": {},
	model.RawOperationArchiveDownload: {}, model.RawOperationArchiveRecent: {},
}

// validRawResource 校验 ticket 绑定的资源：SSH 类是 SSH 会话 ID；归档类是 "<空间>/<数据集>"，恰好一个斜杠，两段非空、不以点开头。
func validRawResource(operation, resource string) bool {
	if resource == "" {
		return false
	}
	if operation != model.RawOperationArchiveDownload && operation != model.RawOperationArchiveRecent {
		return true
	}
	space, dataset, ok := strings.Cut(resource, "/")
	return ok && model.ValidArchiveResourcePart(space) && model.ValidArchiveResourcePart(dataset)
}

func (s *AuthServiceImpl) Logout(ctx context.Context, req *pb.LogoutReq) (*pb.LogoutRsp, error) {
	sid := string(trpc.GetMetaData(ctx, model.CtxSessionID))
	userID := string(trpc.GetMetaData(ctx, model.CtxUserID))
	if sid == "" || userID == "" {
		return &pb.LogoutRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NO_AUTH, Msg: "用户身份验证失败"}}, nil
	}
	session, err := s.userDAO.GetSigningSession(ctx, sid)
	if err != nil || session.UserID != userID {
		return &pb.LogoutRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NO_AUTH, Msg: "用户身份验证失败"}}, nil
	}
	if err := s.userDAO.DeleteSigningSession(ctx, sid); err != nil {
		return &pb.LogoutRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INNER_ERR, Msg: "退出失败"}}, nil
	}
	return &pb.LogoutRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "退出成功"}}, nil
}

func (s *AuthServiceImpl) IssueRawSessionTicket(ctx context.Context, req *pb.IssueRawSessionTicketReq) (*pb.IssueRawSessionTicketRsp, error) {
	if _, ok := rawSessionOperations[req.GetOperation()]; !ok {
		return &pb.IssueRawSessionTicketRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "不支持的操作"}}, nil
	}
	if req.GetSessionId() == "" {
		return &pb.IssueRawSessionTicketRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "session_id不能为空"}}, nil
	}
	if !validRawResource(req.GetOperation(), req.GetSessionId()) {
		return &pb.IssueRawSessionTicketRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "归档操作的 session_id 必须是 <空间>/<数据集>"}}, nil
	}
	userID := string(trpc.GetMetaData(ctx, model.CtxUserID))
	sid := string(trpc.GetMetaData(ctx, model.CtxSessionID))
	if userID == "" || sid == "" {
		return &pb.IssueRawSessionTicketRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NO_AUTH, Msg: "用户身份验证失败"}}, nil
	}
	session, err := s.userDAO.GetSigningSession(ctx, sid)
	if err != nil || session.UserID != userID || !time.Now().Before(session.ExpiresAt) {
		return &pb.IssueRawSessionTicketRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NO_AUTH, Msg: "用户身份验证失败"}}, nil
	}
	ticketID, err := mooxsecurity.RandomHex(32)
	if err != nil {
		return &pb.IssueRawSessionTicketRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INNER_ERR, Msg: "生成 ticket 失败"}}, nil
	}
	expiresAt := time.Now().Add(s.cfg.Security.RawTicketTTL)
	ticket := model.RawSessionTicket{TicketID: ticketID, SessionID: sid, ResourceSessionID: req.GetSessionId(), UserID: userID, Operation: req.GetOperation(), ExpiresAt: expiresAt}
	if err := s.userDAO.SetRawSessionTicket(ctx, ticket); err != nil {
		return &pb.IssueRawSessionTicketRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INNER_ERR, Msg: "生成 ticket 失败"}}, nil
	}
	return &pb.IssueRawSessionTicketRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "生成 ticket 成功"}, Ticket: ticketID, ExpiresAt: expiresAt.Unix()}, nil
}
