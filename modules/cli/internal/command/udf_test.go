package command

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/factorudf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// udfFakeGateway 记录调用，按方法返回预设的响应。
type udfFakeGateway struct {
	method string
	req    proto.Message
	rsp    proto.Message
	closed bool
}

func (g *udfFakeGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	g.method, g.req = service+"/"+method, req.(proto.Message)
	if g.rsp != nil {
		proto.Merge(rsp.(proto.Message), g.rsp)
	}
	return nil
}
func (g *udfFakeGateway) Close() error { g.closed = true; return nil }
func (g *udfFakeGateway) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return nil, nil
}

func useUdfGateway(t *testing.T, g *udfFakeGateway) {
	t.Helper()
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *setupconfig.Snapshot) (commandGateway, error) { return g, nil }
	t.Cleanup(func() { openCommandGateway = previous })
}

func udfOK() *commonpb.RetInfo { return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS} }

const testHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestParseUdfRef(t *testing.T) {
	name, hash, err := parseUdfRef("kalman_trend@" + testHash)
	require.NoError(t, err)
	assert.Equal(t, "kalman_trend", name)
	assert.Equal(t, testHash, hash)
	for _, bad := range []string{"", "kalman", "kalman@abc", "Kalman@" + testHash, "k@" + strings.ToUpper(testHash)} {
		_, _, err := parseUdfRef(bad)
		assert.Error(t, err, bad)
	}
}

func TestUdfPreviewSendsTheVersionOverride(t *testing.T) {
	g := &udfFakeGateway{rsp: &factorpb.PreviewFactorRsp{RetInfo: udfOK(), Columns: []string{"f"}}}
	useUdfGateway(t, g)
	spec := filepath.Join(t.TempDir(), "spec.yaml")
	require.NoError(t, os.WriteFile(spec, []byte("output: udf.ts_mean_udf(close, 3)\n"), 0o600))
	var out bytes.Buffer
	err := runUdfPreview(context.Background(), &out, udfPreviewOptions{Ref: "ts_mean_udf@" + testHash, Spec: spec, Task: "ftask_a", Subjects: []string{"BTC"}, Periods: 5, FactorID: "f"})
	require.NoError(t, err)
	assert.Equal(t, "trpc.moox.factor.FactorMgr/PreviewFactor", g.method)
	req := g.req.(*factorpb.PreviewFactorReq)
	assert.Equal(t, map[string]string{"ts_mean_udf": testHash}, req.GetUdfVersions())
	assert.Equal(t, "ftask_a", req.GetTaskId())
	assert.Equal(t, int32(5), req.GetPeriods())
	assert.Contains(t, req.GetSpec(), "udf.ts_mean_udf")
	assert.True(t, g.closed)

	err = runUdfPreview(context.Background(), &out, udfPreviewOptions{Ref: "ts_mean_udf@" + testHash, Spec: spec})
	assert.ErrorContains(t, err, "--task")
}

func TestUdfActivatePrintsImpactsAndPrerequisites(t *testing.T) {
	rsp := &factorpb.ActivateUdfRsp{
		RetInfo: udfOK(), Name: "ts_mean_udf", OldHash: "aa", NewHash: testHash, DryRun: true,
		Impacts:       []*factorpb.UdfImpact{{FactorId: "f1", OldDefinitionHash: "sha256:1111111111111111111111", NewDefinitionHash: "sha256:2222222222222222222222", OldLookback: 20, NewLookback: 30, Members: []string{"t/f1"}}},
		Tasks:         []string{"t"},
		Instances:     []string{"inst-1"},
		Prerequisites: []*factorpb.UdfPrerequisite{{Name: "因子引擎模块已暂停且本次暂停已收敛", Ok: true, Detail: "已暂停"}, {Name: "受影响任务没有已领取且未结束的补算作业", Ok: false, Detail: "job-1"}},
		Notes:         []string{"回看变长"},
	}
	g := &udfFakeGateway{rsp: rsp}
	useUdfGateway(t, g)
	var out bytes.Buffer
	require.NoError(t, runUdfActivate(context.Background(), &out, udfActivateOptions{Ref: "ts_mean_udf@" + testHash, DryRun: true}))
	req := g.req.(*factorpb.ActivateUdfReq)
	assert.True(t, req.GetDryRun())
	assert.Equal(t, testHash, req.GetHash())
	text := out.String()
	for _, want := range []string{"影响清单", "因子 f1", "回看 20 → 30", "策略实例：inst-1", "✓ 因子引擎模块已暂停", "× 受影响任务没有已领取", "注意：回看变长", "dry-run"} {
		assert.Contains(t, text, want)
	}
}

