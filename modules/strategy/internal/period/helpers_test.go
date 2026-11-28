package period

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

func shanghai(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func stockDay(t *testing.T, year int, month time.Month, day int) time.Time {
	t.Helper()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func parseStrategy(t *testing.T, raw string) dsl.Strategy {
	t.Helper()
	strategy, err := dsl.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("解析 DSL 失败：%v", err)
	}
	return strategy
}
