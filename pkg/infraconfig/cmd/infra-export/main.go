// cmd/infra-export 把 infra 配置导出为 shell `export` 行，供部署脚本 source。
//
// 用法：
//   eval "$(go run ./cmd/infra-export)"
//   eval "$(go run ./cmd/infra-export -root /path/to/repo)"
//
// 输出示例：
//   export REMOTE_HOST=43.132.204.177
//   export REMOTE_SSH=ubuntu@43.132.204.177
//   export STORAGE_URL=http://106.53.107.122:20201
//   export XDATA_URL=http://106.53.107.122:20201
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/pkg/infraconfig"
)

func main() {
	root := flag.String("root", "", "仓库根目录（默认自动回溯定位 infra/）")
	flag.Parse()

	if *root != "" {
		// 指定仓库根时，直接指向其 infra/infra.yaml，避免依赖 cwd。
		if err := os.Setenv("MOOX_INFRA_CONFIG", filepath.Join(*root, "infra", "infra.yaml")); err != nil {
			fmt.Fprintln(os.Stderr, "set MOOX_INFRA_CONFIG:", err)
			os.Exit(1)
		}
		infraconfig.Reset()
	}
	cfg, err := infraconfig.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "infraconfig load:", err)
		os.Exit(1)
	}

	exports := [][2]string{
		{"REMOTE_HOST", cfg.Remote.Host},
		{"REMOTE_SSH", cfg.Remote.SSH},
		{"STORAGE_URL", cfg.Services.StorageAccess.URL()},
		{"XDATA_URL", cfg.Services.XData.URL()},
		{"ADMIN_GATEWAY_HOST", cfg.Services.AdminGateway.Host},
		{"ADMIN_GATEWAY_PORT", fmt.Sprintf("%d", cfg.Services.AdminGateway.Port)},
		{"WEB_HOST_HOST", cfg.Services.WebHost.Host},
		{"WEB_HOST_PORT", fmt.Sprintf("%d", cfg.Services.WebHost.Port)},
		{"TRADE_HOST", cfg.Services.Trade.Host},
		{"TRADE_PORT", fmt.Sprintf("%d", cfg.Services.Trade.Port)},
	}
	for _, kv := range exports {
		if kv[1] == "" || kv[1] == "<deploy-host>" || kv[1] == "ubuntu@<deploy-host>" {
			// 占位值不导出，避免部署脚本误用。
			continue
		}
		fmt.Printf("export %s=%s\n", kv[0], shellQuote(kv[1]))
	}
}

// shellQuote 对含特殊字符的值加单引号。
func shellQuote(v string) string {
	needQuote := false
	for _, r := range v {
		if r == ' ' || r == '"' || r == '\'' || r == '$' || r == '`' || r == '&' || r == '|' || r == '<' || r == '>' || r == ';' || r == '*' || r == '?' || r == '(' || r == ')' {
			needQuote = true
			break
		}
	}
	if !needQuote {
		return v
	}
	return "'" + v + "'"
}
