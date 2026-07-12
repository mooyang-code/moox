package main

import "testing"

func TestParseCommandRequiresCommand(t *testing.T) {
	if _, err := parseArgs(nil); err == nil {
		t.Fatal("parseArgs accepted empty args")
	}
}

func TestParseArgsAcceptsKnownCommands(t *testing.T) {
	cfg, err := parseArgs([]string{"status", "--space", "crypto"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Command != "status" || cfg.Space != "crypto" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseArgsRejectsUnknownCommand(t *testing.T) {
	if _, err := parseArgs([]string{"unknown"}); err == nil {
		t.Fatal("expected unknown command error")
	}
}
