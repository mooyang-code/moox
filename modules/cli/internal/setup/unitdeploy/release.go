package unitdeploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
)

// 版本制品：软件按版本保存在私有状态目录的 releases/<版本 ID>/ 下，与身份类状态（runtime 身份、收据、导出材料）分开。
// 升级已安装单元或部署新主机时显式选择版本；不带版本时仍使用首次初始化的软件（修复原发布）。

// releaseProfiles 是版本制品包含的单元；storage 需要 CGO 二进制，只在提供时加入。
var releaseProfiles = []string{"host", "control", "access", "egress-proxy", "trade"}

var releaseIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,39}$`)

// Release 是一个版本的软件制品清单（releases/<ID>/release.json）。
type Release struct {
	Version      int                           `json:"version"`
	ID           string                        `json:"id"`
	SourceCommit string                        `json:"source_commit"`
	Architecture string                        `json:"architecture"`
	Packages     map[string]unitpackage.Result `json:"packages"`
	Helper       string                        `json:"helper"`
	HelperSHA256 string                        `json:"helper_sha256"`
}

// ReleaseOptions 是构建版本制品的参数。
type ReleaseOptions struct {
	RepositoryRoot string
	StateDirectory string
	ReleaseID      string
	Architecture   string
	// BinaryDirectory 可选：已为目标平台构建好的纯 Go 二进制目录；为空时在本机构建。
	BinaryDirectory string
	// StorageBinaryDirectory 可选：Storage 三个 CGO 服务与 storage CLI 的 Linux 二进制目录；提供时版本包含 storage 单元。
	StorageBinaryDirectory string
	Log                    io.Writer

	// commit 读取来源提交，默认 cleanCommit；测试可替换。
	commit func(context.Context, string) (string, error)
}

func validReleaseID(id string) bool { return releaseIDPattern.MatchString(id) }

func releaseName(id string) string { return filepath.Join("releases", id, "release.json") }

// BuildRelease 构建（或复用）一个版本制品。同一 ID 已存在时独立校验后复用，来源提交不同则拒绝；工作区必须干净。
func BuildRelease(ctx context.Context, options ReleaseOptions) (Release, error) {
	var release Release
	if !validReleaseID(options.ReleaseID) {
		return release, errors.New("release ID must be 1-40 lowercase letters, digits, dots or dashes")
	}
	if options.Architecture != "amd64" && options.Architecture != "arm64" {
		return release, errors.New("release architecture must be amd64 or arm64")
	}
	if options.commit == nil {
		options.commit = cleanCommit
	}
	commit, err := options.commit(ctx, options.RepositoryRoot)
	if err != nil {
		return release, err
	}
	root, err := privateRoot(options.StateDirectory)
	if err != nil {
		return release, err
	}
	defer root.Close()
	if existing, exists, err := readRelease(ctx, root, options.ReleaseID, options.Architecture); err != nil {
		return release, err
	} else if exists {
		if existing.SourceCommit != commit {
			return release, fmt.Errorf("release %s was built from %s, not the current commit %s", options.ReleaseID, existing.SourceCommit, commit)
		}
		return existing, nil
	}
	if err := root.MkdirAll(filepath.Join("releases", options.ReleaseID), 0o700); err != nil {
		return release, err
	}
	work, err := os.MkdirTemp(filepath.Join(root.Name(), "releases", options.ReleaseID), "build-")
	if err != nil {
		return release, err
	}
	bin := options.BinaryDirectory
	if bin == "" {
		bin = filepath.Join(work, "bin")
		if err := buildCoreLocally(ctx, options.RepositoryRoot, bin, options.Architecture, options.Log); err != nil {
			return release, err
		}
		if err := buildLocalTargets(ctx, options.RepositoryRoot, bin, options.Architecture, options.Log, []string{"access", "egress-proxy", "trade"}); err != nil {
			return release, err
		}
	}
	release = Release{Version: 1, ID: options.ReleaseID, SourceCommit: commit, Architecture: options.Architecture, Packages: map[string]unitpackage.Result{}}
	profiles := slices.Clone(releaseProfiles)
	if options.StorageBinaryDirectory != "" {
		profiles = append(profiles, "storage")
	}
	for _, profile := range profiles {
		source := bin
		if profile == "storage" {
			source = options.StorageBinaryDirectory
		}
		result, err := unitpackage.Package(ctx, unitpackage.Options{RepositoryRoot: options.RepositoryRoot, BinaryDirectory: source, Output: filepath.Join(work, profile+".tar.gz"), Profile: profile, GOOS: "linux", GOARCH: options.Architecture})
		if err != nil {
			return Release{}, fmt.Errorf("package %s: %w", profile, err)
		}
		release.Packages[profile] = result
	}
	helperSHA, err := fileSHA(filepath.Join(bin, "moox-runtime"))
	if err != nil {
		return Release{}, err
	}
	if !packageHasHelper(release.Packages["host"], helperSHA) {
		return Release{}, errors.New("runtime helper does not match the host package")
	}
	raw, err := os.ReadFile(filepath.Join(bin, "moox-runtime"))
	if err != nil {
		return Release{}, err
	}
	helperName := filepath.Join("releases", options.ReleaseID, filepath.Base(work), "moox-runtime")
	if err := fsutil.WritePrivate(root, helperName, raw, false); err != nil {
		return Release{}, err
	}
	release.Helper, release.HelperSHA256 = filepath.Join(root.Name(), helperName), helperSHA
	if err := os.Chmod(release.Helper, 0o700); err != nil {
		return Release{}, err
	}
	// 助手是 0700 可执行文件，不能用私有文件树同步；与 prepareCoreSoftware 一样逐个同步软件包、助手与目录。
	names := []string{release.Helper, work, filepath.Dir(work)}
	for _, item := range release.Packages {
		names = append(names, item.Archive)
	}
	for _, name := range names {
		file, err := os.Open(name)
		if err != nil {
			return Release{}, err
		}
		syncErr := file.Sync()
		file.Close()
		if syncErr != nil {
			return Release{}, syncErr
		}
	}
	if err := saveJSON(root, releaseName(options.ReleaseID), release, false); err != nil {
		return Release{}, err
	}
	return release, nil
}

// readRelease 读取并独立校验一个版本制品：每个软件包重新检查摘要、单元与平台，助手摘要与 host 包内的一致。
func readRelease(ctx context.Context, root *os.Root, id, arch string) (Release, bool, error) {
	var release Release
	if !validReleaseID(id) {
		return release, false, errors.New("invalid release ID")
	}
	if _, err := root.Lstat(releaseName(id)); os.IsNotExist(err) {
		return release, false, nil
	} else if err != nil {
		return release, false, err
	}
	if err := readJSON(root, releaseName(id), 4<<20, &release); err != nil {
		return release, true, err
	}
	if release.Version != 1 || release.ID != id || release.Architecture != arch || release.SourceCommit == "" || release.Packages["host"].Archive == "" || release.Packages["control"].Archive == "" {
		return release, true, errors.New("release manifest does not match this target")
	}
	for profile, item := range release.Packages {
		if !insideState(root.Name(), item.Archive) {
			return release, true, errors.New("release artifact is outside private deployment state")
		}
		verified, err := unitpackage.Inspect(ctx, item.Archive)
		if err != nil || verified.SHA256 != item.SHA256 || verified.Manifest.Profile != profile || verified.Manifest.GOOS != "linux" || verified.Manifest.GOARCH != arch {
			return release, true, fmt.Errorf("release %s %s artifact failed independent verification", id, profile)
		}
		release.Packages[profile] = verified
	}
	if !insideState(root.Name(), release.Helper) {
		return release, true, errors.New("release helper is outside private deployment state")
	}
	digest, err := fileSHA(release.Helper)
	if err != nil || digest != release.HelperSHA256 || !packageHasHelper(release.Packages["host"], digest) {
		return release, true, errors.New("release runtime helper changed or differs from its host package")
	}
	return release, true, nil
}

// ReadRelease 供命令读取已有版本（只读校验）。
func ReadRelease(ctx context.Context, stateDirectory, id, arch string) (Release, error) {
	root, err := fsutil.OpenPhysicalRoot(stateDirectory, true)
	if err != nil {
		return Release{}, err
	}
	defer root.Close()
	release, exists, err := readRelease(ctx, root, id, arch)
	if err == nil && !exists {
		err = fmt.Errorf("release %s does not exist; build it with setup release build", id)
	}
	return release, err
}

func packageHasHelper(host unitpackage.Result, digest string) bool {
	return slices.ContainsFunc(host.Manifest.Files, func(file unitpackage.File) bool {
		return file.Path == "bin/moox-runtime" && file.SHA256 == "sha256:"+digest
	})
}

// cleanCommit 返回仓库当前提交；工作区有未提交改动时拒绝，保证版本制品能对应到一个提交。
func cleanCommit(ctx context.Context, repository string) (string, error) {
	status, err := exec.CommandContext(ctx, "git", "-C", repository, "status", "--porcelain").Output()
	if err != nil {
		return "", errors.New("release build requires a git worktree")
	}
	if len(bytes.TrimSpace(status)) != 0 {
		return "", errors.New("release build requires a clean worktree; commit or stash local changes first")
	}
	head, err := exec.CommandContext(ctx, "git", "-C", repository, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", errors.New("cannot read the source commit")
	}
	return strings.TrimSpace(string(head)), nil
}
