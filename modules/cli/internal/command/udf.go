package command

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/factorudf"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const factorMgrService = "trpc.moox.factor.FactorMgr"

// udfMaxRequestBytes 是 FactorMgr 的消息上限（servicecatalog 的 max_body_bytes）；发送前检查完整请求体，超过就在本地拒绝。
const udfMaxRequestBytes = 4 << 20

var udfRefRE = regexp.MustCompile(`^([a-z][a-z0-9_]{0,47})@([0-9a-f]{64})$`)

// parseUdfRef 解析 <名称>@<哈希>。
func parseUdfRef(ref string) (name, hash string, err error) {
	match := udfRefRE.FindStringSubmatch(strings.TrimSpace(ref))
	if match == nil {
		return "", "", fmt.Errorf("%q 不是 <名称>@<64 位版本哈希>（哈希在 udf build / publish 的输出里）", ref)
	}
	return match[1], match[2], nil
}

// udfInvoke 经运维网关调用 FactorMgr 的一个方法；ret_info 非成功时返回错误，但响应仍然填好（调用方可以带着细节报错）。
func udfInvoke(ctx context.Context, file, method string, req, rsp proto.Message, ret func() *commonpb.RetInfo) error {
	gateway, err := openCommandGateway(ctx, file, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	if err := gateway.Invoke(ctx, factorMgrService, method, req, rsp); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if info := ret(); info.GetCode() != commonpb.ErrorCode_SUCCESS {
		return &udfRPCError{Method: method, Code: info.GetCode(), Msg: info.GetMsg()}
	}
	return nil
}

type udfRPCError struct {
	Method string
	Code   commonpb.ErrorCode
	Msg    string
}

func (e *udfRPCError) Error() string { return fmt.Sprintf("%s: %s (code %d)", e.Method, e.Msg, e.Code) }

var udfJSON = protojson.MarshalOptions{Multiline: true, Indent: "  ", UseProtoNames: true, EmitUnpopulated: false}

func printProto(out io.Writer, msg proto.Message) error {
	raw, err := udfJSON.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(raw))
	return err
}

// ---------------------------------------------------------------- publish

type udfPublishOptions struct {
	File    string
	Package string
	// Repo 是私有 UDF 仓库的工作区根（检查工作区干净、提交号与包一致）；默认是包所在 dist/ 的上一级。
	Repo string
}

func runUdfPublish(ctx context.Context, out io.Writer, opts udfPublishOptions) error {
	raw, err := os.ReadFile(opts.Package)
	if err != nil {
		return err
	}
	pkg, err := factorudf.ReadPackage(raw, factorudf.Limits{})
	if err != nil {
		return err
	}
	if _, err := factorudf.VerifyPackage(pkg, factorudf.Limits{}); err != nil {
		return err
	}
	repo := opts.Repo
	if repo == "" {
		repo = filepath.Dir(filepath.Dir(opts.Package))
	}
	// 系统里的每个版本都要能对应到私有仓库的一次提交：要求包里有提交号、工作区干净、HEAD 就是构建时的提交。
	_, commit, dirty, gitErr := gitState(repo)
	if gitErr != nil {
		return fmt.Errorf("读取 %s 的 git 状态失败：%w", repo, gitErr)
	}
	switch {
	case pkg.Manifest.Source.Dirty:
		return errors.New("包是在工作区有未提交改动时构建的：提交之后重新 build（产物要与记录的提交一致）")
	case pkg.Manifest.Source.Commit == "" || commit == "":
		return errors.New("包里没有提交号，或 " + repo + " 不在 git 仓库里：UDF 源码要放在 git 仓库里，提交后重新 build")
	case dirty:
		return fmt.Errorf("%s 有未提交的改动：先提交，再重新 build 与 publish（系统记录提交号，工作区要与它一致）", repo)
	case commit != pkg.Manifest.Source.Commit:
		return fmt.Errorf("包是在提交 %.12s 上构建的，现在仓库在 %.12s：重新 build", pkg.Manifest.Source.Commit, commit)
	}
	req := &factorpb.PublishUdfReq{Package: raw}
	if size := base64.StdEncoding.EncodedLen(len(raw)) + 4096; size > udfMaxRequestBytes || proto.Size(req)+4096 > udfMaxRequestBytes {
		return fmt.Errorf("请求体约 %d 字节，超过 FactorMgr 的消息上限 %d：缩小模块或 fixtures", size, udfMaxRequestBytes)
	}
	rsp := &factorpb.PublishUdfRsp{}
	if err := udfInvoke(ctx, opts.File, "PublishUdf", req, rsp, rsp.GetRetInfo); err != nil {
		return err
	}
	state := "新版本（未启用）"
	if !rsp.GetCreated() {
		state = "版本已存在，只更新了说明与示例"
	}
	fmt.Fprintf(out, "已发布 %s@%s：%s\n", rsp.GetName(), rsp.GetHash(), state)
	fmt.Fprintf(out, "下一步：moox-cli udf preview %s@%s --spec <因子定义> --task-id <因子任务>；满意后先 moox-cli udf activate %s@%s 看影响清单，确认后加 --yes\n",
		rsp.GetName(), rsp.GetHash(), rsp.GetName(), rsp.GetHash())
	return nil
}

