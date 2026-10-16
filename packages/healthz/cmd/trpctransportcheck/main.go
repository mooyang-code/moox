// trpctransportcheck 检查所有 tcp + trpc 服务端都显式使用 go-net 传输层。
//
// Linux 下 tRPC 对未声明 transport 的 tcp + trpc 服务默认启用 tnet，其空闲定时器会把每 30～60 秒才复用一次的连接
// 提前关掉，调用方随后读到 ReadFrame EOF（171）。这里用 tRPC 自己的配置加载判断每个服务最终生效的
// network、protocol、transport（含框架默认值和 server 级继承），而不是按文本匹配。
//
// 用法：go run ./cmd/trpctransportcheck <modules 目录>；有问题时逐行输出并以 1 退出。
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	trpc "trpc.group/trpc-go/trpc-go"
)

func main() {
	root := "modules"
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	findings, err := check(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, finding := range findings {
		fmt.Println(finding)
	}
	if len(findings) > 0 {
		os.Exit(1)
	}
}

func check(root string) ([]string, error) {
	var files []string
	for _, pattern := range []string{"*/config/trpc_go*.yaml", "*/config/*/trpc_go*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return nil, err
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		return nil, errors.New("未找到任何 tRPC 配置：" + root)
	}
	sort.Strings(files)
	var findings []string
	for _, file := range files {
		cfg, err := trpc.LoadConfig(file)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		for _, service := range cfg.Server.Service {
			if defaultsToTNet(service) {
				findings = append(findings, fmt.Sprintf("%s: %s 使用 transport %q，tcp + trpc 服务端必须显式声明 transport: go-net",
					file, service.Name, service.Transport))
			}
		}
	}
	return findings, nil
}

// defaultsToTNet 对应 tRPC 在 Linux 上自动切换 tnet 的条件：tcp/tcp4/tcp6 + trpc。显式写成别的传输层也不允许。
func defaultsToTNet(service *trpc.ServiceConfig) bool {
	switch service.Network {
	case "tcp", "tcp4", "tcp6":
	default:
		return false
	}
	return service.Protocol == "trpc" && service.Transport != "go-net"
}
