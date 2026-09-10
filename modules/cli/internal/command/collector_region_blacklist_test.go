package command

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/require"
)

func TestDisableCollectorBlacklistedTimersBeforePublishing(t *testing.T) {
	for _, status := range []string{"SUCCESS", "FAILED"} {
		t.Run(status, func(t *testing.T) {
			var submitted []collectorRuntimeConfigPatch
			waited := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/admin/cloudnode/GetNodeList":
					_ = json.NewEncoder(w).Encode(map[string]any{"ret_info": map[string]any{"code": 0}, "items": []adminclient.CloudNode{
						{NodeID: "blocked", Region: "ap-guangzhou", NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "timer", Metadata: map[string]any{"timer_enabled": true}},
						{NodeID: "instrument", Region: "ap-guangzhou", NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "timer", Metadata: map[string]any{"function_mode": "instrument_snapshot", "timer_enabled": false, "timer_actual_enabled": true}},
						{NodeID: "disabled", Region: "ap-guangzhou", NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "timer", Metadata: map[string]any{"timer_enabled": false}},
						{NodeID: "allowed", Region: "ap-singapore", NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "timer", Metadata: map[string]any{"timer_enabled": true}},
						{NodeID: "invoke", Region: "ap-guangzhou", NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "invoke"},
					}})
				case "/api/admin/cloudnode/SubmitUpdateNodeRuntimeConfigs":
					var request struct {
						Nodes []collectorRuntimeConfigPatch `json:"nodes"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					submitted = request.Nodes
					_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job_id":"disable"}`))
				case "/api/admin/cloudnode/GetNodeBatchChange":
					waited = true
					_ = json.NewEncoder(w).Encode(map[string]any{"ret_info": map[string]any{"code": 0}, "job": map[string]any{"job_id": "disable", "status": status}})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			defer server.Close()
			err := disableCollectorBlacklistedTimers(context.Background(), adminclient.New(server.URL), &setupconfig.SCFFetcherSpace{SpaceID: "crypto", RegionBlacklist: []string{"ap-guangzhou"}})
			if status == "SUCCESS" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "FAILED")
			}
			require.True(t, waited)
			require.Len(t, submitted, 3)
			require.Equal(t, "blocked", submitted[0].NodeID)
			require.Equal(t, "instrument", submitted[1].NodeID)
			for _, patch := range submitted {
				require.False(t, patch.TimerEnabled)
			}
		})
	}
}

type collectorBlacklistSSHStub struct {
	setupssh.Client
	result setupssh.Result
	err    error
	args   []string
}

func (s *collectorBlacklistSSHStub) Run(_ context.Context, args []string, _ io.Reader) (setupssh.Result, error) {
	s.args = args
	return s.result, s.err
}

func TestCollectorBlacklistRuntimePreflight(t *testing.T) {
	require.NoError(t, preflightCollectorBlacklistRuntime(context.Background(), nil, nil))
	require.Error(t, preflightCollectorBlacklistRuntime(context.Background(), nil, &setupconfig.SCFFetcherSpace{}))
	raw := "scf_region_blacklists:\n  crypto: [ap-guangzhou]\n"
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw)))
	cfg := &setupconfig.SCFFetcherSpace{SpaceID: "crypto", RegionBlacklist: []string{"ap-guangzhou"}}
	empty := &setupconfig.SCFFetcherSpace{SpaceID: "crypto"}
	require.ErrorContains(t, validateCollectorBlacklistRuntime(empty, []byte(raw), hash, "/data/moox/bin/moox-collector"), "differs")
	cleared := []byte("scf_region_blacklists:\n  crypto: []\n")
	require.NoError(t, validateCollectorBlacklistRuntime(empty, cleared, fmt.Sprintf("sha256:%x", sha256.Sum256(cleared)), "/data/moox/bin/moox-collector"))
	for _, test := range []struct {
		name, config, hash, exe string
		fail                    bool
	}{
		{"loaded", raw, hash, "/data/moox/bin/moox-collector", false},
		{"missing identity", raw, "", "/data/moox/bin/moox-collector", true},
		{"not restarted", raw, "sha256:old", "/data/moox/bin/moox-collector", true},
		{"wrong process", raw, hash, "/data/moox/bin/moox-admin", true},
		{"missing policy", "{}\n", fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("{}\n"))), "/data/moox/bin/moox-collector", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateCollectorBlacklistRuntime(cfg, []byte(test.config), test.hash, test.exe)
			if test.fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	ssh := &collectorBlacklistSSHStub{result: setupssh.Result{Stdout: hash + "\n/data/moox/bin/moox-collector\n" + raw}}
	require.NoError(t, verifyCollectorBlacklistRuntime(context.Background(), ssh, "/data/moox", cfg))
	require.Equal(t, "/data/moox", ssh.args[len(ssh.args)-1])
	ssh.err = fmt.Errorf("sensitive remote output")
	err := verifyCollectorBlacklistRuntime(context.Background(), ssh, "/data/moox", cfg)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "sensitive")
	ssh.err = nil
	ssh.result = setupssh.Result{ExitCode: 1}
	require.Error(t, verifyCollectorBlacklistRuntime(context.Background(), ssh, "/data/moox", cfg))
	ssh.result = setupssh.Result{Stdout: "incomplete"}
	require.Error(t, verifyCollectorBlacklistRuntime(context.Background(), ssh, "/data/moox", cfg))
}

func TestCollectorRegionBlacklistPreventsCreateAndEnable(t *testing.T) {
	cfg := setupconfig.SCFFetcherSpace{SpaceID: "crypto", RegionBlacklist: []string{"ap-guangzhou"}}
	_, err := buildCollectorCreateNodeItem(collectorPublishOptions{Region: "ap-guangzhou", FetcherConfig: &cfg}, "package")
	require.ErrorContains(t, err, "blacklisted")
	patches := collectorTimerEnablePatches([]adminclient.CloudNode{{NodeID: "blocked", Region: "ap-guangzhou"}, {NodeID: "allowed", Region: "ap-singapore"}}, cfg)
	require.Len(t, patches, 1)
	require.Equal(t, "allowed", patches[0].NodeID)
}
