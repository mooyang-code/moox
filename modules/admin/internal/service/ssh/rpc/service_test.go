package rpc

import (
	"context"
	"mime/multipart"
	"testing"

	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	ssh "github.com/mooyang-code/moox/modules/admin/internal/service/ssh"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/conn"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mocker "github.com/tencent/goom"
	"trpc.group/trpc-go/trpc-go"
)

type fakeSSHService struct {
	ssh.Service
}

func (f *fakeSSHService) CreateSession(ctx context.Context, hostID, clientIP, userID string) (string, error) {
	return "sess-mock", nil
}

func (f *fakeSSHService) CloseHostSessions(string) {}

func (f *fakeSSHService) DisconnectSession(ctx context.Context, sessionID string) error { return nil }
func (f *fakeSSHService) ResizeWindow(ctx context.Context, sessionID string, w, h int) error {
	return nil
}

func (f *fakeSSHService) GetSessionConn(sessionID string) (*conn.SSHConn, bool) { return nil, false }
func (f *fakeSSHService) SessionBelongsToUser(sessionID, userID string) bool    { return false }

func (f *fakeSSHService) SftpList(ctx context.Context, sessionID string, dirPath string) (interface{}, error) {
	return map[string]interface{}{}, nil
}

func (f *fakeSSHService) SftpUpload(ctx context.Context, sessionID string, dstPath string, files []*multipart.FileHeader) ([]string, error) {
	return nil, nil
}

func (f *fakeSSHService) SftpDownload(ctx context.Context, sessionID, filePath string) (*sftp.File, int64, string, error) {
	return nil, 0, "", nil
}

func (f *fakeSSHService) SftpDelete(ctx context.Context, sessionID, path string) error { return nil }
func (f *fakeSSHService) SftpMkdir(ctx context.Context, sessionID, path string) error  { return nil }

func (f *fakeSSHService) GetSessionManager() *conn.SessionManager { return conn.NewSessionManager() }

func newMockSSHService(t *testing.T) ssh.Service {
	t.Helper()
	mock := mocker.Create()
	t.Cleanup(func() { mock.Reset() })

	sshSvc := (ssh.Service)(nil)
	mock.Interface(&sshSvc).Method("CreateSession").Apply(func(_ *mocker.IContext, _ context.Context, hostID, _, _ string) (string, error) {
		if hostID == "" {
			return "", assert.AnError
		}
		return "sess-mock", nil
	})
	return &fakeSSHService{}
}

func TestService_CreateSession_MissingHostID_ShouldReturnInvalidParam(t *testing.T) {
	svc := NewService(&fakeSSHService{})
	rsp, err := svc.CreateSession(context.Background(), &pb.CreateSessionReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestService_CreateSession_ValidHostID_ShouldReturnSession(t *testing.T) {
	svc := NewService(&fakeSSHService{})
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, authmodel.CtxUserID, []byte("user-1"))
	rsp, err := svc.CreateSession(ctx, &pb.CreateSessionReq{HostId: "storage"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "sess-mock", rsp.GetSessionId())
}

func TestService_DisconnectSession_EmptySessionID_ShouldReturnInvalidParam(t *testing.T) {
	svc := NewService(&fakeSSHService{})
	rsp, err := svc.DisconnectSession(context.Background(), &pb.DisconnectSessionReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestService_ResizeWindow_EmptySessionID_ShouldReturnInvalidParam(t *testing.T) {
	svc := NewService(&fakeSSHService{})
	rsp, err := svc.ResizeWindow(context.Background(), &pb.ResizeWindowReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestService_SftpList_MissingParams_ShouldReturnInvalidParam(t *testing.T) {
	svc := NewService(&fakeSSHService{})
	rsp, err := svc.SftpList(context.Background(), &pb.SftpListReq{SessionId: "s1"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestService_SftpMkdir_MissingParams_ShouldReturnInvalidParam(t *testing.T) {
	svc := NewService(&fakeSSHService{})
	rsp, err := svc.SftpMkdir(context.Background(), &pb.SftpMkdirReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestParsePathsPB_AbsolutePath_ShouldBuildBreadcrumbs(t *testing.T) {
	paths := parsePathsPB("/opt/app")
	require.Len(t, paths, 3)
	assert.Equal(t, "app", paths[2].GetName())
}

func TestSftpListResultToPB_MapPayload_ShouldConvert(t *testing.T) {
	data := map[string]interface{}{
		"files": []map[string]interface{}{
			{"path": "/tmp/a", "name": "a", "mode": "drwx", "size": int64(10), "mod_time": "now", "type": "d"},
		},
		"file_count": 0,
		"dir_count":  1,
	}
	rsp := sftpListResultToPB(data, "/tmp")
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetFiles(), 1)
	assert.Equal(t, "a", rsp.GetFiles()[0].GetName())
}

func TestNewMockSSHService_GoomApply_ShouldNotPanic(t *testing.T) {
	_ = newMockSSHService(t)
}
