package rpc

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	"github.com/stretchr/testify/require"
)

type uploadCapture struct {
	fakeSSHService
	sessionID, path string
}

func (u *uploadCapture) SftpUpload(_ context.Context, sessionID, dstPath string, files []*multipart.FileHeader) ([]string, error) {
	u.sessionID, u.path = sessionID, dstPath
	return []string{files[0].Filename}, nil
}

func uploadRequest(t *testing.T, query, formSession, userID string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if formSession != "" {
		require.NoError(t, writer.WriteField("session_id", formSession))
	}
	require.NoError(t, writer.WriteField("path", "/tmp"))
	part, err := writer.CreateFormFile("file", "a.txt")
	require.NoError(t, err)
	_, _ = part.Write([]byte("hello"))
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, "/api/admin/ssh/SftpUpload"+query, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if userID != "" {
		request = request.WithContext(context.WithValue(request.Context(), auth.CtxUserID, userID))
	}
	return request
}

// 上传只认地址栏 session_id 且校验归属于当前登录用户：表单里的同名字段不能换成别人的会话，不属于当前用户时返回 403。
func TestSftpUploadUsesQuerySessionAndVerifiesOwner(t *testing.T) {
	svc := &uploadCapture{}
	recorder := httptest.NewRecorder()
	SftpUploadHandler(svc)(recorder, uploadRequest(t, "?session_id=mine", "someone-else", "u1"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "mine", svc.sessionID)
	require.Equal(t, "/tmp", svc.path)

	svc = &uploadCapture{}
	recorder = httptest.NewRecorder()
	SftpUploadHandler(svc)(recorder, uploadRequest(t, "", "someone-else", "u1"))
	require.Equal(t, http.StatusForbidden, recorder.Code, "地址栏没有 session_id 就拒绝，不退到表单字段")
	require.Empty(t, svc.sessionID)

	svc = &uploadCapture{}
	recorder = httptest.NewRecorder()
	SftpUploadHandler(svc)(recorder, uploadRequest(t, "?session_id=someone-else", "", "u1"))
	require.Equal(t, http.StatusForbidden, recorder.Code, "不属于当前登录用户的 session_id 返回 403")
	require.Empty(t, svc.sessionID)
}
