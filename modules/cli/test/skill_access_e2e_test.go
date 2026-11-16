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
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	accesskit "github.com/mooyang-code/moox/packages/gatewayclient/testkit"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestSkillUsesProductionAccessWithoutOperatorFiles(t *testing.T) {
	storage := &klineStorageStub{requests: make(chan *pb.ReadTimeSeriesRowsReq, 4)}
	credential := authtest.Identity("moox-skill")
	// Access 与 Storage 同机：Access 直连服务目录里 PrimaryStore 的固定回环端口，假 Storage 就监听在那里（端口被占用时跳过）。
	// 夹具网关只给 Access 提供目录。T16 把夹具改成带外部入口的网关后不再占固定端口。
	storageAddress := serveKlineStorage(t, storage, accesskit.LocalStorageListener(t))
	access := accesskit.StartAccess(t, accesskit.AccessOptions{
		AccessBinary: buildAccessServer(t), GatewayBinary: buildGatewayE2EHelper(t), HostID: klineGatewayNode,
		StorageAddress: storageAddress, ExternalCredentials: []gatewayauth.Credentials{credential},
	})
	configPath := writeKlineConfig(t)
	keyPath := filepath.Join(filepath.Dir(configPath), "assigned-skill.key")
	secret := gatewayauth.EncodeSigningKey(credential.Key)
	require.NoError(t, os.WriteFile(keyPath, []byte(secret+"\n"), 0o600))
	original, err := os.ReadFile(configPath)
	require.NoError(t, err)
	config := fmt.Sprintf("gateway_client:\n  access_address: %q\n  access_id: %q\n  caller: moox-skill\n  key_file: assigned-skill.key\n", access.Address, access.InstanceID)
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
	require.NotContains(t, err.Error(), secret)
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
