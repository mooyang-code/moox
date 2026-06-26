package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestTencentLighthouseFirewallAddCommandIsRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"ops", "tencent", "lighthouse", "firewall", "add"})
	if err != nil || cmd == nil {
		t.Fatalf("ops tencent lighthouse firewall add command not registered: %v", err)
	}
	for _, name := range []string{
		"secret-id",
		"secret-key",
		"region",
		"endpoint",
		"instance-id",
		"public-ip",
		"ports",
		"protocol",
		"cidr",
		"ipv6-cidr",
		"action",
		"description",
		"firewall-version",
		"dry-run",
	} {
		if flag := cmd.Flags().Lookup(name); flag == nil {
			t.Fatalf("firewall add command missing --%s", name)
		}
	}
	if got := cmd.Flags().Lookup("protocol").DefValue; got != "TCP" {
		t.Fatalf("protocol default = %q, want TCP", got)
	}
	if got := cmd.Flags().Lookup("cidr").DefValue; got != "0.0.0.0/0" {
		t.Fatalf("cidr default = %q, want 0.0.0.0/0", got)
	}
}

func TestLighthouseFirewallAddDryRunRejectsInvalidPorts(t *testing.T) {
	var out bytes.Buffer
	cmd := lighthouseFirewallAddCmd
	cmd.SetOut(&out)
	err := runLighthouseFirewallAdd(cmd, lighthouseFirewallAddOptions{
		DryRun:      true,
		InstanceID:  "lhins-demo",
		Protocol:    "TCP",
		Ports:       "bad",
		Cidr:        "0.0.0.0/0",
		Action:      "ACCEPT",
		Description: "moox",
	})
	if err == nil {
		t.Fatal("dry-run with invalid ports should fail")
	}
	if strings.Contains(out.String(), `"dry_run"`) {
		t.Fatalf("dry-run validation failure should not emit success JSON: %s", out.String())
	}
}
