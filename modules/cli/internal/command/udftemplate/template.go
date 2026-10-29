// Package udftemplate 是 moox-cli udf new 生成的 Rust 工作区骨架（设计 6.1）：rust-toolchain.toml 锁定版本与 wasm 目标，
// clippy.toml 是禁用清单，workspace 的 Cargo.toml 带 release 配置，AGENTS.md 是给 Agent 的编写规范，
// 一个 crate 的样板带 fixtures。模板用 [[ ]] 作分隔符，免得与 Rust 的花括号冲突。
package udftemplate

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"text/template"
)

//go:embed all:files
var files embed.FS

// Toolchain 是模板锁定的 Rust 版本，与 sdk/rust/rust-toolchain.toml 保持一致。
const Toolchain = "1.99.1"

// Data 是模板变量。
type Data struct {
	// Name 是 UDF 名称，也是 crate 名。
	Name string
	// ParamsType 是 #[udf] 宏按函数名生成的参数结构体名（CamelCase + Params）。
	ParamsType string
	Toolchain  string
	// SDKDep、SDKDevDep 是 moox-udf 依赖的右半部分（TOML 内联表）：git 依赖（仓库 + 标签）或本地 path 依赖。
	SDKDep    string
	SDKDevDep string
}

// File 是要写出的一个文件。
type File struct {
	Path string // 相对工作区根
	Data []byte
}

// Workspace 渲染工作区级的文件（首次在空目录里创建）。
func Workspace(data Data) ([]File, error) { return render("files/workspace", "", data) }

// Crate 渲染一个 UDF crate 的文件（路径在 udfs/<名称>/ 下）。
func Crate(data Data) ([]File, error) {
	return render("files/crate", path.Join("udfs", data.Name), data)
}

// 隐藏文件在 embed 里用普通名字存放，写出时改回点开头。
var dotNames = map[string]string{"gitignore": ".gitignore"}

func render(root, prefix string, data Data) ([]File, error) {
	var out []File
	err := fs.WalkDir(files, root, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(name, root+"/")
		// 只有 .tmpl 文件按模板渲染，其他文件原样写出（fixtures 的 JSON 里有 [[ ]]，不能当模板处理）。
		if !strings.HasSuffix(rel, ".tmpl") {
			out = append(out, File{Path: path.Join(prefix, rel), Data: raw})
			return nil
		}
		rel = strings.TrimSuffix(rel, ".tmpl")
		if dot, ok := dotNames[path.Base(rel)]; ok {
			rel = path.Join(path.Dir(rel), dot)
		}
		tpl, err := template.New(rel).Delims("[[", "]]").Option("missingkey=error").Parse(string(raw))
		if err != nil {
			return fmt.Errorf("模板 %s：%w", rel, err)
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("渲染 %s：%w", rel, err)
		}
		out = append(out, File{Path: path.Join(prefix, rel), Data: buf.Bytes()})
		return nil
	})
	return out, err
}
