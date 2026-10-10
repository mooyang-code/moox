package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCaddyfileKeepsConsoleOriginContract 校验控制台代理配置：只有一个浏览器站点，上游都是本机回环地址，带安全响应头，
// 不信任 X-Forwarded-*（EdgeOne 回源地址未审核前不配置 trusted_proxies），不再有 11001 站点和网关控制入口。
// 设置 CADDY_BIN 时再用 caddy validate 校验语法。
func TestCaddyfileKeepsConsoleOriginContract(t *testing.T) {
	for _, mode := range []string{TLSModePublic, TLSModeInternal} {
		t.Run(mode, func(t *testing.T) {
			raw, err := renderCaddyfile("106.53.107.122", mode, 9527)
			require.NoError(t, err)
			config := string(raw)
			for _, want := range []string{
				"admin 127.0.0.1:2019",
				"default_sni 106.53.107.122",
				"reverse_proxy 127.0.0.1:9528",
				"reverse_proxy 127.0.0.1:11000",
				"handle /api/admin/*",
				"Content-Security-Policy",
				"X-Frame-Options DENY",
			} {
				require.Contains(t, config, want)
			}
			require.Equal(t, 1, strings.Count(config, "https://106.53.107.122:9527 {"), "只有浏览器入口一个站点")
			for _, unwanted := range []string{"trusted_proxies", "11001", "/api/service/", "/api/gateway", "11002"} {
				require.NotContains(t, config, unwanted)
			}
			if mode == TLSModePublic {
				require.Contains(t, config, "issuer acme")
			} else {
				require.Contains(t, config, "tls internal")
			}
			if caddy := os.Getenv("CADDY_BIN"); caddy != "" {
				path := filepath.Join(t.TempDir(), "Caddyfile")
				require.NoError(t, os.WriteFile(path, raw, 0o600))
				output, err := exec.Command(caddy, "validate", "--config", path, "--adapter", "caddyfile").CombinedOutput()
				require.NoError(t, err, string(output))
			}
		})
	}
	_, err := renderCaddyfile("bad host", TLSModePublic, 9527)
	require.Error(t, err)
	_, err = renderCaddyfile("evil}{", TLSModePublic, 9527)
	require.Error(t, err)
}