// 前提不满足时服务端返回 CONFLICT 并带着完整的检查结果：命令照样打印检查结果，再返回错误。
func TestUdfActivateConflictStillPrintsTheChecks(t *testing.T) {
	g := &udfFakeGateway{rsp: &factorpb.ActivateUdfRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_CONFLICT, Msg: "前提不满足"},
		Name:    "ts_mean_udf", NewHash: testHash,
		Prerequisites: []*factorpb.UdfPrerequisite{{Name: "引擎暂停", Ok: false, Detail: "引擎在运行"}},
	}}
	useUdfGateway(t, g)
	var out bytes.Buffer
	err := runUdfActivate(context.Background(), &out, udfActivateOptions{Ref: "ts_mean_udf@" + testHash})
	require.ErrorContains(t, err, "前提不满足")
	assert.Contains(t, out.String(), "× 引擎暂停：引擎在运行")
}

func TestUdfListAndShow(t *testing.T) {
	g := &udfFakeGateway{rsp: &factorpb.ListUdfsRsp{RetInfo: udfOK(), Udfs: []*factorpb.UdfInfo{{Name: "a", ActiveHash: testHash, Versions: []*factorpb.UdfVersionInfo{{Hash: testHash}}, ReferencedBy: []string{"f1"}}}}}
	useUdfGateway(t, g)
	var out bytes.Buffer
	require.NoError(t, runUdfList(context.Background(), &out, "", false))
	assert.Contains(t, out.String(), "a\t启用 "+testHash)

	g2 := &udfFakeGateway{rsp: &factorpb.GetUdfRsp{RetInfo: udfOK(), Udf: &factorpb.UdfInfo{
		Name: "a", ActiveHash: testHash, Versions: []*factorpb.UdfVersionInfo{{Hash: testHash, Active: true, SourceRef: "repo@abc"}},
		Modules: []*factorpb.UdfModuleStatus{{Hash: testHash, State: "ready"}, {Hash: testHash, State: "fetch_failed", Error: "超时"}},
	}}}
	useUdfGateway(t, g2)
	out.Reset()
	require.NoError(t, runUdfShow(context.Background(), &out, "", "a", false))
	assert.Contains(t, out.String(), "引擎模块：fetch_failed（超时）、ready")
	assert.Equal(t, "a", g2.req.(*factorpb.GetUdfReq).GetName())
}

// git 仓库的辅助：初始化、提交。
func gitInit(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}, {"remote", "add", "origin", "git@example.com:me/udfs.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	return gitCommit(t, dir)
}

func gitCommit(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "x", "--allow-empty"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	commit, err := commandOutput(dir, "git", "rev-parse", "HEAD")
	require.NoError(t, err)
	return commit
}

func udfTestPackage(t *testing.T, dir, commit string) string {
	t.Helper()
	return udfTestPackageWith(t, dir, commit, false)
}

func udfTestPackageDirty(t *testing.T, dir, commit string) string {
	t.Helper()
	return udfTestPackageWith(t, dir, commit, true)
}

func udfTestPackageWith(t *testing.T, dir, commit string, dirty bool) string {
	t.Helper()
	wasm, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "factorudf", "testdata", "ts_mean.wasm"))
	require.NoError(t, err)
	rt, err := factorudf.NewRuntime(context.Background(), factorudf.Options{})
	require.NoError(t, err)
	defer rt.Close(context.Background())
	m, err := rt.Load(context.Background(), wasm)
	require.NoError(t, err)
	rep, err := factorudf.RunConformance(context.Background(), rt, m, nil, factorudf.ConformanceOptions{})
	require.NoError(t, err)
	raw, err := factorudf.WritePackage(&factorudf.Package{Wasm: wasm, Report: rep, Manifest: factorudf.PackageManifest{Manifest: *m.Info.Manifest, Source: factorudf.SourceRef{Repo: "r", Commit: commit, Dirty: dirty}}})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "dist"), 0o755))
	path := filepath.Join(dir, "dist", "ts_mean_udf.udfpkg")
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	return path
}

