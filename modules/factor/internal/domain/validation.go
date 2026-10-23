package domain

import (
	"fmt"
	"strings"
)

// NormalizeFactorDefinition 去掉首尾空白、按 Spec 原文计算 SpecHash，再做 ValidateDefinition（编译并填充派生字段）。
func NormalizeFactorDefinition(factor FactorDef) (FactorDef, error) {
	factor.FactorID = strings.TrimSpace(factor.FactorID)
	factor.Name = strings.TrimSpace(factor.Name)
	factor.Spec = strings.TrimSpace(factor.Spec)
	factor.CategoryID = strings.TrimSpace(factor.CategoryID)
	if factor.CategoryID == "" {
		factor.CategoryID = DefaultCategoryID
	}
	if factor.FactorID == "" || factor.Name == "" || factor.Spec == "" {
		return FactorDef{}, fmt.Errorf("factor_id, name and spec are required")
	}
	factor.Spec += "\n"
	factor.SpecHash = SpecHash(factor.Spec)
	factor.compiled = nil
	return ValidateDefinition(factor)
}

// MustDefinition 规范化并编译一个定义，失败时 panic；用于测试与内置样例。
func MustDefinition(factorID, name, spec string) FactorDef {
	def, err := NormalizeFactorDefinition(FactorDef{FactorID: factorID, Name: name, Spec: spec})
	if err != nil {
		panic(fmt.Sprintf("因子 %s 定义不合法：%v", factorID, err))
	}
	return def
}
