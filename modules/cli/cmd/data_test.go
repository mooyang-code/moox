package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestDataCommandsRequireStorageURLAndDoNotExposeLocalStorageRoot(t *testing.T) {
	for _, cmd := range []*cobra.Command{dataCSVImportCmd, dataRowsExportCmd} {
		if flag := cmd.Flags().Lookup("storage-root"); flag != nil {
			t.Fatalf("%s exposes deprecated --storage-root flag", cmd.CommandPath())
		}
		if flag := cmd.Flags().Lookup("storage-url"); flag == nil {
			t.Fatalf("%s must expose --storage-url", cmd.CommandPath())
		}
	}
}

func TestDataCSVImportExposesChineseDisplayNameFlags(t *testing.T) {
	for _, name := range []string{"dataset-name", "data-source-name", "subject-name", "field-config"} {
		if flag := dataCSVImportCmd.Flags().Lookup(name); flag == nil {
			t.Fatalf("data csv import missing --%s", name)
		}
	}
}

func TestDataCommandsDoNotDefaultBusinessDatasetOrSource(t *testing.T) {
	if flag := dataCSVImportCmd.Flags().Lookup("dataset"); flag == nil || flag.DefValue != "" {
		t.Fatalf("data csv import --dataset default = %q, want empty", dataFlagDefault(flag))
	}
	if flag := dataRowsExportCmd.Flags().Lookup("dataset"); flag == nil || flag.DefValue != "" {
		t.Fatalf("data rows export --dataset default = %q, want empty", dataFlagDefault(flag))
	}
	if flag := dataCSVImportCmd.Flags().Lookup("data-source"); flag == nil || flag.DefValue != "" {
		t.Fatalf("data csv import --data-source default = %q, want empty", dataFlagDefault(flag))
	}
}

func TestRemoteMetadataDisplayNamesAreChineseAndShort(t *testing.T) {
	got, err := resolveChineseDisplayName("数据集", "", "导入K线")
	if err != nil {
		t.Fatalf("resolveChineseDisplayName returned error: %v", err)
	}
	if got != "导入K线" {
		t.Fatalf("default display name = %q, want 导入K线", got)
	}
	if _, err := resolveChineseDisplayName("数据集", "Dataset", "导入K线"); err == nil {
		t.Fatal("english display name should be rejected")
	}
	if _, err := resolveChineseDisplayName("数据集", "这是一个超过十个字符的名称", "导入K线"); err == nil {
		t.Fatal("overlong display name should be rejected")
	}
	configPath := filepath.Join(t.TempDir(), "fields.yaml")
	if err := os.WriteFile(configPath, []byte("column_display_names:\n  close: 收盘价\n  custom_alpha: 阿尔法\n"), 0o644); err != nil {
		t.Fatalf("write field config: %v", err)
	}
	displayNames, err := loadColumnDisplayNames(configPath)
	if err != nil {
		t.Fatalf("loadColumnDisplayNames returned error: %v", err)
	}
	if got := remoteColumnDisplayName(displayNames, "close"); got != "收盘价" {
		t.Fatalf("close display name = %q, want 收盘价", got)
	}
	if got := remoteColumnDisplayName(displayNames, "custom_alpha"); got != "阿尔法" {
		t.Fatalf("custom_alpha display name = %q, want 阿尔法", got)
	}
	if got := remoteColumnDisplayName(displayNames, "unknown_column"); got != "导入列" {
		t.Fatalf("unknown display name = %q, want 导入列", got)
	}
}

func TestRemoteColumnDisplayNamesAreNotHardCodedInGo(t *testing.T) {
	source, err := os.ReadFile("data_remote.go")
	if err != nil {
		t.Fatalf("read data_remote.go: %v", err)
	}
	for _, forbidden := range []string{"开盘价", "最高价", "最低价", "收盘价", "成交量", "成交额", "资金费率"} {
		if strings.Contains(string(source), `"`+forbidden+`"`) {
			t.Fatalf("field display name %q must live in YAML config, not data_remote.go", forbidden)
		}
	}
}

func dataFlagDefault(flag *pflag.Flag) string {
	if flag == nil {
		return "<missing>"
	}
	return flag.DefValue
}
