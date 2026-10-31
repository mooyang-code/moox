package failure

import (
	"fmt"
	"strings"
	"testing"
)

func TestLineFindsWrappedCode(t *testing.T) {
	line, ok := Line(fmt.Errorf("bootstrap: %w", New(TargetIdentityDiffers, "x")))
	if !ok || line != Prefix+TargetIdentityDiffers {
		t.Fatalf("错误码行不对: %q %v", line, ok)
	}
	if _, ok := Line(fmt.Errorf("plain")); ok {
		t.Fatal("普通错误不应有错误码")
	}
}

func TestDescribeOnlyHintsRetryWhenConnectionLost(t *testing.T) {
	if err := Describe("bootstrap observation failed; retry", 0, ""); err.Error() != "bootstrap observation failed; retry" {
		t.Fatalf("连接断开应提示重试: %v", err)
	}
	err := Describe("bootstrap observation failed; retry", 1, "secret-output\n"+Prefix+TargetSubsetInstalled+"\n")
	if !strings.HasPrefix(err.Error(), TargetSubsetInstalled) || strings.Contains(err.Error(), "retry") || strings.Contains(err.Error(), "secret-output") {
		t.Fatalf("远端拒绝的说明不对: %v", err)
	}
}

func TestDescribeHidesUnknownOutput(t *testing.T) {
	err := Describe("retry", 1, "secret-output\n"+Prefix+"unknown_code\n")
	if strings.Contains(err.Error(), "secret-output") || strings.Contains(err.Error(), "unknown_code") || !strings.HasPrefix(err.Error(), "target_rejected") {
		t.Fatalf("未知输出不应显示: %v", err)
	}
}
