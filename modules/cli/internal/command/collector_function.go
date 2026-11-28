package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/rpcclient"
	"github.com/spf13/cobra"
)

// 采集云函数的云上状态、删除与出口探测：都经 Collector（CollectMgr）。云上的函数由 Collector 的对账器按采集计划自动建、自动修，
// 这里只看状态、清理计划外的函数和诊断出口。

// oldAccountFunctionsHint 是换过云账号之后的提示：Collector 只按当前配置的云账号列举函数，旧账号下建的函数它管不到。
const oldAccountFunctionsHint = "只删当前 scf_plan 云账号下、属于本空间的函数；旧云账号下建的函数 Collector 管不到，本命令也删不到，" +
	"要用 moox-cli collector function inspect 查出来，再到腾讯云控制台手工删。"

type collectorFunctionFlags struct {
	File      string
	SpaceID   string
	Drift     bool
	Names     []string
	Prefix    string
	Namespace string
	Region    string
	Yes       bool
}

var (
	collectorFunctionListFlags   collectorFunctionFlags
	collectorFunctionDeleteFlags collectorFunctionFlags
	collectorFunctionProbeFlags  collectorFunctionFlags
)

var collectorFunctionListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出空间的采集函数与补采函数：计划里应有的内容、云上读回的状态，计划外的函数单列（--drift 只看不一致的）",
	Long: "Collector 现场读回空间计划里的每个函数（定时采集函数与补采函数），与期望比对：是否存在、定时器、标的分配、代码包是否最新。" +
		"符合本空间命名格式但不在计划里的函数列在 unplanned 里：对账器已停掉它们的定时器，不会自动删除，要清理用 collector function delete。" +
		"读回要逐个调腾讯云，函数多时需要一两分钟。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runCollectorFunctionList(cmd.Context(), cmd.OutOrStdout(), collectorFunctionListFlags)
	},
}

var collectorFunctionDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "按名字或前缀删除空间的云函数（计划里的函数删掉后下一轮对账会重建；默认只预览，--yes 才执行）",
	Long: "经 Collector 删除云函数，用来清理计划外的函数或强制重建计划里的函数。--name 可重复，--prefix 必须是本空间的函数名前缀。" +
		"--namespace、--region 只删这个命名空间、地域里的：命名空间改过之后，旧命名空间里会留下与在用函数同名的副本，删副本时一定要带上，否则在用的同名函数也会被删（下一轮对账才重建）。" +
		"不加 --yes 时 Collector 用与实际删除相同的匹配列出会被删的函数（带命名空间），不删。" + oldAccountFunctionsHint,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runCollectorFunctionDelete(cmd, collectorFunctionDeleteFlags)
	},
}

var collectorFunctionProbeCmd = &cobra.Command{
	Use:   "probe-egress",
	Short: "调用已生效计划里的各采集函数，取它们此刻的公网出口 IP",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runCollectorFunctionProbe(cmd.Context(), cmd.OutOrStdout(), collectorFunctionProbeFlags)
	},
}

