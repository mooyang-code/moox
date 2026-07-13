package command

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunCLSBootstrapDryRunDoesNotResolveCredentials(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	opts := clsBootstrapOptions{
		SecretID: "should-not-be-printed", SecretKey: "also-secret",
		Region: "ap-guangzhou", LogsetName: "moox", TopicName: "moox-application",
		RetentionDays: 30, Partitions: 1, DryRun: true,
	}
	if err := runCLSBootstrap(cmd, opts); err != nil {
		t.Fatalf("runCLSBootstrap() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{`"dry_run": true`, `"action": "OpenClsService"`, `"logset_name": "moox"`, `"topic_name": "moox-application"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("output %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, opts.SecretID) || strings.Contains(got, opts.SecretKey) {
		t.Fatalf("dry-run output leaked credentials: %s", got)
	}
}

func TestCLSIngestHostUsesRegionalInternalEndpoint(t *testing.T) {
	if got := clsIngestHost("ap-shanghai"); got != "ap-shanghai.cls.tencentyun.com" {
		t.Fatalf("clsIngestHost() = %q", got)
	}
}
