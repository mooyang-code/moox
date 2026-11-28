package input

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/upstream"
)

// VarMember 是关联任务里的一个可变成员：成员键、引用的因子定义和定义声明的可调参数。
type VarMember struct {
	Key      string
	FactorID string
	Params   []upstream.ParamDecl
}

// CheckFactorParams 对照任务的可变成员检查 DSL（设计 8.1、8.2）：factor_params 与 params() 引用的成员必须是任务的可变成员，
// 参数名必须是该因子声明的参数。声明了 factor_params 的策略会关联任务、逐期提交参数，所以任务的每个可变成员都要在 factor_params
// 里有输出（可变成员与 factor_params 一一对应）；没有声明的策略不关联任务，只读可变成员的列，参数由关联它的实例提交（没有关联时用默认参数）。
func CheckFactorParams(strategy dsl.Strategy, members map[string]VarMember, refs []dsl.ParamRef) error {
	available := func() string {
		keys := make([]string, 0, len(members))
		for key := range members {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			return "该任务没有可变成员"
		}
		return "任务的可变成员：" + strings.Join(keys, "、")
	}
	declared := func(member VarMember, name string) bool {
		for _, decl := range member.Params {
			if decl.Name == name {
				return true
			}
		}
		return false
	}
	check := func(where, key, name string) error {
		member, ok := members[key]
		if !ok {
			return fmt.Errorf("%s：成员 %s 不是所选任务的可变成员（%s）", where, key, available())
		}
		if !declared(member, name) {
			names := make([]string, 0, len(member.Params))
			for _, decl := range member.Params {
				names = append(names, decl.Name)
			}
			return fmt.Errorf("%s：因子 %s 没有参数 %s（可调参数：%s）", where, member.FactorID, name, strings.Join(names, "、"))
		}
		return nil
	}
	written := make(map[string]struct{})
	for _, output := range strategy.FactorParamOutputs() {
		if err := check("factor_params."+output.Member+"."+output.Name, output.Member, output.Name); err != nil {
			return err
		}
		written[output.Member] = struct{}{}
	}
	for _, ref := range refs {
		if err := check(fmt.Sprintf(`params("%s", "%s")`, ref.Member, ref.Name), ref.Member, ref.Name); err != nil {
			return err
		}
	}
	if len(strategy.FactorParams) == 0 {
		return nil
	}
	for _, key := range sortedKeys(members) {
		if _, ok := written[key]; !ok {
			return fmt.Errorf("关联任务的可变成员 %s 在 factor_params 里没有输出：可变成员的参数由策略逐期提交，没有输出就永远用默认参数", key)
		}
	}
	return nil
}
