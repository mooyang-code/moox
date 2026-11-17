package rpc

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

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

func uploadRequest(t *testing.T, query, formSession string) *http.Request {
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
	return request
}

// 控制台入口按地址栏的 session_id 核对会话归属，上传也只认地址栏这一份：表单里的同名字段不能换成别人的会话。
func TestSftpUploadUsesQuerySessionOnly(t *testing.T) {
	svc := &uploadCapture{}
	recorder := httptest.NewRecorder()
	SftpUploadHandler(svc)(recorder, uploadRequest(t, "?session_id=mine", "someone-else"))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "mine", svc.sessionID)
	require.Equal(t, "/tmp", svc.path)

	svc = &uploadCapture{}
	recorder = httptest.NewRecorder()
	SftpUploadHandler(svc)(recorder, uploadRequest(t, "", "someone-else"))
	require.Equal(t, http.StatusBadRequest, recorder.Code, "地址栏没有 session_id 就拒绝，不退到表单字段")
	require.Empty(t, svc.sessionID)
}
