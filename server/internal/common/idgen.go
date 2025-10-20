package common

import (
	gonanoid "github.com/matoous/go-nanoid/v2"
)

// 定义字符集：小写字母和数字
const defaultAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// GenerateID 生成固定长度的随机ID
// 使用小写字母和数字组合，默认11位
func GenerateID(length int) string {
	if length <= 0 {
		length = 11 // 默认11位
	}
	id, err := gonanoid.Generate(defaultAlphabet, length)
	if err != nil {
		// 如果生成失败，使用MustGenerate（会panic，但这种情况极少发生）
		id = gonanoid.MustGenerate(defaultAlphabet, length)
	}
	return id
}
