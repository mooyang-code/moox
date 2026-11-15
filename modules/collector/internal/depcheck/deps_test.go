// Package depcheck 记录并检查执行面程序（云函数程序、collector-stream）的依赖清单。
package depcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// executionBinaries 是执行面的两个程序：云函数程序与 collector-stream。
var executionBinaries = map[string]string{
	"scf_market_data": "./cmd/scf/market_data",
	"stream":          "./cmd/stream",
}

// listDependencies 返回一个程序的全部非标准库依赖（go list -deps 的顺序）。
// 固定按 linux/amd64 解析，结果不随本机平台变化；标准库的包随 Go 版本变化，不记。
func listDependencies(t *testing.T, target string) []string {
	t.Helper()
	command := exec.Command("go", "list", "-deps", target)
	command.Dir = filepath.Join("..", "..")
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", target, err)
	}
	var dependencies []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		first, _, _ := strings.Cut(line, "/")
		if strings.Contains(first, ".") {
			dependencies = append(dependencies, line)
		}
	}
	return dependencies
}

// 执行面程序的依赖清单与记录一致。清单变化要有意为之：确认后设 MOOX_UPDATE_GOLDEN=1 重新生成。
func TestExecutionBinaryDependenciesMatchBaseline(t *testing.T) {
	for name, target := range executionBinaries {
		got := strings.Join(listDependencies(t, target), "\n") + "\n"
		path := filepath.Join("testdata", "deps_"+name+".txt")
		if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(want) != got {
			t.Errorf("%s 的依赖清单与 %s 不一致；确认变化符合预期后设 MOOX_UPDATE_GOLDEN=1 重新生成", target, path)
		}
	}
}

// controlPlanePackages 是执行面程序的依赖里不得出现的包：Collector 的库与 SQLite 驱动、控制面、CloudFunction 的协议与客户端。
// 不写裸的 cloudfunction：云函数程序引的第三方包 github.com/tencentyun/scf-go-lib/cloudfunction 是合法依赖。
var controlPlanePackages = []string{
	"/modules/collector/internal/store", "/modules/collector/internal/fetchplan", "/modules/collector/internal/scfinvoker",
	"/modules/collector/internal/egressbalance", "/modules/cloudfunction", "gorm.io/", "sqlite",
}

// 云函数程序与 collector-stream 不带 store、SQLite 驱动、控制面与 CloudFunction 的协议。
func TestExecutionBinariesExcludeControlPlane(t *testing.T) {
	for _, target := range executionBinaries {
		for _, dependency := range listDependencies(t, target) {
			for _, forbidden := range controlPlanePackages {
				if strings.Contains(dependency, forbidden) {
					t.Errorf("%s 依赖了执行面不该带的 %s", target, dependency)
				}
			}
		}
	}
}
