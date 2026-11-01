package fetchunit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/require"
)

// localTransport 把“目标机”命令放到本机运行。
type localTransport struct{ setupssh.Client }

func (localTransport) Run(ctx context.Context, argv []string, stdin io.Reader) (setupssh.Result, error) {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdin = stdin
	var stdout bytes.Buffer
	command.Stdout = &stdout
	err := command.Run()
	code := 0
	if exit := (&exec.ExitError{}); errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	return setupssh.Result{Stdout: stdout.String(), ExitCode: code}, err
}

func fixture(t *testing.T, size int) ([]byte, File) {
	t.Helper()
	body := bytes.Repeat([]byte("moox"), size/4)
	digest := sha256.Sum256(body)
	return body, File{Name: "unit.tar.gz", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}
}

func rangeServer(t *testing.T, body []byte, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		http.ServeContent(w, r, "unit", time.Time{}, bytes.NewReader(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func deadServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDownloadsWithRangeAndVerifies(t *testing.T) {
	body, file := fixture(t, 40<<20)
	root := t.TempDir()
	request := Request{DeployRoot: root, Release: "v1", Files: []File{file}, URLs: []string{rangeServer(t, body, nil).URL + "/{file}"}}
	require.NoError(t, Run(t.Context(), localTransport{}, request))
	raw, err := os.ReadFile(StagingPath(root, file.SHA256, file.Name))
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, raw))
	info, err := os.Stat(StagingPath(root, file.SHA256, file.Name))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestExistingFileIsNotDownloadedAgain(t *testing.T) {
	body, file := fixture(t, 1<<20)
	root := t.TempDir()
	var hits atomic.Int64
	request := Request{DeployRoot: root, Release: "v1", Files: []File{file}, URLs: []string{rangeServer(t, body, &hits).URL + "/{file}"}}
	require.NoError(t, Run(t.Context(), localTransport{}, request))
	hits.Store(0)
	require.NoError(t, Run(t.Context(), localTransport{}, request))
	require.Zero(t, hits.Load())
}

func TestFallsBackToSecondSourceOnBadDigest(t *testing.T) {
	body, file := fixture(t, 1<<20)
	corrupt := bytes.Clone(body)
	corrupt[10] ^= 0xff
	root := t.TempDir()
	request := Request{DeployRoot: root, Release: "v1", Files: []File{file}, URLs: []string{
		rangeServer(t, corrupt, nil).URL + "/{file}", rangeServer(t, body, nil).URL + "/{file}",
	}}
	require.NoError(t, Run(t.Context(), localTransport{}, request))
	raw, err := os.ReadFile(StagingPath(root, file.SHA256, file.Name))
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, raw))
}

func TestAllSourcesFailing(t *testing.T) {
	_, file := fixture(t, 1<<20)
	root := t.TempDir()
	request := Request{DeployRoot: root, Release: "v1", Files: []File{file}, URLs: []string{deadServer(t).URL + "/{file}"}}
	require.ErrorIs(t, Run(t.Context(), localTransport{}, request), ErrNoSource)
	require.NoFileExists(t, StagingPath(root, file.SHA256, file.Name))
	require.False(t, Reachable(t.Context(), localTransport{}, request))
}

func TestProbeChangesNothingOnTarget(t *testing.T) {
	body, file := fixture(t, 1<<20)
	root := filepath.Join(t.TempDir(), "moox")
	request := Request{DeployRoot: root, Release: "v1", Files: []File{file}, URLs: []string{rangeServer(t, body, nil).URL + "/{file}"}}
	require.True(t, Reachable(t.Context(), localTransport{}, request))
	require.NoDirExists(t, root, "预检不能改目标机")
	require.True(t, LocalReachable(t.Context(), request.URLs, file.Name))
	require.False(t, LocalReachable(t.Context(), []string{deadServer(t).URL + "/{file}"}, file.Name))
}

func TestWrongDigestNeverLandsInStaging(t *testing.T) {
	body, file := fixture(t, 1<<20)
	file.SHA256 = strings.Repeat("0", 64)
	root := t.TempDir()
	request := Request{DeployRoot: root, Release: "v1", Files: []File{file}, URLs: []string{rangeServer(t, body, nil).URL + "/{file}"}}
	require.ErrorIs(t, Run(t.Context(), localTransport{}, request), ErrNoSource)
	require.NoFileExists(t, StagingPath(root, file.SHA256, file.Name))
}

func TestKeepsOnlyTwoReleases(t *testing.T) {
	root := t.TempDir()
	for _, release := range []string{"v1", "v2", "v3"} {
		body, file := fixture(t, 1<<20)
		body = append(body, release...)
		digest := sha256.Sum256(body)
		file.SHA256, file.Size = hex.EncodeToString(digest[:]), int64(len(body))
		request := Request{DeployRoot: root, Release: release, Files: []File{file}, URLs: []string{rangeServer(t, body, nil).URL + "/{file}"}}
		require.NoError(t, Run(t.Context(), localTransport{}, request))
		time.Sleep(20 * time.Millisecond)
	}
	entries, err := os.ReadDir(filepath.Join(root, "staging"))
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestLocalDownloadVerifiesAndCaches(t *testing.T) {
	body, file := fixture(t, 1<<20)
	directory := t.TempDir()
	var hits atomic.Int64
	urls := []string{deadServer(t).URL + "/{file}", rangeServer(t, body, &hits).URL + "/{file}"}
	path, err := Local(t.Context(), urls, file, directory)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, raw))
	_, err = Local(t.Context(), urls, file, directory)
	require.NoError(t, err)
	require.EqualValues(t, 1, hits.Load())
	file.SHA256 = strings.Repeat("0", 64)
	_, err = Local(t.Context(), urls, file, directory)
	require.ErrorContains(t, err, "download_failed")
}

func TestScriptIsASCII(t *testing.T) {
	for _, r := range fetchScript {
		require.Less(t, r, rune(128), "python3 -c 在 C 语言环境下可能读不了非 ASCII 内容")
	}
}
