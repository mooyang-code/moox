package deploy

import (
	"context"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/release"
)

// Builder 为一组组件准备目标平台的二进制，返回 文件名 → 本机路径。
type Builder interface {
	Build(ctx context.Context, request BuildRequest) (map[string]string, error)
}

// BuildRequest 是一次构建的输入。
type BuildRequest struct {
	Components   []release.Component
	GOOS, GOARCH string
	// SkipBuild 复用仓库 bin/ 中已有的二进制。
	SkipBuild bool
}

// ScriptBuilder 用仓库的构建脚本构建：纯 Go 组件用 scripts/build/build.sh 交叉编译；交叉编译 CGO 组件（存储、
// 因子管理）时在编译主机上构建（scripts/build/build-storage-linux.sh，编译主机取自 moox.toml 的 [compile_host]）。
type ScriptBuilder struct {
	RepositoryRoot string
	// ConfigFile 是 moox.toml 的绝对路径，build-storage-linux.sh 从中读取编译主机的连接信息。
	ConfigFile string
	// CompileHostPassword 是编译主机的 SSH 密码，交给 build-storage-linux.sh。
	CompileHostPassword string
	Out                 io.Writer
}

// Build 构建请求中组件的二进制。
func (b ScriptBuilder) Build(ctx context.Context, request BuildRequest) (map[string]string, error) {
	cross := request.GOOS != runtime.GOOS || request.GOARCH != runtime.GOARCH
	targets, cgoTargets := map[string]bool{}, map[string]bool{}
	for _, component := range request.Components {
		if component.Caddy {
			continue
		}
		if cross && component.CGOTarget != "" {
			cgoTargets[component.CGOTarget] = true
			continue
		}
		for _, target := range component.BuildTargets {
			targets[target] = true
		}
	}
	if !request.SkipBuild {
		for _, target := range sortedSet(targets) {
			if err := b.run(ctx, []string{"TARGET_GOOS=" + request.GOOS, "TARGET_GOARCH=" + request.GOARCH},
				"scripts/build/build.sh", target); err != nil {
				return nil, fmt.Errorf("构建 %s: %w", target, err)
			}
		}
		if len(cgoTargets) > 0 {
			// build-storage-linux.sh 用本机的 moox-cli 读取编译主机的连接信息。
			if err := b.run(ctx, []string{"TARGET_GOOS=" + runtime.GOOS, "TARGET_GOARCH=" + runtime.GOARCH},
				"scripts/build/build.sh", "cli"); err != nil {
				return nil, fmt.Errorf("构建本机 moox-cli: %w", err)
			}
			for _, target := range sortedSet(cgoTargets) {
				env := []string{
					"MOOX_CLI=" + filepath.Join(b.RepositoryRoot, "bin", "moox-cli"),
					"CONFIG=" + b.ConfigFile,
					"MOOX_LINUX_CGO_TARGET=" + target,
					"MOOX_STORAGE_BUILD_HOST_ROLE=compile",
					"MOOX_STORAGE_BUILD_GOARCH=" + request.GOARCH,
					"MOOX_SSH_PASSWORD=" + b.CompileHostPassword,
				}
				if err := b.run(ctx, env, "scripts/build/build-storage-linux.sh"); err != nil {
					return nil, fmt.Errorf("在编译主机上构建 %s: %w", target, err)
				}
			}
		}
	}
	out := map[string]string{}
	for _, component := range request.Components {
		if component.Caddy {
			path, err := FetchCaddy(ctx, b.RepositoryRoot, request.GOARCH)
			if err != nil {
				return nil, err
			}
			out[component.Binary] = path
			continue
		}
		for _, binary := range component.Binaries {
			path := filepath.Join(b.RepositoryRoot, "bin", binary)
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				return nil, fmt.Errorf("缺少二进制 %s（去掉 --skip-build 重新构建）", path)
			}
			// --skip-build 复用 bin/ 里已有的文件，可能是上一次为另一种架构构建的：架构不对的二进制要到安装器启动组件时
			// 才报 exec format error，那时已经停掉了旧组件。
			if request.SkipBuild {
				if err := verifyLinuxBinary(path, request.GOARCH); err != nil {
					return nil, err
				}
			}
			out[binary] = path
		}
	}
	return out, nil
}

// verifyLinuxBinary 确认文件是目标架构的 Linux 可执行文件。
func verifyLinuxBinary(path, goarch string) error {
	file, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("%s 不是 Linux 可执行文件（去掉 --skip-build 重新构建）: %w", path, err)
	}
	defer file.Close()
	want := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[goarch]
	if want == elf.EM_NONE {
		return fmt.Errorf("不支持的目标架构 %q", goarch)
	}
	if file.Machine != want {
		return fmt.Errorf("%s 是 %s 架构的二进制，目标主机需要 %s（去掉 --skip-build 重新构建）", path, file.Machine, want)
	}
	return nil
}

func (b ScriptBuilder) run(ctx context.Context, env []string, script string, args ...string) error {
	command := exec.CommandContext(ctx, "bash", append([]string{filepath.Join(b.RepositoryRoot, script)}, args...)...)
	command.Dir = b.RepositoryRoot
	command.Env = append(os.Environ(), env...)
	out := b.Out
	if out == nil {
		out = os.Stderr
	}
	command.Stdout, command.Stderr = out, out
	return command.Run()
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
