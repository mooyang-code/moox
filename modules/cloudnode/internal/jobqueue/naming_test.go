package jobqueue

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenBoundsLongValues(t *testing.T) {
	raw := strings.Repeat("A", 80)

	got := SubjectToken(raw)
	if len(got) > 57 || !strings.Contains(got, "_") {
		t.Fatalf("SubjectToken(%d chars) = %q, want bounded readable token with hash", len(raw), got)
	}
}

func TestTokenSanitizesRouteUnsafeChars(t *testing.T) {
	got := SubjectToken("Collect.Kline/V1 ")
	if !strings.HasPrefix(got, "collect_kline_v1_") {
		t.Fatalf("SubjectToken() = %q, want collect_kline_v1 plus identity hash", got)
	}
}

func TestTokenAndConsumerNamesDoNotCollapseNormalizedRoutes(t *testing.T) {
	if SubjectToken("collect.kline") == SubjectToken("collect/kline") {
		t.Fatal("normalized job types must retain distinct identities")
	}
	if ConsumerName("a.b", "pkg", "collect.kline") == ConsumerName("a/b", "pkg", "collect.kline") {
		t.Fatal("consumer names must retain the full route identity")
	}
}

func TestValidateNamingRejectsStoragePrefix(t *testing.T) {
	if err := ValidateNamingConfig(NamingConfig{SubjectPrefix: "moox.storage"}); err == nil {
		t.Fatalf("ValidateNamingConfig(moox.storage) error = nil, want failure")
	}
}

func TestNamingHelpers_ShouldBuildSubjectsAndConsumers(t *testing.T) {
	cfg := NamingConfig{SubjectPrefix: "moox.cloudnode"}
	filter := ExecFilterSubject(cfg, "crypto", "pkg-a", "collect.kline")
	assert.Equal(t, "moox.cloudnode.job.requested.v1.>", filter)
	stream := ExecStreamSubject(cfg)
	assert.Equal(t, "moox.cloudnode.>", stream)
	name := ConsumerName("crypto", "pkg-a", "collect.kline")
	assert.True(t, strings.HasPrefix(name, "cn_exec_"))
}