// ---------------------------------------------------------------- preview

type udfPreviewOptions struct {
	File      string
	Ref       string
	Spec      string
	Task      string
	FactorID  string
	Subjects  []string
	Periods   int32
	AllowPart bool
}

func runUdfPreview(ctx context.Context, out io.Writer, opts udfPreviewOptions) error {
	name, hash, err := parseUdfRef(opts.Ref)
	if err != nil {
		return err
	}
	spec, err := os.ReadFile(opts.Spec)
	if err != nil {
		return err
	}
	if opts.Task == "" {
		return errors.New("--task-id 必填：在这个因子任务的源数据上试算")
	}
	req := &factorpb.PreviewFactorReq{
		TaskId: opts.Task, Spec: string(spec), Subjects: opts.Subjects, Periods: opts.Periods, AllowPartialUniverse: opts.AllowPart,
		FactorId: opts.FactorID, UdfVersions: map[string]string{name: hash},
	}
	rsp := &factorpb.PreviewFactorRsp{}
	err = udfInvoke(ctx, opts.File, "PreviewFactor", req, rsp, rsp.GetRetInfo)
	if err != nil {
		return err
	}
	return printProto(out, rsp)
}

// ---------------------------------------------------------------- activate

type udfActivateOptions struct {
	File   string
	Ref    string
	Yes    bool // 命令行 --yes：确认切换；不带时只给出影响清单
	DryRun bool
}

func runUdfActivate(ctx context.Context, out io.Writer, opts udfActivateOptions) error {
	name, hash, err := parseUdfRef(opts.Ref)
	if err != nil {
		return err
	}
	rsp := &factorpb.ActivateUdfRsp{}
	invokeErr := udfInvoke(ctx, opts.File, "ActivateUdf", &factorpb.ActivateUdfReq{Name: name, Hash: hash, DryRun: opts.DryRun}, rsp, rsp.GetRetInfo)
	var rpcErr *udfRPCError
	if invokeErr != nil && !(errors.As(invokeErr, &rpcErr) && rsp.GetName() != "") {
		return invokeErr
	}
	printActivation(out, rsp)
	if invokeErr != nil {
		return invokeErr
	}
	switch {
	case rsp.GetApplied() && len(rsp.GetImpacts()) == 0:
		fmt.Fprintf(out, "\n已切换 %s：%s → %s。没有因子定义引用它，不需要恢复引擎。\n", name, orEmpty(rsp.GetOldHash()), rsp.GetNewHash())
	case rsp.GetApplied():
		fmt.Fprintf(out, "\n已切换 %s：%s → %s。\n后续：恢复因子引擎模块（lifecycle resume --endpoint <因子引擎地址>），等追赶完成放行；受影响的策略实例会进入 factor_changed，需要重新启用。\n",
			name, orEmpty(rsp.GetOldHash()), rsp.GetNewHash())
	case rsp.GetDryRun():
		fmt.Fprintln(out, "\n（预览：没有修改任何东西；确认后加 --yes）")
	}
	return nil
}

func orEmpty(s string) string {
	if s == "" {
		return "（无）"
	}
	return s
}

func printActivation(out io.Writer, rsp *factorpb.ActivateUdfRsp) {
	fmt.Fprintf(out, "自定义算子 %s：%s → %s\n", rsp.GetName(), orEmpty(rsp.GetOldHash()), rsp.GetNewHash())
	if len(rsp.GetImpacts()) == 0 {
		fmt.Fprintln(out, "影响：没有因子定义引用它")
	} else {
		fmt.Fprintln(out, "影响清单：")
		for _, impact := range rsp.GetImpacts() {
			fmt.Fprintf(out, "  因子 %s：definition_hash %.19s → %.19s，回看 %d → %d，成员 %s\n", impact.GetFactorId(),
				impact.GetOldDefinitionHash(), impact.GetNewDefinitionHash(), impact.GetOldLookback(), impact.GetNewLookback(), strings.Join(impact.GetMembers(), "、"))
			if impact.GetError() != "" {
				fmt.Fprintf(out, "    × %s\n", impact.GetError())
			}
		}
		fmt.Fprintf(out, "  因子任务：%s\n  策略实例：%s\n", orNone(rsp.GetTasks()), orNone(rsp.GetInstances()))
	}
	fmt.Fprintln(out, "前提检查：")
	for _, p := range rsp.GetPrerequisites() {
		mark := "✓"
		if !p.GetOk() {
			mark = "×"
		}
		fmt.Fprintf(out, "  %s %s：%s\n", mark, p.GetName(), p.GetDetail())
	}
	for _, note := range rsp.GetNotes() {
		fmt.Fprintf(out, "注意：%s\n", note)
	}
}

