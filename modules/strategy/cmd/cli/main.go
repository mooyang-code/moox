// moox-strategy-cli 是策略模块的命令行工具：在本地校验策略 DSL，在本地校验并渲染策略模板。
// 模板的保存、列表与删除走 StrategyMgr 接口（moox-cli gateway call），见 docs/模块/策略.md。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/template"
)

const usage = `用法：
  moox-strategy-cli validate <dsl.yaml>
  moox-strategy-cli template check <template.yaml>
  moox-strategy-cli template render <template.yaml> --name <策略名> [--var 名=值 ...] [--params <参数表.csv>]`

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runCLI 解析并校验 DSL，输出名称、内容哈希、规则与引用的列。
func runCLI(args []string, out, errOut io.Writer) error {
	if len(args) > 0 && args[0] == "template" {
		return runTemplate(args[1:], out, errOut)
	}
	if len(args) == 0 || args[0] != "validate" {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(errOut)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(usage)
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("读取 DSL 文件 %s 失败：%w", fs.Arg(0), err)
	}
	strategy, err := dsl.Parse(raw)
	if err != nil {
		return err
	}
	columns, err := dsl.ReferencedColumns(strategy)
	if err != nil {
		return err
	}
	rules := make([]string, 0, len(strategy.Rules))
	for _, rule := range strategy.Rules {
		rules = append(rules, rule.ID+"("+rule.Type+")")
	}
	_, _ = fmt.Fprintf(out, "策略 DSL 有效：name=%s dsl_hash=%s\n规则：%s\n引用列：%s\n", strategy.Name, dsl.Hash(raw), strings.Join(rules, "、"), strings.Join(columns, "、"))
	return nil
}

// varFlags 收集可重复的 --var 名=值。
type varFlags map[string]string

func (v varFlags) String() string { return "" }

func (v varFlags) Set(text string) error {
	name, value, ok := strings.Cut(text, "=")
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("--var 要写成 名=值：%q", text)
	}
	v[strings.TrimSpace(name)] = value
	return nil
}

// runTemplate 在本地校验模板（check），或用变量与参数表渲染出策略 DSL 并输出到标准输出（render）。
func runTemplate(args []string, out, errOut io.Writer) error {
	if len(args) == 0 || (args[0] != "check" && args[0] != "render") {
		return errors.New(usage)
	}
	command := args[0]
	fs := flag.NewFlagSet("template "+command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	name := fs.String("name", "", "生成的策略名（render 必填）")
	paramsPath := fs.String("params", "", "参数表 CSV，第一行是表头（模板带参数表时必填）")
	vars := varFlags{}
	fs.Var(vars, "var", "模板变量，名=值，可重复")
	if len(args) < 2 {
		return errors.New(usage)
	}
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	raw, err := os.ReadFile(args[1])
	if err != nil {
		return fmt.Errorf("读取模板文件 %s 失败：%w", args[1], err)
	}
	parsed, err := template.Parse(raw)
	if err != nil {
		return err
	}
	if command == "check" {
		names := make([]string, 0, len(parsed.Variables))
		for _, variable := range parsed.Variables {
			names = append(names, variable.Name)
		}
		_, _ = fmt.Fprintf(out, "模板有效：template_id=%s name=%s\n变量：%s\n参数表列：%s\n", parsed.ID, parsed.Name, strings.Join(names, "、"), strings.Join(parsed.Columns(), "、"))
		return nil
	}
	if *name == "" {
		return errors.New("render 需要 --name")
	}
	vars[template.NameVariable] = *name
	var rows [][]string
	if *paramsPath != "" {
		text, err := os.ReadFile(*paramsPath)
		if err != nil {
			return fmt.Errorf("读取参数表 %s 失败：%w", *paramsPath, err)
		}
		if rows, err = parsed.ParseRows(string(text)); err != nil {
			return err
		}
	}
	text, err := parsed.Render(vars, rows)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprint(out, text)
	return nil
}
