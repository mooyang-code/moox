package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/mooyang-code/moox/packages/factorudf"
)

// runCargo 在目录里执行 cargo，输出接到 out；失败返回错误。
func runCargo(dir string, out io.Writer, args ...string) error {
	return runCargoEnv(dir, out, nil, args...)
}

func runCargoEnv(dir string, out io.Writer, env []string, args ...string) error {
	cmd := exec.Command("cargo", args...)
	cmd.Dir = dir
	// 去掉会改变构建结果或产物位置的继承环境：产物目录固定在工作区的 target，rustflags 只用 build 自己编码的那一份。
	base := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		switch strings.SplitN(kv, "=", 2)[0] {
		case "RUSTFLAGS", "CARGO_ENCODED_RUSTFLAGS", "CARGO_TARGET_DIR", "CARGO_BUILD_TARGET_DIR", "CARGO_BUILD_RUSTFLAGS", "RUSTC_WRAPPER", "RUSTC_WORKSPACE_WRAPPER":
			continue
		}
		base = append(base, kv)
	}
	cmd.Env = append(append(base, "CARGO_TARGET_DIR="+filepath.Join(dir, "target")), env...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cargo %s：%w", strings.Join(args, " "), err)
	}
	return nil
}

func commandOutput(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s：%w（%s）", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

type udfBuildOptions struct {
	Dir  string // 工作区根
	Name string // crate 名；工作区只有一个 crate 时可省略
	JSON bool   // 输出机器可读的报告
	// SkipNativeTests 为真时不运行 cargo test（原生环境的单元测试与 fixtures）。
	SkipNativeTests bool
}

// udfBuildReport 是 udf build 的机器可读结果。
type udfBuildReport struct {
	Name     string             `json:"name"`
	Hash     string             `json:"hash"`
	Package  string             `json:"package"`
	Bytes    int                `json:"wasm_bytes"`
	Source   string             `json:"source"`
	Commit   string             `json:"commit"`
	Dirty    bool               `json:"dirty"`
	Report   *factorudf.Report  `json:"report"`
	Manifest factorudf.Manifest `json:"manifest"`
}

// 工作区里 crate 的目录：udfs/<名称>。
func udfCrates(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "udfs"))
	if err != nil {
		return nil, fmt.Errorf("%s 不是 UDF 工作区（没有 udfs/ 目录）：先 moox-cli udf new <名称>", root)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

var (
	unsafeRE     = regexp.MustCompile(`\bunsafe\b|\ballow\s*\([^)]*unsafe_code`)
	exportAttrRE = regexp.MustCompile(`#\s*!?\[\s*(no_mangle|export_name|link_section)\b`)
)

// stripRustNonCode 按 Rust 的词法把注释与字符串、字符字面量换成空格（保留换行）：嵌套的块注释、原始字符串 r#"…"#、转义、
// 字符字面量与生命周期（'a）都按语言规则处理，不会被字符串里的 /* 骗过，也不会把字符串里的词当成代码。
func stripRustNonCode(src string) string {
	b := []byte(src)
	out := make([]byte, len(b))
	blank := func(from, to int) {
		for i := from; i < to && i < len(b); i++ {
			if b[i] == '\n' {
				out[i] = '\n'
			} else {
				out[i] = ' '
			}
		}
	}
	i := 0
	for i < len(b) {
		switch {
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			j := i
			for j < len(b) && b[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			depth, j := 1, i+2
			for j < len(b) && depth > 0 {
				switch {
				case b[j] == '/' && j+1 < len(b) && b[j+1] == '*':
					depth++
					j += 2
				case b[j] == '*' && j+1 < len(b) && b[j+1] == '/':
					depth--
					j += 2
				default:
					j++
				}
			}
			blank(i, j)
			i = j
		case isRawStringStart(b, i):
			j := i
			if b[j] == 'b' {
				j++
			}
			j++ // r
			hashes := 0
			for j < len(b) && b[j] == '#' {
				hashes++
				j++
			}
			j++ // 开头的 "
			end := j
			for end < len(b) {
				if b[end] == '"' && hasHashes(b, end+1, hashes) {
					end += 1 + hashes
					break
				}
				end++
			}
			blank(i, end)
			i = end
		case b[i] == '"' || (b[i] == 'b' && i+1 < len(b) && b[i+1] == '"'):
			j := i + 1
			if b[i] == 'b' {
				j++
			}
			for j < len(b) && b[j] != '"' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			blank(i, j+1)
			i = j + 1
		case b[i] == '\'':
			// 字符字面量（'x'、'\n'、'\u{..}'）与生命周期（'a）：后面紧跟闭合引号才是字面量。
			j := i + 1
			if j < len(b) && b[j] == '\\' {
				j += 2
				for j < len(b) && b[j] != '\'' {
					j++
				}
				blank(i, j+1)
				i = j + 1
			} else if j+1 < len(b) && b[j+1] == '\'' {
				blank(i, j+2)
				i = j + 2
			} else {
				out[i] = b[i]
				i++
			}
		default:
			out[i] = b[i]
			i++
		}
	}
	return string(out)
}

func isRawStringStart(b []byte, i int) bool {
	j := i
	if b[j] == 'b' {
		j++
	}
	if j >= len(b) || b[j] != 'r' {
		return false
	}
	// r 前面不能是标识符的一部分（例如 for"…" 不存在，但 bar#"…" 也不是原始字符串）。
	if i > 0 && (isIdentByte(b[i-1])) {
		return false
	}
	j++
	for j < len(b) && b[j] == '#' {
		j++
	}
	return j < len(b) && b[j] == '"'
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func hasHashes(b []byte, from, n int) bool {
	if from+n > len(b) {
		return false
	}
	for k := 0; k < n; k++ {
		if b[from+k] != '#' {
			return false
		}
	}
	return true
}

// scanUdfSource 是源码层的补充检查（只能在本地做）：用户 crate 里不允许 unsafe 与手写的导出属性——这些都由 SDK 的宏生成。
// clippy 的 unsafe_code 是 deny 而不是 forbid（宏生成的导出需要局部放开），所以这里扫描兜底；扫描按 Rust 词法去掉注释与字符串。
func scanUdfSource(crateDir string) error {
	var problems []string
	err := filepath.WalkDir(filepath.Join(crateDir, "src"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".rs") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := stripRustNonCode(string(raw))
		rel, _ := filepath.Rel(crateDir, path)
		if unsafeRE.MatchString(text) {
			problems = append(problems, rel+"：不允许 unsafe（也不允许 allow(unsafe_code)）")
		}
		if exportAttrRE.MatchString(text) {
			problems = append(problems, rel+"：不允许手写 no_mangle / export_name / link_section，导出由 #[udf] 宏生成")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return errors.New("源码检查未通过：\n  " + strings.Join(problems, "\n  "))
	}
	return nil
}

// loadFixtures 读取 crate 的 tests/fixtures/*.json 并规范化（随包上传的就是规范化后的内容）。
func loadUdfFixtures(crateDir string) (factorudf.FixtureSet, error) {
	dir := filepath.Join(crateDir, "tests", "fixtures")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	set := factorudf.FixtureSet{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		norm, err := factorudf.NormalizeFixtures(raw)
		if err != nil {
			return nil, fmt.Errorf("fixtures/%s：%w", e.Name(), err)
		}
		set[e.Name()] = norm
	}
	if total := set.TotalBytes(); total > factorudf.MaxFixturesBytes {
		return nil, fmt.Errorf("fixtures 共 %d 字节，超过上限 %d", total, factorudf.MaxFixturesBytes)
	}
	return set, nil
}

type udfDescription struct {
	Doc     string `toml:"doc"`
	Example string `toml:"example"`
}

// sanitizeRemote 去掉远程地址里的认证信息（https://user:token@host/…），记录来源不能把凭据带进包与服务端。
func sanitizeRemote(remote string) string {
	if i := strings.Index(remote, "://"); i >= 0 {
		rest := remote[i+3:]
		if at := strings.LastIndex(strings.SplitN(rest, "/", 2)[0], "@"); at >= 0 {
			return remote[:i+3] + rest[at+1:]
		}
	}
	return remote
}

// gitState 返回仓库的来源（远程地址，已去凭据）、提交号与工作区是否有未提交的改动；不在 git 仓库里时 commit 为空。
// 状态用显式参数取（所有未跟踪文件、不忽略子模块的改动），不受用户的 git 配置影响；git 命令出错时返回错误，不当作干净。
func gitState(dir string) (remote, commit string, dirty bool, err error) {
	if _, probe := commandOutput(dir, "git", "rev-parse", "--git-dir"); probe != nil {
		return "", "", false, nil // 不在 git 仓库里
	}
	if commit, err = commandOutput(dir, "git", "rev-parse", "HEAD"); err != nil {
		return "", "", false, err
	}
	remote, _ = commandOutput(dir, "git", "remote", "get-url", "origin")
	status, err := commandOutput(dir, "git", "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return "", "", false, err
	}
	return sanitizeRemote(remote), commit, status != "", nil
}

// runUdfBuild 在本地跑完全部门禁并打包：源码检查 → clippy（-D warnings）→ 原生测试 → 编译 wasm（锁定工具链、路径重映射）→
// 模块检查 → 一致性测试 → 合成 manifest → 打包 dist/<名称>-<sha256>.udfpkg。
func runUdfBuild(ctx context.Context, out io.Writer, opts udfBuildOptions) (*udfBuildReport, error) {
	root, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, err
	}
	crates, err := udfCrates(root)
	if err != nil {
		return nil, err
	}
	name := opts.Name
	switch {
	case name == "" && len(crates) == 1:
		name = crates[0]
	case name == "":
		return nil, fmt.Errorf("工作区里有多个 UDF（%s），请指定名称", strings.Join(crates, "、"))
	}
	crateDir := filepath.Join(root, "udfs", name)
	if _, err := os.Stat(crateDir); err != nil {
		return nil, fmt.Errorf("udfs/%s 不存在", name)
	}
	logOut := out
	if opts.JSON {
		logOut = os.Stderr
	}
	step := func(format string, args ...any) { fmt.Fprintf(logOut, "==> "+format+"\n", args...) }

	step("源码检查（unsafe 与手写导出）")
	if err := scanUdfSource(crateDir); err != nil {
		return nil, err
	}
	step("cargo clippy --release --locked -- -D warnings")
	// clippy 用与最终产物相同的 profile（release）与目标：只在 release 下编译的分支（cfg(not(debug_assertions))）也要过禁用清单。
	if err := runCargo(root, logOut, "clippy", "--release", "--locked", "-p", name, "--target", "wasm32-unknown-unknown", "--", "-D", "warnings"); err != nil {
		return nil, err
	}
	if !opts.SkipNativeTests {
		step("cargo test --locked（原生环境的单元测试与 fixtures）")
		if err := runCargo(root, logOut, "test", "--locked", "-p", name); err != nil {
			return nil, err
		}
	}
	step("cargo build --release --locked --target wasm32-unknown-unknown")
	sysroot, err := commandOutput(root, "rustc", "--print", "sysroot")
	if err != nil {
		return nil, err
	}
	cargoHome := os.Getenv("CARGO_HOME")
	if cargoHome == "" {
		home, _ := os.UserHomeDir()
		cargoHome = filepath.Join(home, ".cargo")
	}
	// 路径重映射：产物与构建目录、用户目录无关（panic 信息里的源码路径不会带进 wasm），同样的源码在任何机器上得到同样的字节。
	// 参数用 CARGO_ENCODED_RUSTFLAGS（0x1f 分隔）传，路径里有空格也不会被拆开；--sdk-path 指向的工作区外的 SDK 也一并重映射。
	remaps := []string{root + "=/udf-src", cargoHome + "=/cargo", sysroot + "=/rustc"}
	if sdk := udfSDKRoot(crateDir); sdk != "" {
		remaps = append(remaps, sdk+"=/moox-udf-sdk")
	}
	var flags []string
	for _, remap := range remaps {
		flags = append(flags, "--remap-path-prefix="+remap)
	}
	if err := runCargoEnv(root, logOut, []string{"CARGO_ENCODED_RUSTFLAGS=" + strings.Join(flags, "\x1f")}, "build", "--release", "--locked", "--target", "wasm32-unknown-unknown", "-p", name); err != nil {
		return nil, err
	}
	wasm, err := os.ReadFile(filepath.Join(root, "target", "wasm32-unknown-unknown", "release", strings.ReplaceAll(name, "-", "_")+".wasm"))
	if err != nil {
		return nil, fmt.Errorf("没有找到编译产物：%w", err)
	}

	step("模块检查与一致性测试")
	rt, err := factorudf.NewRuntime(ctx, factorudf.Options{})
	if err != nil {
		return nil, err
	}
	defer rt.Close(context.Background())
	module, err := rt.Load(ctx, wasm)
	if err != nil {
		return nil, err
	}
	if module.Info.Manifest.Name != name {
		return nil, fmt.Errorf("crate 名 %q 与 #[udf(name = ...)] 的名称 %q 不一致：请保持一致", name, module.Info.Manifest.Name)
	}
	fixtures, err := loadUdfFixtures(crateDir)
	if err != nil {
		return nil, err
	}
	report, err := factorudf.RunConformance(ctx, rt, module, fixtures, factorudf.ConformanceOptions{})
	if err != nil {
		return nil, err
	}
	if !report.Passed {
		var lines []string
		for _, f := range report.Failures() {
			lines = append(lines, f.Name+"："+f.Detail)
		}
		return nil, fmt.Errorf("一致性测试未通过：\n  %s", strings.Join(lines, "\n  "))
	}

	var describe udfDescription
	if raw, err := os.ReadFile(filepath.Join(crateDir, "udf.toml")); err == nil {
		if _, err := toml.Decode(string(raw), &describe); err != nil {
			return nil, fmt.Errorf("udf.toml：%w", err)
		}
	}
	remote, commit, dirty, err := gitState(root)
	if err != nil {
		return nil, fmt.Errorf("读取 git 状态失败：%w", err)
	}
	toolchain, _ := commandOutput(root, "rustc", "--version")
	pkg := &factorudf.Package{
		Wasm: wasm, Fixtures: fixtures, Report: report,
		Manifest: factorudf.PackageManifest{
			Manifest: *module.Info.Manifest, Doc: describe.Doc, Example: describe.Example,
			Source: factorudf.SourceRef{Repo: remote, Commit: commit, Dirty: dirty}, Toolchain: toolchain,
		},
	}
	raw, err := factorudf.WritePackage(pkg)
	if err != nil {
		return nil, err
	}
	// 与 publish 同一套包结构校验：build 成功的包，publish 不会因为结构问题（文件个数、大小、manifest 不一致）再拒绝。
	checked, err := factorudf.ReadPackage(raw, factorudf.Limits{})
	if err != nil {
		return nil, fmt.Errorf("生成的包不合格：%w", err)
	}
	if _, err := factorudf.VerifyPackage(checked, factorudf.Limits{}); err != nil {
		return nil, fmt.Errorf("生成的包不合格：%w", err)
	}
	dist := filepath.Join(root, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dist, name+"-"+module.Info.Hash+".udfpkg")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return nil, err
	}
	result := &udfBuildReport{Name: name, Hash: module.Info.Hash, Package: path, Bytes: len(wasm), Source: remote, Commit: commit, Dirty: dirty,
		Report: report, Manifest: *module.Info.Manifest}
	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return result, enc.Encode(result)
	}
	fmt.Fprintf(out, "\n构建完成：%s\n  版本 %s\n  wasm %d 字节，整包 %d 字节\n  一致性测试 %d 组参数全部通过，每个输出点约 %.0f ns，实例峰值内存 %d KiB\n",
		path, module.Info.Hash, len(wasm), len(raw), report.ParamSets, report.NSPerPoint, report.PeakMemBytes>>10)
	if dirty {
		fmt.Fprintln(out, "  注意：工作区有未提交的改动；publish 要求工作区干净，请先提交")
	}
	if commit == "" {
		fmt.Fprintln(out, "  注意：不在 git 仓库里，包里没有提交号；publish 要求源码在 git 仓库里")
	}
	return result, nil
}

// udfSDKRoot 返回 crate 的 moox-udf 依赖是本地 path 依赖时 SDK 的根目录（path 的上一级，即 moox 仓库的 sdk/rust）；git 依赖返回空串。
func udfSDKRoot(crateDir string) string {
	var manifest struct {
		Dependencies map[string]any `toml:"dependencies"`
	}
	if _, err := toml.DecodeFile(filepath.Join(crateDir, "Cargo.toml"), &manifest); err != nil {
		return ""
	}
	dep, ok := manifest.Dependencies["moox-udf"].(map[string]any)
	if !ok {
		return ""
	}
	path, ok := dep["path"].(string)
	if !ok || path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(crateDir, path)
	}
	return filepath.Dir(filepath.Clean(path))
}
