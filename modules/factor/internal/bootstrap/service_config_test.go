package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestFactorMgrServicesUseTRPC 校验经主机网关路由的服务都监听本机 tRPC，端口与组件目录一致：因子引擎经外部接入
// 以 tRPC 调用 FactorEngine，FactorMgr 由主机网关以 tRPC 转发。
func TestFactorMgrServicesUseTRPC(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "trpc_go.yaml"))
	require.NoError(t, err)
	var config struct {
		Server struct {
			Service []struct {
				Name     string `yaml:"name"`
				IP       string `yaml:"ip"`
				Port     int    `yaml:"port"`
				Protocol string `yaml:"protocol"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &config))
	want := map[string]int{"trpc.moox.factor.FactorMgr": 11403, "trpc.moox.factor.FactorEngine": 11405}
	for _, service := range config.Server.Service {
		port, ok := want[service.Name]
		if !ok {
			continue
		}
		require.Equal(t, "trpc", service.Protocol, service.Name)
		require.Equal(t, "127.0.0.1", service.IP, service.Name)
		require.Equal(t, port, service.Port, service.Name)
		delete(want, service.Name)
	}
	require.Empty(t, want, "trpc_go.yaml 缺少经主机网关路由的服务")
}
