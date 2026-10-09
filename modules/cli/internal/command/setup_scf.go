package command

import (
	"encoding/json"
	"fmt"
	"strings"

	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

// newSetupInspectSCFCommand 读取一个腾讯云函数的网络配置与外部接入地址，不输出凭据和完整环境变量。
func newSetupInspectSCFCommand(deps setupDeps) *cobra.Command {
	var file, region, namespace, functionName string
	cmd := &cobra.Command{
		Use:   "inspect-scf",
		Short: "检查腾讯云函数网络配置",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			region = strings.TrimSpace(region)
			if region == "" {
				region = snapshot.Manifest.TencentCloud.Region
			}
			namespace = firstNonEmpty(namespace, "default")
			if strings.TrimSpace(functionName) == "" {
				return fmt.Errorf("必须指定 --function")
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
				SecretID:  snapshot.Manifest.TencentCloud.SecretID,
				SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
				Region:    region,
			})
			if err != nil {
				return err
			}
			fn, err := network.ForRegion(region).GetSCFFunction(cmd.Context(), namespace, strings.TrimSpace(functionName))
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{
				"region": region, "namespace": namespace, "function": functionName,
				"status": fn.Status, "vpc_id": fn.VpcID, "subnet_id": fn.SubnetID,
				"public_net_status": fn.PublicNetStatus,
				"access_address":    fn.Environment[cloudtencent.EnvAccessAddress],
				"access_id":         fn.Environment[cloudtencent.EnvAccessID],
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

// newSetupAttachSCFVPCCommand 为单个函数绑定 VPC 子网，用于在批量发布前单独验证私网路由。
func newSetupAttachSCFVPCCommand(deps setupDeps) *cobra.Command {
	var file, region, namespace, functionName, vpcID, subnetID string
	cmd := &cobra.Command{
		Use:   "attach-scf-vpc",
		Short: "为腾讯云函数绑定 VPC 子网",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			region = firstNonEmpty(strings.TrimSpace(region), snapshot.Manifest.TencentCloud.Region)
			namespace = firstNonEmpty(strings.TrimSpace(namespace), "default")
			if strings.TrimSpace(functionName) == "" || strings.TrimSpace(vpcID) == "" || strings.TrimSpace(subnetID) == "" {
				return fmt.Errorf("必须指定 --function、--vpc-id 和 --subnet-id")
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
				SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey, Region: region,
			})
			if err != nil {
				return err
			}
			fn, err := network.ForRegion(region).GetSCFFunction(cmd.Context(), namespace, functionName)
			if err != nil {
				return err
			}
			status := firstNonEmpty(fn.PublicNetStatus, "ENABLE")
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
	_ = cmd.MarkFlagRequired("function")
	_ = cmd.MarkFlagRequired("vpc-id")
	_ = cmd.MarkFlagRequired("subnet-id")
	return cmd
}

// newSetupInvokeSCFCommand 经 SCF 管理接口直接调用一个函数，云节点服务不可用时用于验证连通性。
func newSetupInvokeSCFCommand(deps setupDeps) *cobra.Command {
	var file, region, namespace, functionName, eventJSON string
	cmd := &cobra.Command{
		Use:   "invoke-scf",
		Short: "直接调用腾讯云函数",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			region = firstNonEmpty(strings.TrimSpace(region), snapshot.Manifest.TencentCloud.Region)
			namespace = firstNonEmpty(strings.TrimSpace(namespace), "default")
			if strings.TrimSpace(functionName) == "" {
				return fmt.Errorf("必须指定 --function")
			}
			var event any = map[string]any{"action": "market_fetch", "space_id": "crypto", "subject_id": "BTC-USDT"}
			if strings.TrimSpace(eventJSON) != "" {
				if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
					return fmt.Errorf("--event 不是合法的 JSON: %w", err)
				}
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey, Region: region})
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
