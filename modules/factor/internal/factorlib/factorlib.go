// Package factorlib 读取 YAML 因子库（docs/计划/因子表达式引擎详细设计.md 9.5）：每个 *.yaml 文件是
// [{factor_id, name, category_id, category_name, spec}] 列表，一个文件可以包含多个因子。
package factorlib

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Entry 是因子库里的一个定义。
type Entry struct {
	FactorID string `yaml:"factor_id"`
	Name     string `yaml:"name"`
	// CategoryID 是因子分类（可选，默认未分类）；CategoryName 非空时导入会建立或更新这个分类。
	CategoryID   string `yaml:"category_id"`
	CategoryName string `yaml:"category_name"`
	Spec         string `yaml:"spec"`
	// AllowPartialUniverse 只对截面因子有意义，默认 false。
	AllowPartialUniverse bool `yaml:"allow_partial_universe"`
	// File 是定义所在的文件，不来自 YAML。
	File string `yaml:"-"`
}

// LoadFile 读取一个因子库文件。字段严格校验：未知字段、缺少 factor_id/name/spec 都报错。
func LoadFile(path string) ([]Entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取因子库 %s：%w", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var entries []Entry
	if err := decoder.Decode(&entries); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("因子库 %s 是空文件", path)
		}
		return nil, fmt.Errorf("解析因子库 %s：%w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("因子库 %s 只能包含一个 YAML 文档", path)
	}
	seen := make(map[string]bool, len(entries))
	for i := range entries {
		entry := &entries[i]
		entry.FactorID, entry.Name, entry.File = strings.TrimSpace(entry.FactorID), strings.TrimSpace(entry.Name), path
		entry.CategoryID, entry.CategoryName = strings.TrimSpace(entry.CategoryID), strings.TrimSpace(entry.CategoryName)
		if entry.FactorID == "" || entry.Name == "" || strings.TrimSpace(entry.Spec) == "" {
			return nil, fmt.Errorf("因子库 %s 第 %d 项缺少 factor_id、name 或 spec", path, i+1)
		}
		if seen[entry.FactorID] {
			return nil, fmt.Errorf("因子库 %s 里 %s 重复", path, entry.FactorID)
		}
		seen[entry.FactorID] = true
	}
	return entries, nil
}

// LoadDir 读取目录下全部 *.yaml，按文件名排序；factor_id 在整个目录内必须唯一。
func LoadDir(dir string) ([]Entry, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("因子库目录 %s 里没有 *.yaml", dir)
	}
	sort.Strings(paths)
	var all []Entry
	owner := map[string]string{}
	for _, path := range paths {
		entries, err := LoadFile(path)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if previous, duplicate := owner[entry.FactorID]; duplicate {
				return nil, fmt.Errorf("因子 %s 同时出现在 %s 与 %s", entry.FactorID, previous, path)
			}
			owner[entry.FactorID] = path
			all = append(all, entry)
		}
	}
	return all, nil
}
