package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/stretchr/testify/require"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
)

func TestLiveCleanupLegacyCryptoSCF(t *testing.T) {
	if os.Getenv("MOOX_LIVE_CLEANUP_LEGACY_CRYPTO_SCF") != "1" {
		t.Skip("opt-in")
	}
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	require.NoError(t, err)
	defer clearSetupSecrets(snapshot)

	const region = "ap-hongkong"
	const namespace = "moox-crypto-ns2"
	const legacyPrefix = "moox-fetcher-crypto-binance-ap-hongkong-"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
		SecretID:  snapshot.Manifest.TencentCloud.SecretID,
		SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
		Region:    region,
	})
	require.NoError(t, err)
	functions, err := network.ListSCFFunctions(ctx, namespace, []string{legacyPrefix})
	require.NoError(t, err)
	require.Len(t, functions, 31, "refuse cleanup unless the exact known legacy fleet is present")

	names := make([]string, 0, len(functions))
	for _, fn := range functions {
		require.True(t, strings.HasPrefix(fn.FunctionName, legacyPrefix))
		require.NotContains(t, fn.FunctionName, "-invoke-")
		info, getErr := network.GetSCFFunction(ctx, namespace, fn.FunctionName)
		require.NoError(t, getErr)
		require.Equal(t, "crypto", info.Environment["MOOX_SPACE_ID"])
		require.True(t, strings.HasPrefix(info.Environment["MOOX_CODE_PACKAGE_ID"], "moox-collector-crypto-"), "unexpected package ownership for %s", fn.FunctionName)
		names = append(names, fn.FunctionName)
	}
	t.Logf("validated legacy crypto SCF fleet for cleanup: count=%d first=%s last=%s", len(names), names[0], names[len(names)-1])

	cred := common.NewCredential(snapshot.Manifest.TencentCloud.SecretID, snapshot.Manifest.TencentCloud.SecretKey)
	cp := profile.NewClientProfile()
	cp.HttpProfile.Endpoint = "scf.tencentcloudapi.com"
	cp.HttpProfile.ReqTimeout = 30
	client, err := scf.NewClient(cred, region, cp)
	require.NoError(t, err)
	for _, name := range names {
		req := scf.NewDeleteFunctionRequest()
		req.FunctionName = common.StringPtr(name)
		req.Namespace = common.StringPtr(namespace)
		_, deleteErr := client.DeleteFunctionWithContext(ctx, req)
		require.NoError(t, deleteErr, name)
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		remaining, listErr := network.ListSCFFunctions(ctx, namespace, []string{legacyPrefix})
		require.NoError(t, listErr)
		if len(remaining) == 0 {
			t.Logf("legacy crypto SCF cleanup verified: deleted=%d remaining=0", len(names))
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("legacy functions still present after cleanup: %d", len(remaining))
		}
		time.Sleep(2 * time.Second)
	}
}
