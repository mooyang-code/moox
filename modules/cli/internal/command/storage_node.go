package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/spf13/cobra"
)

// storageNodeAPI 是修改与删除存储节点登记需要的 Storage 元数据能力。
// 控制台只展示节点；修改和删除只能由 moox-cli 经网关调用（网关按调用方身份限定）。
type storageNodeAPI interface {
	UpdateDataNode(context.Context, *storagepb.UpdateDataNodeReq) (*storagepb.UpdateDataNodeRsp, error)
	DeleteDataNode(context.Context, *storagepb.DeleteDataNodeReq) (*storagepb.DeleteDataNodeRsp, error)
}

// openStorageNode 打开经操作员网关的 Storage 元数据代理；测试里替换。
var openStorageNode = func(ctx context.Context, file string) (storageNodeAPI, io.Closer, error) {
	gateway, err := openCommandGateway(ctx, file, (*setupconfig.Snapshot)(nil))
	if err != nil {
		return nil, nil, err
	}
	return &storageMetadataProxy{gateway: gateway}, gateway, nil
}

func newStorageNodeCommand() *cobra.Command {
	node := &cobra.Command{Use: "node", Short: "存储节点登记的修改与删除（控制台只展示）"}
	var file, nodeID, name, status string
	var yes bool
	update := &cobra.Command{
		Use:   "update",
		Short: "修改存储节点的名称或状态",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			nodeID, name, status = strings.TrimSpace(nodeID), strings.TrimSpace(name), strings.TrimSpace(status)
			if nodeID == "" {
				return errors.New("--node-id 必填")
			}
			if name == "" && status == "" {
				return errors.New("至少给出 --name 或 --status 之一")
			}
			api, closer, err := openStorageNode(cmd.Context(), file)
			if err != nil {
				return err
			}
			defer closer.Close()
			rsp, err := api.UpdateDataNode(cmd.Context(), &storagepb.UpdateDataNodeReq{NodeId: nodeID, Name: name, Status: status})
			if err != nil {
				return fmt.Errorf("UpdateDataNode: %w", err)
			}
			if info := rsp.GetRetInfo(); info.GetCode() != 0 {
				return fmt.Errorf("UpdateDataNode: %s (code %d)", info.GetMsg(), info.GetCode())
			}
			return writeSetupJSON(cmd, map[string]any{"status": "updated", "node_id": nodeID, "name": rsp.GetNode().GetName(), "node_status": rsp.GetNode().GetStatus()})
		},
	}
	update.Flags().StringVar(&file, "file", "./moox.toml", "部署配置文件；用于解析操作员网关")
	update.Flags().StringVar(&nodeID, "node-id", "", "存储节点 ID")
	update.Flags().StringVar(&name, "name", "", "新名称")
	update.Flags().StringVar(&status, "status", "", "新状态")
	remove := &cobra.Command{
		Use:   "delete",
		Short: "删除一个存储节点登记",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			nodeID = strings.TrimSpace(nodeID)
			if nodeID == "" {
				return errors.New("--node-id 必填")
			}
			if !yes {
				return errors.New("删除存储节点登记不可恢复（仍被数据集引用的节点会被拒绝）；确认后加 --yes")
			}
			api, closer, err := openStorageNode(cmd.Context(), file)
			if err != nil {
				return err
			}
			defer closer.Close()
			rsp, err := api.DeleteDataNode(cmd.Context(), &storagepb.DeleteDataNodeReq{NodeId: nodeID})
			if err != nil {
				return fmt.Errorf("DeleteDataNode: %w", err)
			}
			if info := rsp.GetRetInfo(); info.GetCode() != 0 {
				return fmt.Errorf("DeleteDataNode: %s (code %d)", info.GetMsg(), info.GetCode())
			}
			return writeSetupJSON(cmd, map[string]any{"status": "deleted", "node_id": nodeID})
		},
	}
	remove.Flags().StringVar(&file, "file", "./moox.toml", "部署配置文件；用于解析操作员网关")
	remove.Flags().StringVar(&nodeID, "node-id", "", "存储节点 ID")
	remove.Flags().BoolVar(&yes, "yes", false, "确认删除")
	node.AddCommand(update, remove)
	return node
}
