package test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	accesskit "github.com/mooyang-code/moox/packages/gatewayclient/testkit"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestSkillUsesProductionAccessWithoutOperatorFiles(t *testing.T) {
	storage := &klineStorageStub{requests: make(chan *pb.ReadTimeSeriesRowsReq, 4)}
	credential := gatewayauth.Credentials{Caller: "moox-skill", KeyID: "assigned-skill-key-73", Secret: "fixture-skill-external-signing-key-at-least-32-bytes"}
	access := accesskit.StartAccess(t, accesskit.AccessOptions{
		AccessBinary: buildAccessServer(t), GatewayBinary: buildGatewayE2EHelper(t), HostID: klineGatewayNode,
		StorageAddress: startKlineStorage(t, storage), ExternalCredentials: []gatewayauth.Credentials{credential},
	})
	configPath := writeKlineConfig(t)
	keyPath := filepath.Join(filepath.Dir(configPath), "assigned-skill.key")
	require.NoError(t, os.WriteFile(keyPath, []byte(credential.Secret), 0o600))
	original, err := os.ReadFile(configPath)
	require.NoError(t, err)
	config := fmt.Sprintf("gateway_client:\n  access_address: %q\n  access_id: %q\n  caller: moox-skill\n  key_id: %q\n  key_file: assigned-skill.key\n", access.Address, access.InstanceID, credential.KeyID)
	require.NoError(t, os.WriteFile(configPath, append([]byte(config), original...), 0o600))
	binary := buildMooxCLI(t)
	command := exec.Command(binary, "data", "skill", "kline", "get", "--config", configPath,
		"--data-type", "crypto", "--symbol", "BTC-USDT", "--interval", "1m", "--limit", "1")
	// This empty home has no manifest, SSH configuration, directory cache or CA.
	command.Env = append(os.Environ(), "HOME="+t.TempDir(), "MOOX_ACCESS_ADDRESS=invalid:1", "MOOX_GATEWAY_SERVICE_SECRET_KEY=stale-operator-secret")
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	output, err := command.Output()
	require.NoError(t, err, "Skill diagnostics: %s", diagnostics.String())
	for _, secret := range []string{credential.Secret, klineStorageAppKey} {
		require.NotContains(t, string(output), secret)
		require.NotContains(t, diagnostics.String(), secret)
	}
	var response pb.ReadTimeSeriesRowsRsp
	require.NoError(t, protojson.Unmarshal(output, &response))
	require.Len(t, response.GetRows(), 1)
	require.Equal(t, "BTC-USDT", response.GetRows()[0].GetKey().GetSubjectId())
	select {
	case req := <-storage.requests:
		require.Equal(t, "crypto", req.GetSpaceId())
		require.Equal(t, klineStorageAppID, req.GetAuthInfo().GetAppId())
	case <-time.After(3 * time.Second):
		t.Fatal("Storage did not receive the Skill query through Access")
	}
	gateway, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.External, AccessAddress: access.Address, AccessInstanceID: access.InstanceID, Credentials: credential})
	require.NoError(t, err)
	defer gateway.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err = gateway.Invoke(ctx, "trpc.moox.storage.PrimaryStore", "UpsertFields", &pb.PrimaryUpsertFieldsReq{}, &pb.PrimaryUpsertFieldsRsp{})
	require.Error(t, err, "the Skill principal must be rejected before a write reaches Storage")
	require.Zero(t, storage.writeCalls.Load())
	require.NotContains(t, err.Error(), credential.Secret)
}

func buildAccessServer(t *testing.T) string {
	t.Helper()
	if binary := prebuiltE2EBinary(t, "MOOX_CLI_ACCESS_BINARY"); binary != "" {
		return binary
	}
	binary := filepath.Join(t.TempDir(), "moox-access")
	command := exec.Command("go", "build", "-o", binary, "./cmd/server")
	command.Dir = filepath.Join("..", "..", "access")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "build moox-access: %s", output)
	return binary
}