func TestUdfPublishRequiresACleanWorkspaceAtTheBuiltCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644))
	commit := gitInit(t, dir)
	pkg := udfTestPackage(t, dir, commit)
	// dist 是未跟踪文件：先忽略它，才算“工作区干净”。
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("dist/\n"), 0o644))
	commit = gitCommit(t, dir)
	pkg = udfTestPackage(t, dir, commit)

	g := &udfFakeGateway{rsp: &factorpb.PublishUdfRsp{RetInfo: udfOK(), Name: "ts_mean_udf", Hash: testHash, Created: true}}
	useUdfGateway(t, g)
	var out bytes.Buffer
	require.NoError(t, runUdfPublish(context.Background(), &out, udfPublishOptions{Package: pkg}))
	assert.Equal(t, "trpc.moox.factor.FactorMgr/PublishUdf", g.method)
	assert.NotEmpty(t, g.req.(*factorpb.PublishUdfReq).GetPackage())
	assert.Contains(t, out.String(), "新版本（未启用）")

	// 工作区有未提交的改动：拒绝，不调用服务。
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("changed"), 0o644))
	g2 := &udfFakeGateway{}
	useUdfGateway(t, g2)
	err := runUdfPublish(context.Background(), &out, udfPublishOptions{Package: pkg})
	require.ErrorContains(t, err, "未提交的改动")
	assert.Empty(t, g2.method)

	// 提交之后与包里的提交号不一致：拒绝。
	gitCommit(t, dir)
	err = runUdfPublish(context.Background(), &out, udfPublishOptions{Package: pkg})
	require.ErrorContains(t, err, "重新 build")

	// 包是在脏工作区构建出来的：即使提交号一致也拒绝。
	dirtyPkg := udfTestPackageDirty(t, dir, gitCommit(t, dir))
	err = runUdfPublish(context.Background(), &out, udfPublishOptions{Package: dirtyPkg})
	require.ErrorContains(t, err, "未提交改动时构建")

	// 包里没有提交号：拒绝。
	pkgNoCommit := udfTestPackage(t, dir, "")
	err = runUdfPublish(context.Background(), &out, udfPublishOptions{Package: pkgNoCommit})
	require.ErrorContains(t, err, "没有提交号")

	// 不是包：拒绝。
	bad := filepath.Join(dir, "dist", "bad.udfpkg")
	require.NoError(t, os.WriteFile(bad, []byte("junk"), 0o644))
	require.Error(t, runUdfPublish(context.Background(), &out, udfPublishOptions{Package: bad}))
}

func TestUdfNewGeneratesAWorkspaceAndAddsCrates(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	require.NoError(t, runUdfNew(&out, udfNewOptions{Dir: dir, Name: "kalman_trend", NoLock: true}))
	for _, f := range []string{"rust-toolchain.toml", "clippy.toml", "Cargo.toml", "AGENTS.md", ".gitignore",
		"udfs/kalman_trend/Cargo.toml", "udfs/kalman_trend/udf.toml", "udfs/kalman_trend/src/lib.rs", "udfs/kalman_trend/tests/fixtures/basic.json"} {
		_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f)))
		assert.NoError(t, err, f)
	}
	lib, err := os.ReadFile(filepath.Join(dir, "udfs/kalman_trend/src/lib.rs"))
	require.NoError(t, err)
	assert.Contains(t, string(lib), `name = "kalman_trend"`)
	assert.Contains(t, string(lib), "&KalmanTrendParams")
	manifest, _ := os.ReadFile(filepath.Join(dir, "udfs/kalman_trend/Cargo.toml"))
	assert.Contains(t, string(manifest), `git = "`+udfSDKRepo+`"`)
	assert.Contains(t, string(manifest), udfSDKTag)
	toolchain, _ := os.ReadFile(filepath.Join(dir, "rust-toolchain.toml"))
	assert.Contains(t, string(toolchain), "wasm32-unknown-unknown")

	// 已有工作区：只添加 crate，不覆盖已有文件。
	require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("我改过"), 0o644))
	require.NoError(t, runUdfNew(&out, udfNewOptions{Dir: dir, Name: "second_one", NoLock: true}))
	agents, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	assert.Equal(t, "我改过", string(agents))
	_, err = os.Stat(filepath.Join(dir, "udfs/second_one/src/lib.rs"))
	assert.NoError(t, err)

	// 重名、不合法名称、非空且非工作区的目录都拒绝。
	assert.ErrorContains(t, runUdfNew(&out, udfNewOptions{Dir: dir, Name: "second_one", NoLock: true}), "已经存在")
	assert.ErrorContains(t, runUdfNew(&out, udfNewOptions{Dir: dir, Name: "Bad-Name", NoLock: true}), "不合法")
	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, "x"), []byte("x"), 0o644))
	assert.ErrorContains(t, runUdfNew(&out, udfNewOptions{Dir: other, Name: "abc", NoLock: true}), "不是空目录")
}

