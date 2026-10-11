package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/require"
)

type pathCapture struct {
	fakeSSHService
	path string
}

func (p *pathCapture) SftpDownload(_ context.Context, _, filePath string) (*sftp.File, int64, string, error) {
	p.path = filePath
	return nil, 0, "", http.ErrMissingFile // 只关心收到的路径，随后按失败返回
}

// 前端对路径做一次 encodeURIComponent，服务端只解码一次：+、%、%2F 必须原样往返。
func TestSftpDownloadHandlerDecodesPathOnce(t *testing.T) {
	for _, original := range []string{"/tmp/a+b.txt", "/tmp/100%.txt", "/tmp/a%2Fb.txt", "/tmp/空 格.txt"} {
		svc := &pathCapture{}
		request := httptest.NewRequest(http.MethodGet, "/x?session_id=s&path="+url.QueryEscape(original), nil)
		SftpDownloadHandler(svc)(httptest.NewRecorder(), request)
		require.Equal(t, original, svc.path)
	}
}
