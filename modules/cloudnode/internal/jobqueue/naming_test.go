package jobqueue

import (
	"strings"
	"testing"
)

func TestConsumerNamesDoNotCollapseDistinctRoutes(t *testing.T) {
	if ConsumerName("a.b", "pkg", "collect.kline") == ConsumerName("a/b", "pkg", "collect.kline") {
		t.Fatal("consumer names must retain the full route identity")
	}
}

func TestConsumerNameIsStableAndBounded(t *testing.T) {
	name := ConsumerName("crypto", "pkg-a", "collect.kline")
	if !strings.HasPrefix(name, "cn_exec_") || len(name) != len("cn_exec_")+24 {
		t.Fatalf("consumer name = %q", name)
	}
	if name != ConsumerName("crypto", "pkg-a", "collect.kline") {
		t.Fatalf("consumer name is not stable: %q", name)
	}
}