func TestUdfParamsType(t *testing.T) {
	assert.Equal(t, "KalmanTrendParams", udfParamsType("kalman_trend"))
	assert.Equal(t, "AParams", udfParamsType("a"))
	assert.Equal(t, "Ema2FastParams", udfParamsType("ema2_fast"))
}

func TestScanUdfSourceRejectsUnsafeAndHandWrittenExports(t *testing.T) {
	write := func(src string) string {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "lib.rs"), []byte(src), 0o644))
		return dir
	}
	assert.NoError(t, scanUdfSource(write("// 这里提到 unsafe 只是注释\nfn f() { let s = \"unsafe in a string\"; }\n")))
	assert.ErrorContains(t, scanUdfSource(write("fn f() { unsafe { } }")), "unsafe")
	assert.ErrorContains(t, scanUdfSource(write("#![allow(unsafe_code)]\nfn f() {}")), "unsafe")
	assert.ErrorContains(t, scanUdfSource(write("#[no_mangle]\npub extern \"C\" fn x() {}")), "手写")
	assert.ErrorContains(t, scanUdfSource(write("#[link_section = \"moox.udf\"]\nstatic X: [u8; 1] = [0];")), "手写")

	// 词法层面：嵌套块注释、原始字符串、字符字面量与生命周期都按 Rust 规则处理。
	assert.NoError(t, scanUdfSource(write("/* 外层 /* 嵌套 unsafe */ 仍在注释里 unsafe */\nfn f() {}")))
	assert.NoError(t, scanUdfSource(write("fn f() { let _ = r#\"unsafe \" 还在字符串里\"#; }")))
	assert.NoError(t, scanUdfSource(write("fn f<'a>(x: &'a str) { let _ = '\"'; let _ = \"/*\"; }\nfn g() {}")))
	// 字符串里的 /* 不能骗过扫描：之后的真 unsafe 必须被发现。
	assert.ErrorContains(t, scanUdfSource(write("fn f() { let _ = \"/*\"; }\nfn g() { unsafe {} }")), "unsafe")
	assert.ErrorContains(t, scanUdfSource(write("fn f<'a>(x: &'a str) { unsafe {} }")), "unsafe")
	// C 字符串（c"…"、cr#"…"#）与字节串同样按字符串处理：里面的 /* 不是注释、里面的 unsafe 不是代码。
	assert.NoError(t, scanUdfSource(write("fn f() { let _ = cr#\"unsafe\"#; let _ = c\"unsafe\"; let _ = br\"unsafe\"; }")))
	assert.ErrorContains(t, scanUdfSource(write("fn f(v: &f64) -> f64 { let _ = cr#\"\" /*\"#;\n#[allow(unsafe_code)]\nunsafe { core::ptr::read(v) } }")), "unsafe")
	// 原始标识符不是原始字符串；多字节字符字面量按一个字符处理；expect 与 cfg_attr 里的 no_mangle 同样被拒绝。
	assert.ErrorContains(t, scanUdfSource(write("macro_rules! skip { ($($t:tt)*) => { () }; }\nskip!(r#r\"\\\" /*\");\n#[allow(unsafe_code)]\nunsafe { core::ptr::read(v) }")), "unsafe")
	assert.ErrorContains(t, scanUdfSource(write("fn f(c: char) { let _ = matches!(c, '中'|'\"');\n#[allow(unsafe_code)]\nunsafe { } }")), "unsafe")
	assert.NoError(t, scanUdfSource(write("fn f(c: char) { let _ = matches!(c, '中'|'x'); let _ = \"unsafe\"; }")))
	// 只有属性里的名称才算：普通标识符与函数名不误报。
	assert.NoError(t, scanUdfSource(write("pub fn label(export_name: u32) -> u32 { export_name }")))
	assert.NoError(t, scanUdfSource(write("pub fn expect(unsafe_code: u32) -> u32 { unsafe_code }")))
	assert.ErrorContains(t, scanUdfSource(write("#[expect(unsafe_code)]\nfn f() {}")), "unsafe")
	assert.ErrorContains(t, scanUdfSource(write("#[r#no_mangle]\npub extern \"C\" fn extra() -> i32 { 1 }")), "手写")
	assert.ErrorContains(t, scanUdfSource(write("#[cfg_attr(all(), no_mangle)]\npub extern \"C\" fn extra() -> i32 { 1 }")), "手写")
}

