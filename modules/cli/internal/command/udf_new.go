package command

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/command/udftemplate"
)

// udfNameRE 与 packages/factorudf 的名称规则一致：^[a-z][a-z0-9_]{0,47}$。
var udfNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// rustKeywords 是不能当函数名与 crate 名用的 Rust 关键字：模板把 UDF 名直接用作函数名。
var rustKeywords = map[string]bool{
	"as": true, "async": true, "await": true, "break": true, "const": true, "continue": true, "crate": true, "dyn": true, "else": true,
	"enum": true, "extern": true, "false": true, "fn": true, "for": true, "if": true, "impl": true, "in": true, "let": true, "loop": true,
	"match": true, "mod": true, "move": true, "mut": true, "pub": true, "ref": true, "return": true, "self": true, "static": true,
	"struct": true, "super": true, "trait": true, "true": true, "type": true, "unsafe": true, "use": true, "where": true, "while": true,
	"abstract": true, "become": true, "box": true, "do": true, "final": true, "gen": true, "macro": true, "override": true, "priv": true,
	"try": true, "typeof": true, "unsized": true, "virtual": true, "yield": true,
}

// SDK 作为 git 依赖：仓库地址 + 标签（udf-sdk-v<版本>）。SDK 发布时打这个标签；本地开发用 --sdk-path 指向 sdk/rust。
const (
	udfSDKRepo = "https://github.com/mooyang-code/moox"
	udfSDKTag  = "udf-sdk-v0.1.0"
)

type udfNewOptions struct {
	Dir     string
	Name    string
	SDKPath string
	// NoLock 为真时不执行 cargo generate-lockfile（测试用）。
	NoLock bool
}

// camelCase 把 snake_case 名称转成 #[udf] 宏生成的参数结构体名（<驼峰>Params）。
func udfParamsType(name string) string {
	var b strings.Builder
	for _, part := range strings.Split(name, "_") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String() + "Params"
}

// runUdfNew 生成骨架：目录里还没有工作区（没有 Cargo.toml）就生成整个工作区，已有就只添加 crate。
func runUdfNew(out io.Writer, opts udfNewOptions) error {
	if !udfNameRE.MatchString(opts.Name) {
		return fmt.Errorf("名称 %q 不合法：要匹配 %s（小写字母开头，只含小写字母、数字、下划线，最长 48）", opts.Name, udfNameRE)
	}
	if rustKeywords[opts.Name] {
		return fmt.Errorf("名称 %q 是 Rust 关键字，不能用作 UDF 名（模板把它直接用作函数名）：换一个", opts.Name)
	}
	root, err := filepath.Abs(opts.Dir)
	if err != nil {
		return err
	}
	data := udftemplate.Data{Name: opts.Name, ParamsType: udfParamsType(opts.Name), Toolchain: udftemplate.Toolchain}
	if opts.SDKPath != "" {
		abs, err := filepath.Abs(opts.SDKPath)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(abs, "moox-udf", "Cargo.toml")); err != nil {
			return fmt.Errorf("--sdk-path %s 下没有 moox-udf/Cargo.toml", abs)
		}
		data.SDKDep = fmt.Sprintf(`{ path = %q }`, filepath.Join(abs, "moox-udf"))
		data.SDKDevDep = fmt.Sprintf(`{ path = %q, features = ["testing"] }`, filepath.Join(abs, "moox-udf"))
	} else {
		data.SDKDep = fmt.Sprintf(`{ git = %q, tag = %q }`, udfSDKRepo, udfSDKTag)
		data.SDKDevDep = fmt.Sprintf(`{ git = %q, tag = %q, features = ["testing"] }`, udfSDKRepo, udfSDKTag)
	}
	if _, err := os.Stat(filepath.Join(root, "udfs", opts.Name)); err == nil {
		return fmt.Errorf("udfs/%s 已经存在", opts.Name)
	}
	_, workspaceErr := os.Stat(filepath.Join(root, "Cargo.toml"))
	newWorkspace := os.IsNotExist(workspaceErr)
	var files []udftemplate.File
	if newWorkspace {
		if entries, err := os.ReadDir(root); err == nil && len(entries) > 0 {
			return fmt.Errorf("%s 不是空目录，也不是已有的 UDF 工作区（没有 Cargo.toml）", root)
		}
		if files, err = udftemplate.Workspace(data); err != nil {
			return err
		}
	}
	crate, err := udftemplate.Crate(data)
	if err != nil {
		return err
	}
	files = append(files, crate...)
	for _, f := range files {
		target := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, f.Data, 0o644); err != nil {
			return err
		}
	}
	kind := "添加 crate"
	if newWorkspace {
		kind = "生成工作区与 crate"
	}
	fmt.Fprintf(out, "%s：%s（udfs/%s）\n", kind, root, opts.Name)
	if !opts.NoLock {
		// 生成 Cargo.lock（含锁定版本的 SDK）：之后的 build 全部带 --locked。
		if err := runCargo(root, out, "generate-lockfile"); err != nil {
			fmt.Fprintf(out, "警告：cargo generate-lockfile 失败（%v）；联网后在 %s 里手工执行，并把 Cargo.lock 提交\n", err, root)
		}
	}
	fmt.Fprintf(out, "下一步：编辑 udfs/%s/src/lib.rs、udf.toml 与 tests/fixtures，然后 moox-cli udf build %s\n", opts.Name, opts.Name)
	if newWorkspace {
		fmt.Fprintln(out, "提示：先 git init 并提交；Cargo.lock 已生成，要一并提交（build 使用 --locked）。")
	}
	return nil
}
