// Package depcheck 记录并检查执行面程序（云函数程序、collector-stream）的依赖清单。
package depcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	// 两个入口程序直接引入的包。测试靠子进程跑 go list，测试程序自己不引这些包的话，
	// 依赖图变了测试缓存也不会失效（普通 go test 会返回上一次的 ok）。入口程序新增直接引入时这里要跟着加。
	_ "github.com/mooyang-code/moox/modules/collector/internal/serverless/market_data"
	_ "github.com/mooyang-code/moox/modules/collector/internal/streamapp"
	_ "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	_ "github.com/mooyang-code/moox/packages/clsreporter/logcls"
	_ "github.com/mooyang-code/moox/packages/healthz"
	_ "github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "github.com/mooyang-code/moox/packages/report"
)

// repositoryPrefix 是本仓库包的导入路径前缀。
const repositoryPrefix = "github.com/mooyang-code/moox/"

// executionBinaries 是执行面的两个程序：云函数程序与 collector-stream。
var executionBinaries = map[string]string{
	"scf_market_data": "./cmd/scf/market_data",
	"stream":          "./cmd/stream",
}

// listDependencies 返回一个程序的全部非标准库依赖（go list -deps 的顺序）。
// 固定按 linux/amd64 解析，结果不随本机平台变化；用跑测试的这套 Go 工具链，不看 PATH。
func listDependencies(t *testing.T, target string) []string {
	t.Helper()
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "list", "-deps", target)
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

// 执行面程序依赖的本仓库的包与记录一致（第三方包随依赖升级变化，不记，只查下面的禁止名单）。
// 清单变化要有意为之：确认后设 MOOX_UPDATE_GOLDEN=1 重新生成。
func TestExecutionBinaryDependenciesMatchBaseline(t *testing.T) {
	for name, target := range executionBinaries {
		var own []string
		for _, dependency := range listDependencies(t, target) {
			if strings.HasPrefix(dependency, repositoryPrefix) {
				own = append(own, dependency)
			}
		}
		got := strings.Join(own, "\n") + "\n"
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

// controlPlanePackages 是执行面程序的依赖里不得出现的包：Collector 的库与 SQLite 驱动、控制面（含调腾讯云的 cloud 包）、
// CloudFunction 与 Admin 的协议和客户端。
// 不写裸的 cloudfunction：云函数程序引的第三方包 github.com/tencentyun/scf-go-lib/cloudfunction 是合法依赖。
var controlPlanePackages = []string{
	"/modules/collector/internal/store", "/modules/collector/internal/fetchplan",
	"/modules/collector/internal/egressbalance", "/modules/collector/internal/cloud", "/modules/cloudfunction", "/modules/admin/", "gorm.io/", "sqlite",
}

// 云函数程序与 collector-stream 不带 store、SQLite 驱动、控制面、CloudFunction 与 Admin 的协议。
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
