package jobqueue

import (
	"strings"
	"testing"
)

func TestExecSubjectUsesCloudNodePrefix(t *testing.T) {
	cfg := NamingConfig{SubjectPrefix: "moox.cloudnode"}

	got := ExecSubject(cfg, "crypto", "moox-collector_dev", "collect.kline")
	want := "moox.cloudnode.exec.v1.jobitem.s.crypto.pkg.moox-collector_dev.type.collect_kline"
	if got != want {
		t.Fatalf("ExecSubject() = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, "moox.storage.") {
		t.Fatalf("exec subject must not use storage prefix: %s", got)
	}
}

func TestTokenFallsBackToHashWhenTooLong(t *testing.T) {
	raw := strings.Repeat("A", 80)

	got := SubjectToken(raw)
	if !strings.HasPrefix(got, "x") || len(got) != 17 {
		t.Fatalf("SubjectToken(%d chars) = %q, want x + 16 hex chars", len(raw), got)
	}
}

func TestTokenSanitizesRouteUnsafeChars(t *testing.T) {
	got := SubjectToken("Collect.Kline/V1 ")
	if got != "collect_kline_v1" {
		t.Fatalf("SubjectToken() = %q, want collect_kline_v1", got)
	}
}

func TestValidateNamingRejectsStoragePrefix(t *testing.T) {
	if err := ValidateNamingConfig(NamingConfig{SubjectPrefix: "moox.storage"}); err == nil {
		t.Fatalf("ValidateNamingConfig(moox.storage) error = nil, want failure")
	}
}
