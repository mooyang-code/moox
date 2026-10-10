// moox-strategy-cli 是策略模块的命令行工具：在本地校验策略 DSL。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

const usage = "用法：moox-strategy-cli validate <dsl.yaml>"

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runCLI 解析并校验 DSL，输出名称、内容哈希、规则与引用的列。
func runCLI(args []string, out, errOut io.Writer) error {
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
