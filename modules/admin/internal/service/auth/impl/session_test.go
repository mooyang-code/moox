package impl

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go"
)

func TestLogoutDeletesOnlyCurrentSession(t *testing.T) {
	svc, _, secret := newAuthServiceForLoginTest(t)
	ctx := trpc.BackgroundContext()
	expires := time.Now().Add(time.Hour)
	require.NoError(t, svc.userDAO.SetSigningSession(ctx, model.RequestSigningSession{SessionID: "current", UserID: "user-login-1", EncryptedSecret: "x", ExpiresAt: expires}))
	require.NoError(t, svc.userDAO.SetSigningSession(ctx, model.RequestSigningSession{SessionID: "other", UserID: "user-login-1", EncryptedSecret: "y", ExpiresAt: expires}))
	_ = secret
	trpc.SetMetaData(ctx, model.CtxUserID, []byte("user-login-1"))
	trpc.SetMetaData(ctx, model.CtxSessionID, []byte("current"))
	rsp, err := svc.Logout(ctx, &pb.LogoutReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	_, err = svc.userDAO.GetSigningSession(ctx, "current")
	require.Error(t, err)
	_, err = svc.userDAO.GetSigningSession(ctx, "other")
	require.NoError(t, err)
}

func TestLogoutIgnoresRequestBodyAndRejectsMissingGatewaySession(t *testing.T) {
	svc, _, _ := newAuthServiceForLoginTest(t)
	ctx := trpc.BackgroundContext()
	expires := time.Now().Add(time.Hour)
	require.NoError(t, svc.userDAO.SetSigningSession(ctx, model.RequestSigningSession{SessionID: "victim", UserID: "user-login-1", EncryptedSecret: "x", ExpiresAt: expires}))

	rsp, err := svc.Logout(ctx, &pb.LogoutReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NO_AUTH, rsp.GetRetInfo().GetCode())
	_, err = svc.userDAO.GetSigningSession(ctx, "victim")
	require.NoError(t, err)
}

func TestIssueRawSessionTicketValidatesOperationAndBindsSession(t *testing.T) {
	svc, _, _ := newAuthServiceForLoginTest(t)
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, model.CtxUserID, []byte("user-login-1"))
	trpc.SetMetaData(ctx, model.CtxSessionID, []byte("sid-1"))
	require.NoError(t, svc.userDAO.SetSigningSession(ctx, model.RequestSigningSession{SessionID: "sid-1", UserID: "user-login-1", EncryptedSecret: "encrypted", ExpiresAt: time.Now().Add(time.Hour)}))
	bad, err := svc.IssueRawSessionTicket(ctx, &pb.IssueRawSessionTicketReq{Operation: "shell"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, bad.GetRetInfo().GetCode())
	good, err := svc.IssueRawSessionTicket(ctx, &pb.IssueRawSessionTicketReq{Operation: "sftp_download", SessionId: "ssh-session-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, good.GetRetInfo().GetCode())
	assert.NotEmpty(t, good.GetTicket())
	ticket, err := svc.userDAO.ConsumeRawSessionTicket(ctx, good.GetTicket())
	require.NoError(t, err)
	assert.Equal(t, "user-login-1", ticket.UserID)
	assert.Equal(t, "sid-1", ticket.SessionID)
	assert.Equal(t, "sftp_download", ticket.Operation)
	assert.Equal(t, "ssh-session-1", ticket.ResourceSessionID)
}

func TestIssueRawSessionTicketRejectsMissingTargetSession(t *testing.T) {
	svc, _, _ := newAuthServiceForLoginTest(t)
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, model.CtxUserID, []byte("user-login-1"))
	trpc.SetMetaData(ctx, model.CtxSessionID, []byte("sid-1"))
	require.NoError(t, svc.userDAO.SetSigningSession(ctx, model.RequestSigningSession{SessionID: "sid-1", UserID: "user-login-1", ExpiresAt: time.Now().Add(time.Hour)}))
	rsp, err := svc.IssueRawSessionTicket(ctx, &pb.IssueRawSessionTicketReq{Operation: "ssh_ws"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestIssueRawSessionTicketRejectsMissingSession(t *testing.T) {
	svc, _, _ := newAuthServiceForLoginTest(t)
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, model.CtxUserID, []byte("user-login-1"))
	trpc.SetMetaData(ctx, model.CtxSessionID, []byte("missing"))
	rsp, err := svc.IssueRawSessionTicket(ctx, &pb.IssueRawSessionTicketReq{Operation: "ssh_ws", SessionId: "ssh-session-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NO_AUTH, rsp.GetRetInfo().GetCode())
}

// 归档的两个操作绑定 "<空间>/<数据集>"，格式不对就拒绝签发。
func TestIssueRawSessionTicketBindsArchiveResource(t *testing.T) {
	svc, _, _ := newAuthServiceForLoginTest(t)
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, model.CtxUserID, []byte("user-login-1"))
	trpc.SetMetaData(ctx, model.CtxSessionID, []byte("sid-1"))
	require.NoError(t, svc.userDAO.SetSigningSession(ctx, model.RequestSigningSession{SessionID: "sid-1", UserID: "user-login-1", EncryptedSecret: "encrypted", ExpiresAt: time.Now().Add(time.Hour)}))
	for _, operation := range []string{model.RawOperationArchiveDownload, model.RawOperationArchiveRecent} {
		good, err := svc.IssueRawSessionTicket(ctx, &pb.IssueRawSessionTicketReq{Operation: operation, SessionId: "crypto/dataset_factor_spot_1m"})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, good.GetRetInfo().GetCode())
		ticket, err := svc.userDAO.ConsumeRawSessionTicket(ctx, good.GetTicket())
		require.NoError(t, err)
		assert.Equal(t, operation, ticket.Operation)
		assert.Equal(t, "crypto/dataset_factor_spot_1m", ticket.ResourceSessionID)
		for _, resource := range []string{"crypto", "crypto/", "/ds", "crypto/ds/x", ".state/ds", "crypto/..", "crypto/a\\b"} {
			bad, err := svc.IssueRawSessionTicket(ctx, &pb.IssueRawSessionTicketReq{Operation: operation, SessionId: resource})
			require.NoError(t, err)
			assert.Equal(t, pb.ErrorCode_INVALID_PARAM, bad.GetRetInfo().GetCode(), resource)
		}
	}
}
