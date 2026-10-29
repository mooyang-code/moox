package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
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
		Long:  "service 写完整服务名，例如 trpc.moox.strategy.StrategyMgr（可用的服务与方法见 gateway methods）；--data 为 JSON 文本，@文件 读取文件，- 读取标准输入。请求与返回的字段见各模块 proto 与 skill 的接口配方。",
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

// newGatewayMethodsCommand 从内嵌的服务目录列出内部服务与方法，标出只读方法和 moox-cli 是否有权调用；不连网。
func newGatewayMethodsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "methods [服务名关键字]",
		Short: "列出 gateway call 可调用的内部服务与方法（只读，不连网）",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			catalog, err := servicecatalog.LoadEmbedded()
			if err != nil {
				return err
			}
			keyword := ""
			if len(args) == 1 {
				keyword = strings.ToLower(args[0])
			}
			out := cmd.OutOrStdout()
			for _, component := range catalog.Components {
				for _, service := range component.Services {
					if keyword != "" && !strings.Contains(strings.ToLower(service.Path), keyword) {
						continue
					}
					allowed := map[string]bool{}
					for _, grant := range service.ACL {
						if slices.Contains(grant.Callers, "moox-cli") {
							for _, method := range grant.Methods {
								allowed[method] = true
							}
						}
					}
					if len(allowed) == 0 {
						continue
					}
					fmt.Fprintf(out, "%s（组件 %s）\n", service.Path, component.ID)
					for _, method := range service.Methods {
						kind := "写"
						if slices.Contains(service.ReadOnlyMethods, method) {
							kind = "只读"
						}
						if !allowed[method] {
							kind += "，moox-cli 无权调用"
						}
						fmt.Fprintf(out, "  %-36s %s\n", method, kind)
					}
				}
			}
			return nil
		},
	}
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
	gatewayCmd := &cobra.Command{Use: "gateway", Short: "直接调用内部服务（没有专用命令的管理接口）"}
	gatewayCmd.AddCommand(newGatewayCallCommand(), newGatewayMethodsCommand())
	rootCmd.AddCommand(gatewayCmd)
}