func orNone(list []string) string {
	if len(list) == 0 {
		return "无"
	}
	return strings.Join(list, "、")
}

// ---------------------------------------------------------------- list / show

func runUdfList(ctx context.Context, out io.Writer, file string, asJSON bool) error {
	rsp := &factorpb.ListUdfsRsp{}
	if err := udfInvoke(ctx, file, "ListUdfs", &factorpb.ListUdfsReq{}, rsp, rsp.GetRetInfo); err != nil {
		return err
	}
	if asJSON {
		return printProto(out, rsp)
	}
	if len(rsp.GetUdfs()) == 0 {
		fmt.Fprintln(out, "还没有自定义算子")
		return nil
	}
	for _, udf := range rsp.GetUdfs() {
		active := udf.GetActiveHash()
		if active == "" {
			active = "（未启用）"
		}
		fmt.Fprintf(out, "%s\t启用 %s\t版本 %d\t被 %d 个因子定义引用\t更新 %s\n", udf.GetName(), active, len(udf.GetVersions()), len(udf.GetReferencedBy()), udf.GetUpdatedAt())
	}
	return nil
}

func runUdfShow(ctx context.Context, out io.Writer, file, name string, asJSON bool) error {
	rsp := &factorpb.GetUdfRsp{}
	if err := udfInvoke(ctx, file, "GetUdf", &factorpb.GetUdfReq{Name: name}, rsp, rsp.GetRetInfo); err != nil {
		return err
	}
	if asJSON {
		return printProto(out, rsp)
	}
	udf := rsp.GetUdf()
	fmt.Fprintf(out, "%s\n  启用版本：%s\n  被引用：%s\n", udf.GetName(), orEmpty(udf.GetActiveHash()), orNone(udf.GetReferencedBy()))
	modules := map[string][]string{}
	for _, m := range udf.GetModules() {
		state := m.GetState()
		if m.GetError() != "" {
			state += "（" + m.GetError() + "）"
		}
		modules[m.GetHash()] = append(modules[m.GetHash()], state)
	}
	fmt.Fprintln(out, "  版本：")
	for _, v := range udf.GetVersions() {
		mark := " "
		if v.GetActive() {
			mark = "*"
		}
		states := modules[v.GetHash()]
		sort.Strings(states)
		fmt.Fprintf(out, "   %s %s  来源 %s  %s  引擎模块：%s\n", mark, v.GetHash(), v.GetSourceRef(), v.GetCreatedAt(), orNone(states))
	}
	return nil
}

// ---------------------------------------------------------------- cobra

var udfCmd = &cobra.Command{
	Use:   "udf",
	Short: "自定义算子（wasm UDF）：new → build → publish → preview → activate",
	Long: `自定义算子（wasm UDF）：DSL 表达不了的那一步算法，用 Rust 写成 UDF，编译成 wasm 后发布，在因子表达式里写成 udf.<名称>(...)。

  moox-cli udf new <名称> [--sdk-path <sdk/rust>]   生成骨架（空目录生成工作区，已有工作区只添加 crate）
  moox-cli udf build [<名称>]                        clippy → 原生测试 → 编译 → 模块检查 → 一致性测试 → 打包 dist/<名称>-<哈希>.udfpkg
  moox-cli udf publish <包>                           上传并由服务端复核（要求 git 工作区干净），存为不可变版本（未启用）
  moox-cli udf preview <名称>@<哈希> --spec <文件> --task-id <任务>   用未启用的版本在真实数据上试算
  moox-cli udf activate <名称>@<哈希> [--yes]         切换启用版本：默认只给出影响清单与前提检查，--yes 才切换
  moox-cli udf list | show <名称>                     查看版本、引用关系与各引擎的模块状态

切换启用版本要在因子引擎模块暂停期间进行：lifecycle pause --endpoint <因子引擎健康检查地址，默认 http://127.0.0.1:11417>（等收敛）→ 取消列出的已领取补算作业 →
udf activate → lifecycle resume --endpoint <同上>（追赶：同步目录、备齐模块、用新版本补算暂停期间的周期，再放行）。`,
}

