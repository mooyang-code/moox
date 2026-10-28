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
	cmd.Env = append(os.Environ(), env...)
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
	unsafeRE       = regexp.MustCompile(`\bunsafe\b|\ballow\s*\([^)]*unsafe_code`)
	exportAttrRE   = regexp.MustCompile(`#\s*\[\s*(no_mangle|export_name|link_section)\b`)
	lineCommentRE  = regexp.MustCompile(`//[^\n]*`)
	blockCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
	stringRE       = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)
)

// scanUdfSource 是源码层的补充检查（只能在本地做）：用户 crate 里不允许 unsafe 与手写的导出属性——这些都由 SDK 的宏生成。
// 去掉注释与字符串再匹配，避免说明文字里的词误报。clippy 的 unsafe_code 是 deny 而不是 forbid（宏生成的导出需要局部放开），
// 所以这里扫描兜底。
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
		text := blockCommentRE.ReplaceAllString(string(raw), "")
		text = lineCommentRE.ReplaceAllString(text, "")
		text = stringRE.ReplaceAllString(text, `""`)
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

// gitState 返回仓库的来源（远程地址）、提交号与工作区是否有未提交的改动；不在 git 仓库里时 commit 为空。
func gitState(dir string) (remote, commit string, dirty bool) {
	commit, err := commandOutput(dir, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", "", false
	}
	remote, _ = commandOutput(dir, "git", "remote", "get-url", "origin")
	status, _ := commandOutput(dir, "git", "status", "--porcelain")
	return remote, commit, status != ""
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
	step("cargo clippy --locked -- -D warnings")
	if err := runCargo(root, logOut, "clippy", "--locked", "-p", name, "--target", "wasm32-unknown-unknown", "--", "-D", "warnings"); err != nil {
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
	flags := fmt.Sprintf("--remap-path-prefix=%s=/udf-src --remap-path-prefix=%s=/cargo --remap-path-prefix=%s=/rustc", root, cargoHome, sysroot)
	if err := runCargoEnv(root, logOut, []string{"RUSTFLAGS=" + flags}, "build", "--release", "--locked", "--target", "wasm32-unknown-unknown", "-p", name); err != nil {
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
	remote, commit, dirty := gitState(root)
	toolchain, _ := commandOutput(root, "rustc", "--version")
	pkg := &factorudf.Package{
		Wasm: wasm, Fixtures: fixtures, Report: report,
		Manifest: factorudf.PackageManifest{
			Manifest: *module.Info.Manifest, Doc: describe.Doc, Example: describe.Example,
			Source: factorudf.SourceRef{Repo: remote, Commit: commit}, Toolchain: toolchain,
		},
	}
	raw, err := factorudf.WritePackage(pkg)
	if err != nil {
		return nil, err
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
