package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// SetID derives the canonical FactorSet ID from its source and frequency.
func SetID(sourceDatasetID, freq string) string {
	return "fset_" + sourceSuffix(sourceDatasetID, freq)
}

// ResultDatasetID derives the canonical result Dataset ID for a FactorSet.
func ResultDatasetID(sourceDatasetID, freq string) string {
	return "dataset_factor_" + sourceSuffix(sourceDatasetID, freq)
}

// SourceHash returns the content identity used for immutable factor source files.
func SourceHash(sourceCode string) string {
	hash := sha256.Sum256([]byte(sourceCode))
	return "sha256:" + hex.EncodeToString(hash[:])
}

// DefinitionHash 是因子完整计算契约的指纹：源码 hash、因子类型、规范化后的参数、回看周期、
// 输入输出列与 allow_partial_universe。它是持久化字段的派生值，读取时计算、不落库；
// 只改参数而不改源码时 SourceHash 不变、DefinitionHash 变化，策略据此判断绑定的版本。
func DefinitionHash(factor FactorDef) string {
	sourceHash := strings.TrimSpace(factor.SourceHash)
	if sourceHash == "" {
		sourceHash = SourceHash(strings.TrimSpace(factor.SourceCode))
	}
	params, err := normalizeParamsJSON(factor.ParamsJSON)
	if err != nil {
		params = strings.TrimSpace(factor.ParamsJSON)
	}
	parts := []string{
		"definition",
		sourceHash,
		strings.TrimSpace(factor.FactorType),
		params,
		strconv.Itoa(factor.LookbackPeriods),
		strings.Join(trimmedStrings(factor.InputColumns), ","),
		strings.Join(trimmedStrings(factor.Outputs), ","),
		strconv.FormatBool(factor.AllowPartialUniverse),
	}
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func trimmedStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.TrimSpace(value))
	}
	return out
}

func sourceSuffix(sourceDatasetID, freq string) string {
	suffix := strings.TrimPrefix(strings.TrimSpace(sourceDatasetID), "dataset_")
	freq = strings.TrimSpace(freq)
	if freq != "" && !strings.HasSuffix(suffix, "_"+freq) {
		suffix += "_" + freq
	}
	return suffix
}
