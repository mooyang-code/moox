package marketstorage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 每个云函数代码包目录（configs/scf/<空间>）里的存储访问配置都要能解析出函数写 Storage 的应用身份：
// 函数每次调用都按包里的 sources/market/binance.yaml 取 spot 这份绑定，密钥来自环境变量。
// 港股、美股那份只留了这一项（没有币安地址和数据源字段），在这里固定它够用。
func TestEverySCFPackageConfigResolvesTheStorageIdentity(t *testing.T) {
	root := filepath.Join("..", "..", "configs", "scf")
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	seen := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path, err := filepath.Abs(filepath.Join(root, entry.Name(), "sources", "market", "binance.yaml"))
		require.NoError(t, err)
		t.Setenv("MOOX_STORAGE_MARKET_CONFIG", path)
		t.Setenv(storageAppKeysEnv, `{"moox-collector":"`+strings.Repeat("a", 64)+`"}`)
		auth, err := ResolveStorageAuthInfo(InstTypeSPOT)
		require.NoError(t, err, entry.Name())
		require.Equal(t, "moox-collector", auth.GetAppId(), entry.Name())
		require.Equal(t, strings.Repeat("a", 64), auth.GetAppKey(), entry.Name())
		seen[entry.Name()] = true
	}
	require.True(t, seen["stockhk"] && seen["stockus"] && seen["stockcn"] && seen["market_data"])
}
