package command

import (
	"encoding/json"
	"fmt"
	"strings"

	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

func init() {
	collectorFunctionCmd.AddCommand(
		newCollectorFunctionInspectCommand(loadSetupSnapshot),
		newCollectorFunctionAttachVPCCommand(loadSetupSnapshot),
		newCollectorFunctionInvokeCommand(loadSetupSnapshot),
	)
}

// newCollectorFunctionInspectCommand 直接调腾讯云 SCF 读取单个函数的网络配置（不输出凭据与完整环境），
// 用于部署验收 VPC/子网与入口地址，或查找旧云账号下的残留函数。
func newCollectorFunctionInspectCommand(load func(string) (*setupSnapshot, error)) *cobra.Command {
	var file, region, namespace, functionName string
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "检查腾讯云函数网络配置（直调腾讯云 SCF）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			region = strings.TrimSpace(region)
			if region == "" {
				region = snapshot.Manifest.CloudRegion()
			}
			namespace = firstNonEmpty(namespace, "default")
			if strings.TrimSpace(functionName) == "" {
				return fmt.Errorf("--function 必填")
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
				SecretID:  snapshot.Secrets.TencentCloud.SecretID,
				SecretKey: snapshot.Secrets.TencentCloud.SecretKey,
				Region:    region,
			})
			if err != nil {
				return err
			}
			fn, err := network.ForRegion(region).GetSCFFunction(cmd.Context(), namespace, strings.TrimSpace(functionName))
			if err != nil {
				return err
			}
			entryAddress := ""
			if fn.Environment != nil {
				entryAddress = fn.Environment["MOOX_ENTRY_ADDRESS"]
			}
			return writeSetupJSON(cmd, map[string]any{
				"region": region, "namespace": namespace, "function": functionName,
				"status": fn.Status, "vpc_id": fn.VpcID, "subnet_id": fn.SubnetID,
				"public_net_status": fn.PublicNetStatus,
				"entry_address":     entryAddress,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&region, "region", "", "SCF 地域")
	cmd.Flags().StringVar(&namespace, "namespace", "default", "SCF 命名空间")
	cmd.Flags().StringVar(&functionName, "function", "", "SCF 函数名")
	_ = cmd.MarkFlagRequired("function")
	return cmd
}

// newCollectorFunctionAttachVPCCommand 直接为单个腾讯云函数绑定 VPC 子网，用于网络排障验证。
func newCollectorFunctionAttachVPCCommand(load func(string) (*setupSnapshot, error)) *cobra.Command {
	var file, region, namespace, functionName, vpcID, subnetID string
	var yes bool
	cmd := &cobra.Command{
		Use:   "attach-vpc",
		Short: "为腾讯云函数绑定 VPC 子网（直调腾讯云 SCF；默认只预览，--yes 才绑定）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			region = firstNonEmpty(strings.TrimSpace(region), snapshot.Manifest.CloudRegion())
			namespace = firstNonEmpty(strings.TrimSpace(namespace), "default")
			if strings.TrimSpace(functionName) == "" || strings.TrimSpace(vpcID) == "" || strings.TrimSpace(subnetID) == "" {
				return fmt.Errorf("--function、--vpc-id 与 --subnet-id 必填")
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
				SecretID: snapshot.Secrets.TencentCloud.SecretID, SecretKey: snapshot.Secrets.TencentCloud.SecretKey, Region: region,
			})
			if err != nil {
				return err
			}
			fn, err := network.ForRegion(region).GetSCFFunction(cmd.Context(), namespace, functionName)
			if err != nil {
				return err
			}
			status := firstNonEmpty(fn.PublicNetStatus, "ENABLE")
			if !yes {
				return writePreview(cmd, map[string]any{
					"action": "attach_scf_vpc", "region": region, "namespace": namespace, "function": functionName,
					"current_vpc_id": fn.VpcID, "current_subnet_id": fn.SubnetID, "vpc_id": vpcID, "subnet_id": subnetID, "public_net_status": status,
				})
			}
			if err := network.ForRegion(region).UpdateSCFNetwork(cmd.Context(), namespace, functionName, vpcID, subnetID, status, fn.Environment); err != nil {
				return err
			}
			updated, err := network.ForRegion(region).GetSCFFunction(cmd.Context(), namespace, functionName)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"region": region, "namespace": namespace, "function": functionName, "status": updated.Status, "vpc_id": updated.VpcID, "subnet_id": updated.SubnetID, "public_net_status": updated.PublicNetStatus})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&region, "region", "", "SCF 地域")
	cmd.Flags().StringVar(&namespace, "namespace", "default", "SCF 命名空间")
	cmd.Flags().StringVar(&functionName, "function", "", "SCF 函数名")
	cmd.Flags().StringVar(&vpcID, "vpc-id", "", "VPC ID")
	cmd.Flags().StringVar(&subnetID, "subnet-id", "", "子网 ID")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认绑定；不加只预览")
	_ = cmd.MarkFlagRequired("function")
	_ = cmd.MarkFlagRequired("vpc-id")
	_ = cmd.MarkFlagRequired("subnet-id")
	return cmd
}

// newCollectorFunctionInvokeCommand 直接经腾讯云 SCF 的控制接口调用一个函数，不经 Collector；Collector 不可用时用来证明连通性。
func newCollectorFunctionInvokeCommand(load func(string) (*setupSnapshot, error)) *cobra.Command {
	var file, region, namespace, functionName, eventJSON string
	cmd := &cobra.Command{
		Use:   "invoke",
		Short: "直接调用腾讯云函数（直调腾讯云 SCF）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			region = firstNonEmpty(strings.TrimSpace(region), snapshot.Manifest.CloudRegion())
			namespace = firstNonEmpty(strings.TrimSpace(namespace), "default")
			if strings.TrimSpace(functionName) == "" {
				return fmt.Errorf("--function 必填")
			}
			var event any = map[string]any{"action": "market_fetch", "space_id": "crypto", "subject_id": "BTC-USDT"}
			if strings.TrimSpace(eventJSON) != "" {
				if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
					return fmt.Errorf("event JSON 无效: %w", err)
				}
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{SecretID: snapshot.Secrets.TencentCloud.SecretID, SecretKey: snapshot.Secrets.TencentCloud.SecretKey, Region: region})
			if err != nil {
				return err
			}
			result, err := network.ForRegion(region).InvokeSCF(cmd.Context(), namespace, functionName, event)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"region": region, "namespace": namespace, "function": functionName, "request_id": result.RequestID, "result": result.Result, "log": result.Log})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&region, "region", "", "SCF 地域")
	cmd.Flags().StringVar(&namespace, "namespace", "default", "SCF 命名空间")
	cmd.Flags().StringVar(&functionName, "function", "", "SCF 函数名")
	cmd.Flags().StringVar(&eventJSON, "event", "", "JSON 事件；缺省为 crypto BTC-USDT market_fetch")
	_ = cmd.MarkFlagRequired("function")
	return cmd
}
