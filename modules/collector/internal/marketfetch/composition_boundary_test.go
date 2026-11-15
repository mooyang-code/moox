package marketfetch

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// controlPlaneImports 是执行面（云函数程序、collector-stream 里跑的代码）不得引入的包：
// 控制面、Collector 的库与 SQLite 驱动、CloudFunction 的协议与客户端。
var controlPlaneImports = []string{
	"/internal/store", "/internal/fetchplan", "/internal/cloud", "/internal/egressbalance",
	"cloudfunctiongen", "gorm.io/", "modernc.org/sqlite",
}

// runtimeImports 返回本包非测试文件的全部导入（文件名 → 导入路径）。
func runtimeImports(t *testing.T) map[string][]string {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string][]string{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range f.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			imports[name] = append(imports[name], path)
		}
	}
	return imports
}

func TestRuntimeDoesNotImportProviderImplementations(t *testing.T) {
	for name, paths := range runtimeImports(t) {
		for _, path := range paths {
			if strings.Contains(path, "/internal/sources/") && !strings.HasSuffix(path, "/sources/stockcn") {
				t.Errorf("%s imports concrete source %s", name, path)
			}
		}
	}
}

// 执行面不引入控制面：依赖方向只能是 fetchplan → marketfetch。
func TestRuntimeDoesNotImportControlPlane(t *testing.T) {
	for name, paths := range runtimeImports(t) {
		for _, path := range paths {
			for _, forbidden := range controlPlaneImports {
				if strings.Contains(path, forbidden) {
					t.Errorf("%s 引入了执行面不该依赖的 %s", name, path)
				}
			}
		}
	}
}
