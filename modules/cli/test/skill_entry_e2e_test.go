package test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// Skill 只带自己的签名私钥与入口地址：经主机网关的外部入口读 K 线，名单外的写方法在入口就被拒。
// 夹具网关（entry-native）把本机登记成外部入口，监听上挂的是生产的外部入口处理，假 Storage 监听临时端口。
func TestSkillUsesExternalEntryWithoutOperatorFiles(t *testing.T) {
	storage := &klineStorageStub{requests: make(chan *pb.ReadTimeSeriesRowsReq, 4)}
	credential := authtest.Identity("moox-skill")
	entryAddress := startEntryGateway(t, startKlineStorage(t, storage))
	configPath := writeKlineConfig(t)
	keyPath := filepath.Join(filepath.Dir(configPath), "assigned-skill.key")
	secret := gatewayauth.EncodeSigningKey(credential.Key)
	require.NoError(t, os.WriteFile(keyPath, []byte(secret+"\n"), 0o600))
	original, err := os.ReadFile(configPath)
	require.NoError(t, err)
	config := fmt.Sprintf("gateway_client:\n  entry_address: %q\n  entry_host: %q\n  caller: moox-skill\n  key_file: assigned-skill.key\n", entryAddress, klineGatewayNode)
	require.NoError(t, os.WriteFile(configPath, append([]byte(config), original...), 0o600))
	binary := buildMooxCLI(t)
	command := exec.Command(binary, "data", "skill", "kline", "get", "--config", configPath,
		"--data-type", "crypto", "--symbol", "BTC-USDT", "--interval", "1m", "--limit", "1")
	// This empty home has no manifest, SSH configuration, directory cache or CA.
	command.Env = append(os.Environ(), "HOME="+t.TempDir(), "MOOX_ENTRY_ADDRESS=invalid:1", "MOOX_GATEWAY_SERVICE_SECRET_KEY=stale-operator-secret")
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	output, err := command.Output()
	require.NoError(t, err, "Skill diagnostics: %s", diagnostics.String())
	require.NotContains(t, string(output), secret)
	require.NotContains(t, diagnostics.String(), secret)
	var response pb.ReadTimeSeriesRowsRsp
	require.NoError(t, protojson.Unmarshal(output, &response))
	require.Len(t, response.GetRows(), 1)
	require.Equal(t, "BTC-USDT", response.GetRows()[0].GetKey().GetSubjectId())
	select {
	case req := <-storage.requests:
		require.Equal(t, "crypto", req.GetSpaceId())
	case <-time.After(3 * time.Second):
		t.Fatal("Storage did not receive the Skill query through the external entry")
	}
	gateway, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.External, EntryAddress: entryAddress, EntryHost: klineGatewayNode, Credentials: credential})
	require.NoError(t, err)
	defer gateway.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err = gateway.Invoke(ctx, "trpc.moox.storage.PrimaryStore", "UpsertFields", &pb.PrimaryUpsertFieldsReq{}, &pb.PrimaryUpsertFieldsRsp{})
	require.Error(t, err, "the Skill principal must be rejected before a write reaches Storage")
	require.Zero(t, storage.writeCalls.Load())
	require.NotContains(t, err.Error(), secret)
}

// startEntryGateway 起一个把本机登记成外部入口的夹具网关，返回外部入口的地址。
func startEntryGateway(t *testing.T, upstreamAddress string) string {
	t.Helper()
	readyFile := filepath.Join(t.TempDir(), "gateway.ready")
	process := startGatewayHelperProcessFor(t, buildGatewayE2EHelper(t), "moox-skill",
		"--mode", "entry-native", "--node-id", klineGatewayNode, "--upstream-addr", upstreamAddress, "--ready-file", readyFile)
	t.Cleanup(func() {
		if process.stop(5 * time.Second) {
			t.Errorf("gateway helper required kill: %s", process.logs.String())
		}
	})
	target, err := process.waitForReady(readyFile, 30*time.Second)
	require.NoError(t, err)
	return strings.TrimPrefix(target, "ip://")
}
