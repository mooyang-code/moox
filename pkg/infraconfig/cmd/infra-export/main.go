// cmd/infra-export 读取 infra 配置并输出 `export KEY=VALUE` 行，
// 供部署脚本通过 `eval "$(go run ./pkg/infraconfig/cmd/infra-export)"` source。
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/pkg/infraconfig"
)

func main() {
	shell := flag.String("shell", "bash", "输出格式：bash|plain")
	flag.Parse()
	c, err := infraconfig.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "infra-export: %v\n", err)
		os.Exit(1)
	}
	prefix := ""
	if *shell == "plain" {
		prefix = ""
	} else {
		prefix = "export "
	}
	emit := func(k, v string) {
		if v == "" {
			v = ""
		}
		fmt.Printf("%s%s=%q\n", prefix, k, v)
	}
	emit("STORAGE_URL", c.Services.StorageAccess.URL())
	emit("XDATA_URL", c.Services.XData.URL())
	emit("ADMIN_GATEWAY_HOST", c.Services.AdminGateway.Host)
	emit("ADMIN_GATEWAY_PORT", fmt.Sprintf("%d", c.Services.AdminGateway.Port))
	emit("WEB_HOST_HOST", c.Services.WebHost.Host)
	emit("WEB_HOST_PORT", fmt.Sprintf("%d", c.Services.WebHost.Port))
	emit("TRADE_HOST", c.Services.Trade.Host)
	emit("TRADE_PORT", fmt.Sprintf("%d", c.Services.Trade.Port))
	emit("REMOTE_HOST", c.Remote.Host)
	emit("REMOTE_SSH", c.Remote.SSH)
}
