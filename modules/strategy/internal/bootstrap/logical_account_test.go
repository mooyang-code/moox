package bootstrap

import (
	"context"
	"strings"
	"testing"
)

func TestLogicalAccountOwnerStubFailsClosedUntilTradeRPCIsAvailable(t *testing.T) {
	client := newLogicalAccountOwnerClient("ip://trade:11200")
	for name, call := range map[string]func() error{
		"validate": func() error {
			return client.Validate(context.Background(), "space-1", "logical-1")
		},
		"claim": func() error {
			return client.Claim(context.Background(), "space-1", "logical-1", "runner-1")
		},
		"release": func() error {
			return client.Release(context.Background(), "space-1", "logical-1", "runner-1")
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil || !strings.Contains(err.Error(), "LogicalAccount owner RPC") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLogicalAccountOwnerStubValidatesIdentityBeforeTransport(t *testing.T) {
	client := newLogicalAccountOwnerClient("ip://trade:11200")
	if err := client.Validate(context.Background(), "", "logical-1"); err == nil ||
		!strings.Contains(err.Error(), "space_id") {
		t.Fatalf("error = %v", err)
	}
	if err := client.Claim(
		context.Background(),
		"space-1",
		"logical-1",
		"",
	); err == nil || !strings.Contains(err.Error(), "runner_id") {
		t.Fatalf("error = %v", err)
	}
}
