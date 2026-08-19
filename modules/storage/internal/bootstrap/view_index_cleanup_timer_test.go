package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-go/server"
)

func TestViewIndexCleanupTimerSpec(t *testing.T) {
	if viewIndexCleanupTimerService != "trpc.moox.storage.view.cleanup.timer" {
		t.Fatalf("timer service = %q", viewIndexCleanupTimerService)
	}
	if viewIndexCleanupTimerTimeout != 20*time.Second {
		t.Fatalf("timer timeout = %s", viewIndexCleanupTimerTimeout)
	}
}

func TestRegisterViewIndexCleanupTimerRejectsMissingService(t *testing.T) {
	err := RegisterViewIndexCleanupTimer(&server.Server{}, func(context.Context) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("err = %v", err)
	}
}