func init() {
	collectorFunctionCmd.AddCommand(collectorFunctionListCmd, collectorFunctionDeleteCmd, collectorFunctionProbeCmd)
	for cmd, flags := range map[*cobra.Command]*collectorFunctionFlags{
		collectorFunctionListCmd: &collectorFunctionListFlags, collectorFunctionDeleteCmd: &collectorFunctionDeleteFlags, collectorFunctionProbeCmd: &collectorFunctionProbeFlags,
	} {
		cmd.Flags().StringVar(&flags.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
		cmd.Flags().StringVar(&flags.SpaceID, "space-id", "", "空间 ID")
	}
	collectorFunctionListCmd.Flags().BoolVar(&collectorFunctionListFlags.Drift, "drift", false, "只列出与期望不一致的函数（没读到、不存在、定时器或标的分配不一致、代码包落后）")
	deleteFlags := collectorFunctionDeleteCmd.Flags()
	deleteFlags.StringArrayVar(&collectorFunctionDeleteFlags.Names, "name", nil, "要删的函数名，可重复")
	deleteFlags.StringVar(&collectorFunctionDeleteFlags.Prefix, "prefix", "", "按函数名前缀删（必须是本空间的前缀）")
	deleteFlags.StringVar(&collectorFunctionDeleteFlags.Namespace, "namespace", "", "只删这个命名空间里的（删旧命名空间里的同名副本时必带）")
	deleteFlags.StringVar(&collectorFunctionDeleteFlags.Region, "region", "", "只删这个地域里的，如 ap-nanjing")
	deleteFlags.BoolVar(&collectorFunctionDeleteFlags.Yes, "yes", false, "确认删除；不加只预览")
	collectorFunctionProbeCmd.Flags().StringArrayVar(&collectorFunctionProbeFlags.Names, "name", nil, "只探测这些函数，可重复；不给时探测全部")
}

// openCollectorFunctionClient 校验空间并打开网关；返回的 close 必须调用。
func openCollectorFunctionClient(ctx context.Context, flags collectorFunctionFlags) (*rpcclient.Client, func() error, error) {
	spaceID := strings.ToLower(strings.TrimSpace(flags.SpaceID))
	if spaceID == "" {
		return nil, nil, errors.New("--space-id is required")
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return nil, nil, err
	}
	return &rpcclient.Client{Gateway: gateway, SpaceID: spaceID}, gateway.Close, nil
}

type collectorFunctionListView struct {
	SpaceID    string                    `json:"space_id"`
	Total      int                       `json:"total"`
	DriftCount int                       `json:"drift_count"`
	Functions  []rpcclient.CloudFunction `json:"functions"`
	Unplanned  []unplannedFunctionView   `json:"unplanned"`
	Note       string                    `json:"note,omitempty"`
}

// unplannedFunctionView 是计划外的一个函数，带上只删它自己的命令（限定命名空间与地域，不碰在用的同名函数）。
type unplannedFunctionView struct {
	rpcclient.CloudFunction
	DeleteCommand string `json:"delete_command"`
}

// unplannedDeleteCommand 是只删一个函数的命令：同名函数在别的命名空间里可能正在用，所以一定带上命名空间与地域。
func unplannedDeleteCommand(spaceID string, function rpcclient.CloudFunction) string {
	return fmt.Sprintf("moox-cli collector function delete --space-id %s --name %s --namespace %s --region %s", spaceID, function.Name, function.Namespace, function.Region)
}

// buildCollectorFunctionList 把读回分成计划内与计划外两组；drift 为 true 时计划内只留不一致的。
func buildCollectorFunctionList(spaceID string, functions []rpcclient.CloudFunction, drift bool) collectorFunctionListView {
	view := collectorFunctionListView{SpaceID: spaceID, Total: len(functions), Functions: []rpcclient.CloudFunction{}, Unplanned: []unplannedFunctionView{}}
	for _, function := range functions {
		if function.Drifted() {
			view.DriftCount++
		}
		switch {
		case function.Unplanned:
			view.Unplanned = append(view.Unplanned, unplannedFunctionView{CloudFunction: function, DeleteCommand: unplannedDeleteCommand(spaceID, function)})
		case !drift || function.Drifted():
			view.Functions = append(view.Functions, function)
		}
	}
	if len(view.Unplanned) > 0 {
		view.Note = fmt.Sprintf("有 %d 个计划外的函数：定时器已停，确认后用各自的 delete_command 删除（带命名空间与地域，不碰在用的同名函数）", len(view.Unplanned))
	}
	return view
}

func runCollectorFunctionList(ctx context.Context, out io.Writer, flags collectorFunctionFlags) error {
	client, closeGateway, err := openCollectorFunctionClient(ctx, flags)
	if err != nil {
		return err
	}
	defer closeGateway()
	functions, err := client.ListCloudFunctions(ctx)
	if err != nil {
		return err
	}
	return printJSON(out, buildCollectorFunctionList(client.SpaceID, functions, flags.Drift))
}

func runCollectorFunctionDelete(cmd *cobra.Command, flags collectorFunctionFlags) error {
	names := make([]string, 0, len(flags.Names))
	for _, name := range flags.Names {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	prefix := strings.TrimSpace(flags.Prefix)
	if len(names) == 0 && prefix == "" {
		return errors.New("--name 与 --prefix 至少给一个")
	}
	ctx := cmd.Context()
	client, closeGateway, err := openCollectorFunctionClient(ctx, flags)
	if err != nil {
		return err
	}
	defer closeGateway()
	request := rpcclient.DeleteCloudFunctionsRequest{Names: names, Prefix: prefix, Namespace: strings.TrimSpace(flags.Namespace), Region: strings.ToLower(strings.TrimSpace(flags.Region))}
	if !flags.Yes {
		request.DryRun = true
		preview, err := client.DeleteCloudFunctions(ctx, request)
		if err != nil {
			return err
		}
		targets, refused := []rpcclient.CloudFunctionDeletion{}, []rpcclient.CloudFunctionDeletion{}
		for _, item := range preview {
			if item.Error != "" {
				refused = append(refused, item)
			} else {
				targets = append(targets, item)
			}
		}
		return writePreview(cmd, map[string]any{
			"space_id": client.SpaceID, "request": request, "targets": targets, "refused": refused,
			"note": "targets 是加 --yes 后会删的函数（与实际删除同一套匹配）；计划里的函数删掉后下一轮对账会重建。" + oldAccountFunctionsHint,
		})
	}
	cmd.SilenceUsage = true
	results, err := client.DeleteCloudFunctions(ctx, request)
	if err != nil {
		return err
	}
	summary := map[string]any{"space_id": client.SpaceID, "results": results}
	var failed, running int
	for _, result := range results {
		switch {
		case result.Error != "":
			failed++
		case !result.Done:
			running++
		}
	}
	if running > 0 {
		summary["note"] = fmt.Sprintf("%d 个函数还在删，稍后用 moox-cli collector function list --space-id %s 查看", running, client.SpaceID)
	}
	if err := printJSON(cmd.OutOrStdout(), summary); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d 个函数没删成，原因见 results", failed)
	}
	return nil
}

type collectorProbeReport struct {
	SpaceID             string                     `json:"space_id"`
	Results             []rpcclient.FunctionEgress `json:"results"`
	DistinctOutboundIPs []string                   `json:"distinct_outbound_ips"`
	DistinctCount       int                        `json:"distinct_count"`
	FailedCount         int                        `json:"failed_count"`
}

func buildCollectorProbeReport(spaceID string, probes []rpcclient.FunctionEgress) collectorProbeReport {
	report := collectorProbeReport{SpaceID: spaceID, Results: probes, DistinctOutboundIPs: []string{}}
	seen := map[string]bool{}
	for _, probe := range probes {
		if probe.Error != "" {
			report.FailedCount++
		}
		if ip := strings.TrimSpace(probe.IP); ip != "" && !seen[ip] {
			seen[ip] = true
			report.DistinctOutboundIPs = append(report.DistinctOutboundIPs, ip)
		}
	}
	sort.Strings(report.DistinctOutboundIPs)
	report.DistinctCount = len(report.DistinctOutboundIPs)
	return report
}

func runCollectorFunctionProbe(ctx context.Context, out io.Writer, flags collectorFunctionFlags) error {
	client, closeGateway, err := openCollectorFunctionClient(ctx, flags)
	if err != nil {
		return err
	}
	defer closeGateway()
	probes, err := client.ProbeFunctionEgress(ctx, flags.Names)
	if err != nil {
		return err
	}
	if len(probes) == 0 {
		return errors.New("已生效的计划里没有可探测的采集函数")
	}
	report := buildCollectorProbeReport(client.SpaceID, probes)
	if err := printJSON(out, report); err != nil {
		return err
	}
	if report.FailedCount > 0 {
		return fmt.Errorf("%d 个函数探测失败，原因见 results", report.FailedCount)
	}
	return nil
}
