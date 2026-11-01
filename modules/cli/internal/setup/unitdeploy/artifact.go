package unitdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/bundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/fetchunit"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
)

// downloads 报告软件由目标机下载：在产物包目录里运行，并且没有指定本机二进制。
func (o DeployOptions) downloads() bool { return o.Bundle != nil && o.BinaryDirectory == "" }

// bundleSoftware 从产物包清单取一个单元的软件记录。Archive 为空表示软件不在这台电脑上。
func bundleSoftware(manifest *bundle.Manifest, profile, arch string) (unitpackage.Result, error) {
	var result unitpackage.Result
	if arch != "amd64" {
		return result, fmt.Errorf("target_arch_unsupported: 发布只有 linux/amd64，目标机是 %s", arch)
	}
	artifact, ok := manifest.Units[profile]
	if !ok {
		return result, fmt.Errorf("bundle_incomplete: 产物包 %s 没有 %s 单元", manifest.Release, profile)
	}
	if err := json.Unmarshal(artifact.Manifest, &result.Manifest); err != nil || result.Manifest.Profile != profile {
		return result, fmt.Errorf("bundle_incomplete: 产物包 %s 的 %s 单元清单无效", manifest.Release, profile)
	}
	result.SHA256, result.RemoteFile, result.Size, result.Release = "sha256:"+artifact.SHA256, artifact.File, artifact.Size, manifest.Release
	return result, nil
}

// packageHelperSHA 从 host 包清单取运行助手的摘要。
func packageHelperSHA(host unitpackage.Result) (string, error) {
	i := slices.IndexFunc(host.Manifest.Files, func(file unitpackage.File) bool { return file.Path == "bin/moox-runtime" })
	if i < 0 {
		return "", errors.New("host software has no runtime helper")
	}
	return strings.TrimPrefix(host.Manifest.Files[i].SHA256, "sha256:"), nil
}

// artifactPath 是软件包在目标机上的位置，下载和本机推送用同一路径。
func artifactPath(deployRoot string, item unitpackage.Result) string {
	name := item.RemoteFile
	if name == "" {
		name = item.Manifest.Profile + ".tar.gz"
	}
	return fetchunit.StagingPath(deployRoot, strings.TrimPrefix(item.SHA256, "sha256:"), name)
}

// artifactDownload 记下一次下载要用的参数。
type artifactDownload struct {
	options DeployOptions
	root    string // 部署根
	limit   int64
}

// ensureArtifact 确认目标机上有这个软件包，没有就让目标机下载。所有源都不通时由这台电脑下载再推送。
func ensureArtifact(ctx context.Context, transport setupssh.Client, download artifactDownload, item unitpackage.Result) (string, error) {
	remote := artifactPath(download.root, item)
	digest := strings.TrimPrefix(item.SHA256, "sha256:")
	if item.Archive != "" {
		return remote, uploadFile(ctx, transport, download.root, item.Archive, remote, digest, 0o600)
	}
	file := fetchunit.File{Name: item.RemoteFile, SHA256: digest, Size: item.Size}
	urls := fetchunit.Sources(item.Release)
	err := fetchunit.Run(ctx, transport, fetchunit.Request{DeployRoot: download.root, Release: item.Release, Files: []fetchunit.File{file}, URLs: urls, RateLimit: download.limit})
	if err == nil || !errors.Is(err, fetchunit.ErrNoSource) {
		return remote, err
	}
	if download.options.Log != nil {
		fmt.Fprintf(download.options.Log, "目标机下载 %s 失败（%v），改由这台电脑下载后推送\n", item.RemoteFile, err)
	}
	local, err := fetchunit.Local(ctx, urls, file, filepath.Join(download.options.StateDirectory, "downloads"))
	if err != nil {
		return remote, err
	}
	return remote, uploadFile(ctx, transport, download.root, local, remote, digest, 0o600)
}

// helperExtractScript 从目标机上的 host 包取出运行助手，校验通过才放到位。
const helperExtractScript = `set -eu
temporary="$3.tmp.$$"
trap 'rm -f -- "$temporary"' EXIT
tar -xzOf "$1" bin/moox-runtime > "$temporary"
echo "$2  $temporary" | sha256sum -c - >/dev/null
chmod 0700 "$temporary"
mv -f -- "$temporary" "$3"`

// ensureHelper 确认目标机上有运行助手：本机有就推送，否则从目标机的 host 包取出。
func ensureHelper(ctx context.Context, transport setupssh.Client, root, local, hostArchive, digest string) (string, error) {
	remote := path.Join(root, "bootstrap-input/runtime", digest, "moox-runtime")
	if local != "" {
		return remote, uploadFile(ctx, transport, root, local, remote, digest, 0o700)
	}
	present, err := remotePresent(ctx, transport, root, remote, digest, 0o700)
	if err != nil || present {
		return remote, err
	}
	if output, err := transport.Run(ctx, []string{"sh", "-c", helperExtractScript, "moox-helper", hostArchive, digest, remote}, nil); err != nil {
		return remote, remoteFailure("取出运行助手", err, output)
	}
	return remote, nil
}
