// Package fetchunit 让目标机自己下载部署单元包。目标机并行下载，互不影响；都下载不了时由操作机下载再推送。
package fetchunit

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

//go:embed fetch.py
var fetchScript string

// DefaultConnections 是每台目标机同时下载的连接数：四台各 8 连接时实测有连接拿不到数据。
const DefaultConnections = 4

// UpgradeRateLimit 是升级时的默认限速（字节/秒），避免挤占海外机的实时采集。
const UpgradeRateLimit = 8 << 20

// File 是要下载的一个文件。Name 在发布页上的文件名，SHA256 不带前缀。
type File struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Request 是下发给目标机脚本的请求。URLs 是带 {file} 占位的地址模板。
type Request struct {
	DeployRoot  string   `json:"deploy_root"`
	Release     string   `json:"release"`
	Files       []File   `json:"files"`
	URLs        []string `json:"urls"`
	Connections int      `json:"connections"`
	RateLimit   int64    `json:"rate_limit,omitempty"`
	ProbeOnly   bool     `json:"probe_only,omitempty"`
}

// ErrNoSource 表示目标机上所有下载源都不可用。
var ErrNoSource = errors.New("no_download_source")

// Sources 返回一个发布版本的下载地址模板，CNB 在前：GitHub 在国内主机上基本不通。
func Sources(release string) []string {
	return []string{
		"https://cnb.cool/mooyang-code/moox/-/releases/download/" + release + "/{file}",
		"https://github.com/mooyang-code/moox/releases/download/" + release + "/{file}",
	}
}

// StagingPath 是文件在目标机上的位置。
func StagingPath(deployRoot, sha256, name string) string {
	return path.Join(deployRoot, "staging", sha256, name)
}

// Run 在目标机上运行下载脚本；所有源都失败时返回 ErrNoSource。
func Run(ctx context.Context, transport setupssh.Client, request Request) error {
	if request.Connections == 0 {
		request.Connections = DefaultConnections
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	output, err := transport.Run(ctx, []string{"python3", "-c", fetchScript}, bytes.NewReader(raw))
	if err == nil && output.ExitCode == 0 {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %s", ErrNoSource, lastReason(output.Stdout))
}

// lastReason 取进度输出里的失败原因。
func lastReason(stdout string) string {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var event struct {
			Event  string `json:"event"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(lines[i]), &event) == nil && event.Reason != "" {
			return event.Reason
		}
	}
	return "目标机下载失败"
}

// Reachable 只测下载源，不下载，供预检判断要不要退回本机推送。
func Reachable(ctx context.Context, transport setupssh.Client, request Request) bool {
	request.ProbeOnly = true
	return Run(ctx, transport, request) == nil
}

// Local 在操作机上下载一个文件并校验，结果放在 directory 下。已有且摘要正确时不重新下载。
func Local(ctx context.Context, urls []string, file File, directory string) (string, error) {
	final := filepath.Join(directory, file.SHA256, file.Name)
	if digest, err := fileDigest(final); err == nil && digest == file.SHA256 {
		return final, nil
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return "", err
	}
	client := &http.Client{Timeout: time.Hour}
	var last error
	for _, template := range urls {
		if last = localFetch(ctx, client, strings.ReplaceAll(template, "{file}", file.Name), file, final); last == nil {
			return final, nil
		}
	}
	return "", fmt.Errorf("download_failed: 这台电脑也下载不了 %s: %w", file.Name, last)
}

func localFetch(ctx context.Context, client *http.Client, url string, file File, final string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	temporary, err := os.CreateTemp(filepath.Dir(final), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temporary, digest), response.Body); err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != file.SHA256 {
		return errors.New("摘要不一致")
	}
	if err := errors.Join(temporary.Sync(), temporary.Chmod(0o600), temporary.Close()); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), final)
}

func fileDigest(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