func newUdfCommands() *cobra.Command {
	var file string
	cmd := udfCmd
	cmd.PersistentFlags().StringVar(&file, "file", defaultSetupFile, "moox.toml 路径（经运维网关调用 FactorMgr）")

	var newOpts udfNewOptions
	newCmd := &cobra.Command{
		Use: "new <名称>", Short: "生成 UDF 骨架", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			newOpts.Name = args[0]
			return runUdfNew(cmd.OutOrStdout(), newOpts)
		},
	}
	newCmd.Flags().StringVar(&newOpts.Dir, "dir", ".", "工作区目录")
	newCmd.Flags().StringVar(&newOpts.SDKPath, "sdk-path", "", "本地 SDK 目录（moox 仓库的 sdk/rust）；不给就用 git 依赖（仓库 + 标签）")

	var buildOpts udfBuildOptions
	buildCmd := &cobra.Command{
		Use: "build [<名称>]", Short: "本地门禁并打包", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				buildOpts.Name = args[0]
			}
			_, err := runUdfBuild(cmd.Context(), cmd.OutOrStdout(), buildOpts)
			return err
		},
	}
	buildCmd.Flags().StringVar(&buildOpts.Dir, "dir", ".", "工作区目录")
	buildCmd.Flags().BoolVar(&buildOpts.JSON, "json", false, "输出机器可读的报告（日志写到标准错误）")
	buildCmd.Flags().BoolVar(&buildOpts.SkipNativeTests, "skip-native-tests", false, "不运行 cargo test")

	var pubOpts udfPublishOptions
	publishCmd := &cobra.Command{
		Use: "publish <包>", Short: "上传并复核，存为不可变版本（未启用）", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pubOpts.File, pubOpts.Package = file, args[0]
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			return runUdfPublish(ctx, cmd.OutOrStdout(), pubOpts)
		},
	}
	publishCmd.Flags().StringVar(&pubOpts.Repo, "repo", "", "UDF 仓库的工作区根（默认是包所在 dist/ 的上一级）")

	var prevOpts udfPreviewOptions
	previewCmd := &cobra.Command{
		Use: "preview <名称>@<哈希>", Short: "用未启用的版本在真实数据上试算", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prevOpts.File, prevOpts.Ref = file, args[0]
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			return runUdfPreview(ctx, cmd.OutOrStdout(), prevOpts)
		},
	}
	previewCmd.Flags().StringVar(&prevOpts.Spec, "spec", "", "因子定义文件（里面用 udf.<名称>(...)）")
	previewCmd.Flags().StringVar(&prevOpts.Task, "task-id", "", "在哪个因子任务的源数据上试算")
	previewCmd.Flags().StringVar(&prevOpts.FactorID, "factor-id", "", "因子 ID（单输出因子的输出名就是它）；不给按草稿处理")
	previewCmd.Flags().StringSliceVar(&prevOpts.Subjects, "subjects", nil, "展示的标的，逗号分隔；不给取前几个")
	previewCmd.Flags().Int32Var(&prevOpts.Periods, "periods", 0, "最近多少期（默认 20，最多 500）")
	previewCmd.Flags().BoolVar(&prevOpts.AllowPart, "allow-partial-universe", false, "截面因子允许标的不完整")

	var actOpts udfActivateOptions
	activateCmd := &cobra.Command{
		Use: "activate <名称>@<哈希>", Short: "切换启用版本（默认只看影响清单与前提，--yes 才切换）", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actOpts.File, actOpts.Ref = file, args[0]
			actOpts.DryRun = !actOpts.Yes
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			return runUdfActivate(ctx, cmd.OutOrStdout(), actOpts)
		},
	}
	activateCmd.Flags().BoolVar(&actOpts.Yes, "yes", false, "确认切换；不加只给出影响清单与前提检查，不修改")

	var asJSON bool
	listCmd := &cobra.Command{
		Use: "list", Short: "列出自定义算子", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUdfList(cmd.Context(), cmd.OutOrStdout(), file, asJSON)
		},
	}
	showCmd := &cobra.Command{
		Use: "show <名称>", Short: "查看一个自定义算子的版本、引用与引擎模块状态", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUdfShow(cmd.Context(), cmd.OutOrStdout(), file, args[0], asJSON)
		},
	}
	listCmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	showCmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")

	cmd.AddCommand(newCmd, buildCmd, publishCmd, previewCmd, activateCmd, listCmd, showCmd)
	return cmd
}

func init() { rootCmd.AddCommand(newUdfCommands()) }
