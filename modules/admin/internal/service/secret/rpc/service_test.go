package rpc

import "testing"

func TestMaskSecretValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"空字符串", "", ""},
		{"1字符", "a", "•"},
		{"4字符", "abcd", "••••"},
		{"5字符", "abcde", "ab•de"},
		{"8字符", "abcdefgh", "ab••••gh"},
		{"9字符", "abcdefghi", "abcd•fghi"},
		{"16字符", "0123456789012345", "0123••••••••2345"},
		{"34字符", "AKIDabcdefghijklmnopqrstuvwxyz0123", "AKID••••••••••••••••••••••••••0123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskSecretValue(tt.input)
			if got != tt.want {
				t.Errorf("maskSecretValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestMaskSecretValuePreservesEndpoints(t *testing.T) {
	// 长秘钥保留前4后4
	input := "AKIDxxxxxxxxxxxxxxxxxxxx8j9k"
	result := maskSecretValue(input)
	if result[:4] != "AKID" {
		t.Errorf("prefix not preserved: got %s", result[:4])
	}
	if result[len(result)-4:] != "8j9k" {
		t.Errorf("suffix not preserved: got %s", result[len(result)-4:])
	}
	if result[4:len(result)-4] == "xxxxxxxxxxxxxxxxxxxx" {
		t.Errorf("middle not masked")
	}
}

func TestIsMaskedSecret(t *testing.T) {
	// 真实秘钥含 * 不应被误判
	if isMaskedSecret("AKIDabc*def123") {
		t.Error("real secret with * should not be masked")
	}
	// 脱敏串含 • 应被识别
	if !isMaskedSecret("AKID••••••••1234") {
		t.Error("masked value with • should be detected")
	}
	// 纯明文不误判
	if isMaskedSecret("AKIDabcdefghijklmnopqrstuvwxyz") {
		t.Error("plain value should not be masked")
	}
}
