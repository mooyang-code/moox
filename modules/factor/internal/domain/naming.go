package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// TaskID derives the FactorTask ID of a set without a writer from its source and frequency.
func TaskID(sourceDatasetID, freq string) string {
	return "ftask_" + sourceSuffix(sourceDatasetID, freq)
}

// InstanceTaskID derives the private FactorTask ID of a strategy instance. 实例 ID 是 UUID，不满足 Storage 的小写
// 下划线标识规则，也太长，所以取它的摘要：结果数据集 ID 不超过 Storage 的 50 个字符上限。
func InstanceTaskID(instanceID string) string {
	return "ftask_" + instanceSuffix(instanceID)
}

// ResultDatasetID derives the result Dataset ID from a FactorTask ID.
func ResultDatasetID(taskID string) string {
	return "dataset_factor_" + strings.TrimPrefix(strings.TrimSpace(taskID), "ftask_")
}

// ResultViewID 是结果数据集的默认 View（Storage 激活结果数据集时按 view_ + 数据集后缀创建）。
func ResultViewID(taskID string) string {
	return "view_factor_" + strings.TrimPrefix(strings.TrimSpace(taskID), "ftask_")
}

func instanceSuffix(instanceID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(instanceID)))
	return "i" + hex.EncodeToString(sum[:])[:20]
}

// InstanceDatasetName 是私有结果数据集的展示名（Storage 限 10 个字符、空间内唯一）。
func InstanceDatasetName(instanceID string) string {
	return "策略因子" + instanceSuffix(instanceID)[1:7]
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
	def.Outputs = compiled.OutputNames()
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

func sourceSuffix(sourceDatasetID, freq string) string {
	suffix := strings.TrimPrefix(strings.TrimSpace(sourceDatasetID), "dataset_")
	freq = strings.TrimSpace(freq)
	if freq != "" && !strings.HasSuffix(suffix, "_"+freq) {
		suffix += "_" + freq
	}
	return suffix
}
