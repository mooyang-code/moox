package config

import (
	"testing"
	"time"
)

func TestLoadRepositoryConfig(t *testing.T) {
	cfg, err := Load("../../config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EventBus.Durable != "streamcalc_kline_v1" || cfg.Aggregation.TargetFrequency != "5m" || cfg.Aggregation.AllowedLateness != 30*time.Second {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestParseFrequency(t *testing.T) {
	for value, want := range map[string]time.Duration{"1m": time.Minute, "5m": 5 * time.Minute, "1h": time.Hour, "1d": 24 * time.Hour} {
		got, err := ParseFrequency(value)
		if err != nil || got != want {
			t.Fatalf("ParseFrequency(%q) = %s, %v; want %s", value, got, err, want)
		}
	}
	if _, err := ParseFrequency("30s"); err == nil {
		t.Fatal("ParseFrequency(30s) error = nil")
	}
}
