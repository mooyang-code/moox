// Package factorlib 读取 YAML 因子库：每个 *.yaml 文件是
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

// File 是一个因子库文件：名称只用于报错定位。
type File struct {
	Name    string
	Content []byte
}

// Parse 解析一个因子库文件。字段严格校验：未知字段、缺少 factor_id/name/spec 都报错。
func Parse(name string, raw []byte) ([]Entry, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var entries []Entry
	if err := decoder.Decode(&entries); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("因子库 %s 是空文件", name)
		}
		return nil, fmt.Errorf("解析因子库 %s：%w", name, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("因子库 %s 只能包含一个 YAML 文档", name)
	}
	seen := make(map[string]bool, len(entries))
	for i := range entries {
		entry := &entries[i]
		entry.FactorID, entry.Name, entry.File = strings.TrimSpace(entry.FactorID), strings.TrimSpace(entry.Name), name
		entry.CategoryID, entry.CategoryName = strings.TrimSpace(entry.CategoryID), strings.TrimSpace(entry.CategoryName)
		if entry.FactorID == "" || entry.Name == "" || strings.TrimSpace(entry.Spec) == "" {
			return nil, fmt.Errorf("因子库 %s 第 %d 项缺少 factor_id、name 或 spec", name, i+1)
		}
		if seen[entry.FactorID] {
			return nil, fmt.Errorf("因子库 %s 里 %s 重复", name, entry.FactorID)
		}
		seen[entry.FactorID] = true
	}
	return entries, nil
}

// ParseFiles 按给定顺序解析多个因子库文件；factor_id 在全部文件内必须唯一。
func ParseFiles(files []File) ([]Entry, error) {
	if len(files) == 0 {
		return nil, errors.New("没有因子库文件")
	}
	var all []Entry
	owner := map[string]string{}
	for _, file := range files {
		entries, err := Parse(file.Name, file.Content)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if previous, duplicate := owner[entry.FactorID]; duplicate {
				return nil, fmt.Errorf("因子 %s 同时出现在 %s 与 %s", entry.FactorID, previous, file.Name)
			}
			owner[entry.FactorID] = file.Name
			all = append(all, entry)
		}
	}
	return all, nil
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
	files := make([]File, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取因子库 %s：%w", path, err)
		}
		files = append(files, File{Name: path, Content: raw})
	}
	return ParseFiles(files)
}
