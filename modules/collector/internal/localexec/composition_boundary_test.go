package localexec

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本地执行端在 collector-stream 里跑，属于执行面：不得引入控制面、Collector 的库与 SQLite 驱动、
// CloudFunction 的协议与客户端。规则与 marketfetch 的同名测试相同。
func TestRuntimeDoesNotImportControlPlane(t *testing.T) {
	forbiddenImports := []string{
		"/internal/store", "/internal/fetchplan", "/internal/scfinvoker", "/internal/egressbalance",
		"cloudfunctiongen", "gorm.io/", "modernc.org/sqlite",
	}
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
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
			for _, forbidden := range forbiddenImports {
				if strings.Contains(path, forbidden) {
					t.Errorf("%s 引入了执行面不该依赖的 %s", name, path)
				}
			}
		}
	}
}
