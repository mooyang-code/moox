package main

import "testing"

func TestParseCommandRequiresCommand(t *testing.T) {
	if _, err := parseArgs(nil); err == nil {
		t.Fatal("parseArgs accepted empty args")
	}
}
