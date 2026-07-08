package config

import "testing"

func TestApplyDefaultsDoesNotInventBackfillWindow(t *testing.T) {
	var cfg RuntimeConfig
	cfg.ApplyDefaults()
	if cfg.Storage.View.BackfillWindow != "" {
		t.Fatalf("backfill_window default = %q, want explicit required config", cfg.Storage.View.BackfillWindow)
	}
}

func TestParseWindowRequiresPositiveWindow(t *testing.T) {
	if _, err := ParseWindow("90d"); err != nil {
		t.Fatalf("ParseWindow(90d): %v", err)
	}
	for _, value := range []string{"", "0d", "-1d", "90x"} {
		if _, err := ParseWindow(value); err == nil {
			t.Fatalf("ParseWindow(%q) error = nil, want invalid", value)
		}
	}
}
