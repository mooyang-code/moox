package utils

import (
	"fmt"
	"regexp"
)

// ValidateStringFormat 验证字符串格式：长度 1-20，仅支持大小写字母和数字。
func ValidateStringFormat(value, fieldName string) error {
	if len(value) < 1 || len(value) > 20 {
		return fmt.Errorf("%s长度必须在1-20个字符之间", fieldName)
	}

	matched, err := regexp.MatchString("^[a-zA-Z0-9]+$", value)
	if err != nil {
		return fmt.Errorf("验证%s格式时发生错误", fieldName)
	}
	if !matched {
		return fmt.Errorf("%s只能包含大小写字母和数字", fieldName)
	}
	return nil
}
