package ssh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/textproto"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeRemote struct {
	createErr, writeErr, closeErr error
	written                       bytes.Buffer
}

type fakeRemoteFile struct{ remote *fakeRemote }

func (f fakeRemoteFile) Write(p []byte) (int, error) {
	if f.remote.writeErr != nil {
		return 0, f.remote.writeErr
	}
	return f.remote.written.Write(p)
}
func (f fakeRemoteFile) Close() error { return f.remote.closeErr }

func (r *fakeRemote) Create(string) (io.WriteCloser, error) {
	if r.createErr != nil {
		return nil, r.createErr
	}
	return fakeRemoteFile{remote: r}, nil
}

func uploadHeader(t *testing.T, name, content string) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, _ = part.Write([]byte(content))
	require.NoError(t, writer.Close())
	form, err := multipart.NewReader(&body, writer.Boundary()).ReadForm(1 << 20)
	require.NoError(t, err)
	return form.File["file"][0]
}

// 创建、写入、关闭任何一步失败都必须返回错误（关闭时才 flush 的错误也算），不能让前端显示“上传成功”。
func TestUploadOneFileReportsEveryRemoteFailure(t *testing.T) {
	ok := &fakeRemote{}
	require.NoError(t, uploadOneFile(context.Background(), ok, "/tmp", uploadHeader(t, "ok.txt", "hello")))
	require.Equal(t, "hello", ok.written.String())
	for name, remote := range map[string]*fakeRemote{
		"创建失败": {createErr: errors.New("permission denied")},
		"写入失败": {writeErr: errors.New("disk full")},
		"关闭失败": {closeErr: errors.New("flush failed")},
	} {
		require.Error(t, uploadOneFile(context.Background(), remote, "/tmp", uploadHeader(t, "f.txt", "x")), name)
	}
}
