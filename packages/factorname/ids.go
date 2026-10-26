package factorname

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
)

const (
	taskPrefix    = "ftask_"
	datasetPrefix = "dataset_factor_"
	viewPrefix    = "view_factor_"
	suffixAlpha   = "abcdefghijklmnopqrstuvwxyz0123456789"
	generatedLen  = 8
)

var suffixPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9_]*[a-z0-9])?$`)

// ValidateSuffix 校验任务 ID 后缀：小写字母、数字和下划线，首尾不能是下划线，最长 MaxSuffixLen。
func ValidateSuffix(suffix string) error {
	if len(suffix) == 0 || len(suffix) > MaxSuffixLen || !suffixPattern.MatchString(suffix) {
		return fmt.Errorf("任务 ID 后缀 %q 不合法：只能是小写字母、数字和下划线，首尾不能是下划线，最长 %d 个字符", suffix, MaxSuffixLen)
	}
	return nil
}

// NewSuffix 生成 8 位随机后缀（小写字母和数字）。
func NewSuffix() (string, error) {
	raw := make([]byte, generatedLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成任务 ID 后缀：%w", err)
	}
	out := make([]byte, generatedLen)
	for i, b := range raw {
		out[i] = suffixAlpha[int(b)%len(suffixAlpha)]
	}
	return string(out), nil
}

// TaskID、ResultDatasetID、ResultViewID 由同一个后缀生成因子任务、结果数据集、默认 View 的 ID。
func TaskID(suffix string) (string, error)          { return withPrefix(taskPrefix, suffix) }
func ResultDatasetID(suffix string) (string, error) { return withPrefix(datasetPrefix, suffix) }
func ResultViewID(suffix string) (string, error)    { return withPrefix(viewPrefix, suffix) }

// ParseTaskID、ParseResultDatasetID、ParseResultViewID 是上面三个函数的反解，返回后缀。
func ParseTaskID(id string) (string, error)          { return trimPrefix(taskPrefix, id) }
func ParseResultDatasetID(id string) (string, error) { return trimPrefix(datasetPrefix, id) }
func ParseResultViewID(id string) (string, error)    { return trimPrefix(viewPrefix, id) }

func withPrefix(prefix, suffix string) (string, error) {
	if err := ValidateSuffix(suffix); err != nil {
		return "", err
	}
	return prefix + suffix, nil
}

func trimPrefix(prefix, id string) (string, error) {
	if !strings.HasPrefix(id, prefix) {
		return "", fmt.Errorf("ID %q 不是以 %s 开头", id, prefix)
	}
	suffix := strings.TrimPrefix(id, prefix)
	if err := ValidateSuffix(suffix); err != nil {
		return "", err
	}
	return suffix, nil
}
