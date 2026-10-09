package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/privatenet"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

func TestSetupPrivateNetworkDryRunJSON(t *testing.T) {
	snapshot := setupSnapshot(t)
	called := false
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		ensurePrivateNetwork: func(_ context.Context, got *setupconfig.Snapshot, opts privatenet.Options, _ io.Writer) (privatenet.Result, error) {
			called = true
			require.True(t, opts.DryRun)
			require.False(t, opts.SkipProbe)
			require.Equal(t, snapshot, got)
			return privatenet.Result{DryRun: true, Status: "dry_run", Plan: privatenet.Plan{Ports: []string{"11003"}}}, nil
		},
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"private-network", "--file", "moox.toml", "--dry-run"})
	require.NoError(t, cmd.Execute())
	require.True(t, called)
	var result privatenet.Result
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	require.Equal(t, "dry_run", result.Status)
	require.Equal(t, []string{"11003"}, result.Plan.Ports)
	require.NotContains(t, stdout.String(), "admin-test-password")
	require.NotContains(t, stdout.String(), "AKID-test-secret")
}

func TestSetupPrivateNetworkSkipProbeFlag(t *testing.T) {
	snapshot := setupSnapshot(t)
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		ensurePrivateNetwork: func(_ context.Context, _ *setupconfig.Snapshot, opts privatenet.Options, _ io.Writer) (privatenet.Result, error) {
			require.False(t, opts.DryRun)
			require.True(t, opts.SkipProbe)
			return privatenet.Result{Status: "ready"}, nil
		},
	})
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"private-network", "--file", "moox.toml", "--skip-probe"})
	require.NoError(t, cmd.Execute())
}

func TestSetupSCFNetworkPlanCommandPrintsResolvedRoutes(t *testing.T) {
	snapshot := setupSnapshot(t)
	plan := privatenet.SCFRoutePlan{Routes: []privatenet.SCFAccessRoute{
		{Region: "ap-nanjing", Network: "vpc", AccessHostID: "storage", AccessID: "access@storage", AccessAddress: "10.0.0.5:11004", VpcID: "vpc-1", SubnetID: "subnet-1"},
	}}
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		resolveSCFRoutes: func(_ context.Context, got *setupconfig.Snapshot, region string) (privatenet.SCFRoutePlan, error) {
			require.Equal(t, snapshot, got)
			require.Equal(t, "ap-nanjing", region)
			return plan, nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"scf-network-plan", "--file", "moox.toml", "--region", "ap-nanjing"})
	require.NoError(t, cmd.Execute())
	var got privatenet.SCFRoutePlan
	require.NoError(t, json.Unmarshal(output.Bytes(), &got))
	require.Equal(t, plan.Routes, got.Routes)
}
