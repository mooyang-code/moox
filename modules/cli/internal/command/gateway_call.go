package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/spf13/cobra"
)

// newGatewayCallCommand 通过运维 SSH 网关调用任意内部服务方法（JSON 序列化），
// 用于没有专用命令的管理接口，如 StrategyMgr 的策略定义与实例。
func newGatewayCallCommand() *cobra.Command {
	var file, space, data string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "call <service> <method>",
		Short: "调用内部服务方法（JSON）",
		Long:  "service 写完整服务名，例如 trpc.moox.strategy.StrategyMgr；--data 为 JSON 文本，@文件 读取文件，- 读取标准输入。",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := readGatewayCallBody(data)
			if err != nil {
				return err
			}
			var request any
			if err := json.Unmarshal(body, &request); err != nil {
				return fmt.Errorf("--data 不是合法的 JSON: %w", err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			gateway, err := openCommandGateway(ctx, file, nil)
			if err != nil {
				return err
			}
			defer gateway.Close()
			client := &adminclient.Client{Gateway: gateway, SpaceID: space}
			var response json.RawMessage
			if err := client.CallGatewayJSON(ctx, args[0], args[1], request, &response); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(response))
			return err
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&space, "space", "crypto", "请求携带的空间 ID")
	cmd.Flags().StringVar(&data, "data", "{}", "请求 JSON；@文件 读取文件，- 读取标准输入")
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "调用超时")
	return cmd
}

func readGatewayCallBody(data string) ([]byte, error) {
	switch {
	case data == "-":
		return io.ReadAll(os.Stdin)
	case strings.HasPrefix(data, "@"):
		return os.ReadFile(strings.TrimPrefix(data, "@"))
	default:
		return []byte(data), nil
	}
}

func init() {
	gatewayCmd := &cobra.Command{Use: "gateway", Short: "内部网关调试工具"}
	gatewayCmd.AddCommand(newGatewayCallCommand())
	rootCmd.AddCommand(gatewayCmd)
}