func TestSanitizeRemoteStripsCredentials(t *testing.T) {
	assert.Equal(t, "https://github.com/a/b.git", sanitizeRemote("https://user:token@github.com/a/b.git"))
	assert.Equal(t, "https://github.com/a/b.git", sanitizeRemote("https://token@github.com/a/b.git"))
	assert.Equal(t, "https://github.com/a/b.git", sanitizeRemote("https://github.com/a/b.git"))
	assert.Equal(t, "git@github.com:a/b.git", sanitizeRemote("git@github.com:a/b.git"))
}

func TestUdfNewRejectsRustKeywordNames(t *testing.T) {
	var out bytes.Buffer
	err := runUdfNew(&out, udfNewOptions{Dir: t.TempDir(), Name: "match", NoLock: true})
	require.ErrorContains(t, err, "关键字")
}

// T6.8：在独立的空目录（不依赖 moox 源码树里的任何工作区文件）执行 udf new → build，产出的包能通过 factorudf 的检查。
// 需要 cargo（锁定版本的 Rust 工具链与 wasm 目标）；没有时跳过，CI 的 verify-rust 任务里运行。
func TestUdfNewThenBuildInAnEmptyDirectory(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("没有 cargo")
	}
	sdk, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "sdk", "rust"))
	require.NoError(t, err)
	dir := t.TempDir()
	var out bytes.Buffer
	require.NoError(t, runUdfNew(&out, udfNewOptions{Dir: dir, Name: "mid_range", SDKPath: sdk}), out.String())
	_, err = os.Stat(filepath.Join(dir, "Cargo.lock"))
	require.NoError(t, err, "udf new 生成了 Cargo.lock：%s", out.String())

	// 构建要求仓库在 git 里（提交号）；先提交。
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/target/\n/dist/\n"), 0o644))
	gitInit(t, dir)

	out.Reset()
	result, err := runUdfBuild(context.Background(), &out, udfBuildOptions{Dir: dir})
	require.NoError(t, err, out.String())
	assert.Equal(t, "mid_range", result.Name)
	assert.True(t, result.Report.Passed)
	assert.NotEmpty(t, result.Commit)
	assert.False(t, result.Dirty)

	raw, err := os.ReadFile(result.Package)
	require.NoError(t, err)
	pkg, err := factorudf.ReadPackage(raw, factorudf.Limits{})
	require.NoError(t, err)
	info, err := factorudf.VerifyPackage(pkg, factorudf.Limits{})
	require.NoError(t, err)
	assert.Equal(t, result.Hash, info.Hash)
	assert.Equal(t, "mid_range", info.Manifest.Name)
	assert.Equal(t, "example.com:me/udfs.git", strings.TrimPrefix(pkg.Manifest.Source.Repo, "git@"))
	assert.Contains(t, pkg.Manifest.Doc, "mid_range")
	assert.NotEmpty(t, pkg.Fixtures)

	// 同样的源码再构建一次：字节相同（路径重映射，产物与构建目录无关）。
	again, err := runUdfBuild(context.Background(), io.Discard, udfBuildOptions{Dir: dir, SkipNativeTests: true})
	require.NoError(t, err)
	assert.Equal(t, result.Hash, again.Hash)

	// 违反规范的源码被拒绝：unsafe。
	lib := filepath.Join(dir, "udfs", "mid_range", "src", "lib.rs")
	src, err := os.ReadFile(lib)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(lib, append(src, []byte("\nfn sneaky() { unsafe { } }\n")...), 0o644))
	_, err = runUdfBuild(context.Background(), io.Discard, udfBuildOptions{Dir: dir})
	require.ErrorContains(t, err, "unsafe")

	// 违反禁用清单的代码被 clippy 拒绝：融合乘加。
	require.NoError(t, os.WriteFile(lib, append(src, []byte("\nfn sneaky(a: f64) -> f64 { a.mul_add(2.0, 1.0) }\n")...), 0o644))
	_, err = runUdfBuild(context.Background(), io.Discard, udfBuildOptions{Dir: dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clippy")
}
