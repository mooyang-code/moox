package depcheck

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testOnlyPackages 是只给测试用的包：非测试代码不得引用（假腾讯云接进生产代码就再也不会调真实的腾讯云）。
var testOnlyPackages = []string{repositoryPrefix + "modules/collector/internal/cloud/cloudtest"}

// Collector 的非测试代码不引用只给测试用的包。
func TestProductionCodeDoesNotImportTestOnlyPackages(t *testing.T) {
	root := filepath.Join("..", "..")
	checked := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); name == "testdata" || name == "node_modules" || (strings.HasPrefix(name, ".") && path != root) {
				return filepath.SkipDir
			}
			// 只给测试用的包自己不算。
			if filepath.ToSlash(path) == filepath.ToSlash(filepath.Join(root, "internal", "cloud", "cloudtest")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		checked++
		for _, spec := range file.Imports {
			imported, _ := strconv.Unquote(spec.Path.Value)
			for _, forbidden := range testOnlyPackages {
				if imported == forbidden {
					t.Errorf("%s 引用了只给测试用的包 %s", path, forbidden)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 100 {
		t.Fatalf("只扫到 %d 个非测试文件，扫描范围可能不对", checked)
	}
}
