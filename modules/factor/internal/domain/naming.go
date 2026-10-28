package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
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

// udfRef 匹配定义文本里的自定义算子调用 udf.<名称>(。
var udfRef = regexp.MustCompile(`\budf\s*\.\s*([a-z][a-z0-9_]{0,47})\s*\(`)

// UDFNames 返回定义文本里引用的自定义算子名称（去重、排序）。按文本扫描，注释里的写法也会被算进去（只会多算，
// 不会漏掉），精确的引用由编译结果给出（Compiled().UDFs）。
func UDFNames(spec string) []string {
	if !strings.Contains(spec, "udf") {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, match := range udfRef.FindAllStringSubmatch(spec, -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			names = append(names, match[1])
		}
	}
	sort.Strings(names)
	return names
}

// UDFKey 是自定义算子绑定的指纹：名称 → 版本的集合。绑定相同的已编译定义可以复用编译结果，绑定变了就要重新编译。
func UDFKey(udfs map[string]factorexpr.UDF) string {
	if len(udfs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(udfs))
	for name, udf := range udfs {
		parts = append(parts, name+"@"+udf.Hash)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// CompileDefinition 编译 Spec 并填充派生字段（类型、输入列、输出列、回看）；已用同样的自定义算子绑定编译过的直接返回。
func CompileDefinition(def FactorDef) (FactorDef, error) {
	key := UDFKey(def.UDFs)
	if def.compiled != nil && def.compiledKey == key {
		return def, nil
	}
	def.compiled, def.compiledKey = nil, ""
	var env factorexpr.UDFEnv
	if len(def.UDFs) > 0 {
		env = factorexpr.UDFs(def.UDFs)
	}
	compiled, err := factorexpr.CompileWith(def.Spec, env)
	if err != nil {
		return def, err
	}
	// 没有启用版本的名称只是解析环境里的占位（文本扫描会把注释里的 udf.<名称>( 也算进来），不带到引擎去。
	// 有版本的绑定一律保留：引擎拿原文重新编译，常量折叠掉的分支里的调用也要能解析（只看最终输出树的引用集合会漏掉它们）。
	if len(def.UDFs) > 0 {
		bound := make(map[string]factorexpr.UDF, len(def.UDFs))
		for name, udf := range def.UDFs {
			if udf.Hash != "" {
				bound[name] = udf
			}
		}
		def.UDFs = bound
		key = UDFKey(bound)
	}
	def.compiled, def.compiledKey = compiled, key
	def.FactorType = string(compiled.Type)
	def.InputColumns = append([]string(nil), compiled.InputColumns...)
	def.Outputs = compiled.OutputNamesFor(def.FactorID)
	def.LookbackPeriods = compiled.Lookback
	return def, nil
}

// CompiledCurrent 报告定义是否已用当前的自定义算子绑定编译过：复制定义后替换了绑定，编译结果就不再适用。
func (d FactorDef) CompiledCurrent() bool {
	return d.compiled != nil && d.compiledKey == UDFKey(d.UDFs)
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
		// 备用指纹也包含自定义算子的绑定：不同版本编译失败时身份不同。
		parts := []string{"definition-uncompiled-v2", factorexpr.ExprVersion, def.Spec, strconv.FormatBool(def.AllowPartialUniverse)}
		if key := UDFKey(def.UDFs); key != "" {
			parts = append(parts, key)
		}
		hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
		return "sha256:" + hex.EncodeToString(hash[:])
	}
	parts := []string{"definition-v2", factorexpr.ExprVersion, compiled.compiled.CanonicalText(), strconv.FormatBool(def.AllowPartialUniverse)}
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(hash[:])
}
