package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorname"
)

// ResultDatasetID 由任务 ID 推出结果数据集 ID；任务 ID 不合法时返回空串（创建任务时已校验）。
func ResultDatasetID(taskID string) string {
	suffix, err := factorname.ParseTaskID(strings.TrimSpace(taskID))
	if err != nil {
		return ""
	}
	id, _ := factorname.ResultDatasetID(suffix)
	return id
}

// SpecHash returns the content identity of a definition text.
func SpecHash(spec string) string {
	hash := sha256.Sum256([]byte(spec))
	return "sha256:" + hex.EncodeToString(hash[:])
}

// CompileDefinition 编译 Spec 并填充派生字段（类型、输入列、输出列、回看）；已编译过的直接返回。
func CompileDefinition(def FactorDef) (FactorDef, error) {
	if def.compiled != nil {
		return def, nil
	}
	compiled, err := factorexpr.Compile(def.Spec)
	if err != nil {
		return def, err
	}
	def.compiled = compiled
	def.FactorType = string(compiled.Type)
	def.InputColumns = append([]string(nil), compiled.InputColumns...)
	def.Outputs = compiled.OutputNamesFor(def.FactorID)
	def.LookbackPeriods = compiled.Lookback
	return def, nil
}

// Compiled 返回编译结果；没有编译过或编译失败时为 nil。
func (d FactorDef) Compiled() *factorexpr.Factor { return d.compiled }

// DefinitionHash 是因子完整计算契约的指纹（设计 9.3）：expr 语义版本、规范程序文本与 allow_partial_universe。
// 只改注释、格式、let 名不会改变它；任何影响结果的改动都会改变它。它在读取时派生、不落库。
// Spec 编译失败（只在管理端与引擎的 expr 版本不一致时出现）时按原文派生一个稳定、非空的指纹：周期标记与跳过账本
// 要求指纹非空，且原文不变时指纹不变；它与任何可编译定义的指纹都不同。没有 Spec 时返回空串。
func DefinitionHash(def FactorDef) string {
	compiled, err := CompileDefinition(def)
	if err != nil {
		if def.Spec == "" {
			return ""
		}
		parts := []string{"definition-uncompiled-v2", factorexpr.ExprVersion, def.Spec, strconv.FormatBool(def.AllowPartialUniverse)}
		hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
		return "sha256:" + hex.EncodeToString(hash[:])
	}
	parts := []string{"definition-v2", factorexpr.ExprVersion, compiled.compiled.CanonicalText(), strconv.FormatBool(def.AllowPartialUniverse)}
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(hash[:])
}
