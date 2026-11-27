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

// 所有市场参数与数据源运行配置（含 crypto/sources/binance.yaml）均内嵌在采集器二进制内；
// SCF 代码包只含单一 main 二进制，不再依赖外部 configs/scf 配置目录。
func TestSCFPackageIsBinaryOnlyAndBinanceConfigIsEmbedded(t *testing.T) {
	collectorRoot := filepath.Join("..", "..", "..", "collector")
	embeddedBinance := filepath.Join(collectorRoot, "config", "markets", "crypto", "sources", "binance.yaml")
	_, err := os.Stat(embeddedBinance)
	require.NoError(t, err, "binance.yaml 应内嵌在 config/markets/crypto/sources/binance.yaml")

	_, err = os.Stat(filepath.Join(collectorRoot, "configs", "scf"))
	require.ErrorIs(t, err, os.ErrNotExist, "外部 configs/scf 目录应已裁撤")

	binary := filepath.Join(t.TempDir(), "main")
	require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
	result, err := collectorpackager.BuildSCFPackage(collectorpackager.BuildSCFPackageOptions{
		BinaryPath: binary,
		OutPath:    filepath.Join(t.TempDir(), "package.zip"),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"main"}, result.Entries)
}
