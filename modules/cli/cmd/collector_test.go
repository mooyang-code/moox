package cmd

import "testing"

func TestCollectorFunctionCommandsAreRegistered(t *testing.T) {
	packageCmd, _, err := rootCmd.Find([]string{"collector", "function", "package"})
	if err != nil || packageCmd == nil {
		t.Fatalf("collector function package command not registered: %v", err)
	}
	for _, name := range []string{"collector-root", "version", "out", "config"} {
		if flag := packageCmd.Flags().Lookup(name); flag == nil {
			t.Fatalf("package command missing --%s", name)
		}
	}

	publishCmd, _, err := rootCmd.Find([]string{"collector", "function", "publish"})
	if err != nil || publishCmd == nil {
		t.Fatalf("collector function publish command not registered: %v", err)
	}
	for _, name := range []string{
		"control-url",
		"access-token",
		"cloud-account-id",
		"runtime",
		"handler",
		"region",
		"zip",
		"version",
		"package-name",
		"package-type",
		"biz-type",
		"node-type",
		"env",
		"function-config",
	} {
		if flag := publishCmd.Flags().Lookup(name); flag == nil {
			t.Fatalf("publish command missing --%s", name)
		}
	}
	if got := publishCmd.Flags().Lookup("handler").DefValue; got != "main" {
		t.Fatalf("handler default = %q, want main", got)
	}
}
