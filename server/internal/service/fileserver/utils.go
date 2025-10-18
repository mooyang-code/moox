package fileserver

import (
	"strings"
)

// isValidFilePath 检查文件路径是否合法（防止路径遍历攻击）
func (s *Server) isValidFilePath(filepath string) bool {
	// 检查是否包含危险字符
	dangerousPatterns := []string{"..", "~", "//", "\\"}
	for _, pattern := range dangerousPatterns {
		if strings.Contains(filepath, pattern) {
			return false
		}
	}

	// 检查是否以合法后缀结尾
	allowedExtensions := []string{".zip", ".tar.gz", ".tar", ".jar", ".war"}
	for _, ext := range allowedExtensions {
		if strings.HasSuffix(strings.ToLower(filepath), ext) {
			return true
		}
	}
	return false
}
