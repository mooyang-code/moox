package command

import (
	"debug/elf"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/collectorpackager"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	"github.com/stretchr/testify/require"
)

// Collector 登记的每个市场（config/markets 下有 market.yaml 的目录）都要有云函数代码包的运行配置目录，
// 否则 moox-cli 给这个空间打代码包时找不到配置。新增市场时照 A 股在 configs/scf/<空间>/ 下加一份。
// 用这份目录真的打一个包：包里带上存储访问配置，并通过上传前的校验（不含凭据、没有多余文件）。
func TestEveryCollectorMarketHasAnSCFPackageConfigDir(t *testing.T) {
	collectorRoot := filepath.Join("..", "..", "..", "collector")
	entries, err := os.ReadDir(filepath.Join(collectorRoot, "config", "markets"))
	require.NoError(t, err)
	markets := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(collectorRoot, "config", "markets", entry.Name(), "market.yaml")); err != nil {
			continue
		}
		markets++
		binding := filepath.Join(collectorRoot, "configs", filepath.FromSlash(defaultCollectorPackageConfigDir(entry.Name())), "sources", "market", "binance.yaml")
		_, err := os.Stat(binding)
		require.NoError(t, err, "市场 %s 缺少云函数代码包的存储访问配置", entry.Name())

		binary := filepath.Join(t.TempDir(), "main")
		require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
		result, err := collectorpackager.BuildSCFPackage(collectorpackager.BuildSCFPackageOptions{
			BinaryPath: binary, ConfigDir: filepath.Dir(filepath.Dir(filepath.Dir(binding))), OutPath: filepath.Join(t.TempDir(), "package.zip"),
		})
		require.NoError(t, err, entry.Name())
		require.Equal(t, []string{"main", "sources/market/binance.yaml"}, result.Entries, entry.Name())
	}
	require.GreaterOrEqual(t, markets, 3)
}
