package servicehost

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

// 参数校验与平台限制：非 Windows 上 service 子命令报错并给出替代方式；Run 直接运行 serve。
func TestCommandArgumentsAndPlatform(t *testing.T) {
	spec := Spec{Name: "moox-test"}
	for _, args := range [][]string{nil, {"install", "extra"}} {
		if err := Command(spec, args); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Fatalf("args %v: err = %v", args, err)
		}
	}
	if err := Command(spec, []string{"restart"}); err == nil || !strings.Contains(err.Error(), "unknown service command") {
		t.Fatalf("unknown command: err = %v", err)
	}
	if err := Command(Spec{}, []string{"install"}); err == nil {
		t.Fatal("empty service name accepted")
	}
	if runtime.GOOS != "windows" {
		if err := Command(spec, []string{"install"}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("install on %s: err = %v", runtime.GOOS, err)
		}
	}
	called := false
	if err := Run("moox-test", func(context.Context) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("Run outside the service manager: called=%v err=%v", called, err)
	}
}
