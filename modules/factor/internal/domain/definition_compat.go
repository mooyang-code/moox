package domain

import (
	"fmt"
	"slices"
)

// CheckDefinitionCompatible 检查新定义能否替换一个已被任务引用的旧定义（设计 5.4）：不允许增删参数，不允许改参数的类型、
// 声明顺序和默认值，也不允许增删、改名或重排输出。成员键依赖参数的顺序与默认值，列名依赖输出列表，这些一变，已有的列就对不上了。
// 参数的范围和表达式本身可以改；范围是否仍包含各成员保存的参数，由调用方再用 CheckMemberParams 检查。
func CheckDefinitionCompatible(old, updated FactorDef) error {
	oldCompiled, newCompiled := old.Compiled(), updated.Compiled()
	if oldCompiled == nil || newCompiled == nil {
		return fmt.Errorf("因子 %s 的定义没有编译", old.FactorID)
	}
	if len(oldCompiled.Params) != len(newCompiled.Params) {
		return fmt.Errorf("因子 %s 已被任务引用，不能增删参数；请新建一个因子定义", old.FactorID)
	}
	for i, param := range oldCompiled.Params {
		next := newCompiled.Params[i]
		switch {
		case param.Name != next.Name:
			return fmt.Errorf("因子 %s 已被任务引用，不能改参数名或调整参数顺序（第 %d 个参数 %s → %s）", old.FactorID, i+1, param.Name, next.Name)
		case param.Type != next.Type:
			return fmt.Errorf("因子 %s 已被任务引用，不能改参数 %s 的类型", old.FactorID, param.Name)
		case param.Default != next.Default:
			return fmt.Errorf("因子 %s 已被任务引用，不能改参数 %s 的默认值（成员键依赖默认值）", old.FactorID, param.Name)
		}
	}
	if !slices.Equal(old.Outputs, updated.Outputs) {
		return fmt.Errorf("因子 %s 已被任务引用，不能增删、改名或重排输出", old.FactorID)
	}
	return nil
}
