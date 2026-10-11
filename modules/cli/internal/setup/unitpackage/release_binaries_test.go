package unitpackage

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 发布脚本的装箱清单必须覆盖所有部署单元需要的二进制，否则发布包无法用于原生部署。
func TestReleaseBinaryListCoversEveryDeploymentProfile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "scripts", "build", "build-release-binaries.sh"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)binary_names=\((.*?)\n\)`).FindSubmatch(raw)
	if block == nil {
		t.Fatal("binary_names not found in build-release-binaries.sh")
	}
	script := string(raw)
	listed := map[string]bool{}
	for _, name := range strings.Fields(string(block[1])) {
		listed[name] = true
	}
	// Linux 才有的二进制在脚本里单独追加。
	for _, name := range regexp.MustCompile(`binary_names\+=\(([^)]*)\)`).FindAllStringSubmatch(script, -1) {
		for _, field := range strings.Fields(name[1]) {
			listed[field] = true
		}
	}
	for profile := range unitProfiles {
		names, err := Binaries(profile)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if !listed[name] {
				t.Errorf("profile %s needs %s, which build-release-binaries.sh does not package", profile, name)
			}
		}
	}
}
