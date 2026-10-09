package marketstorage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

const storageAppKeysEnv = "MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"

func TestStoragePrimaryAppKeysRejectDuplicateAppID(t *testing.T) {
	key := strings.Repeat("a", 64)
	for _, secondID := range []string{"moox-collector", `moox-\u0063ollector`} {
		raw := `{"moox-collector":"` + key + `","` + secondID + `":"` + strings.Repeat("b", 64) + `"}`
		keys, err := parseStoragePrimaryAppKeys(raw)
		require.Error(t, err)
		require.Nil(t, keys)
	}
}

func TestStorageRuntimeAuthMatchesBindingAppIDExactly(t *testing.T) {
	setStorageAuthConfig(t, "moox-collector", "host-key")
	t.Setenv(storageAppKeysEnv, `{" moox-collector ":"`+strings.Repeat("a", 64)+`"}`)
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "host-test-secret")
	assertStorageAuthConstructorsFailBeforeProxy(t)
}

func TestStorageRuntimeAuthRejectsInvalidManagedCredentialsBeforeProxy(t *testing.T) {
	key := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"invalid JSON", "{"},
		{"duplicate", `{"moox-collector":"` + key + `","moox-collector":"` + strings.Repeat("b", 64) + `"}`},
		{"null", "null"},
		{"array", "[]"},
		{"trailing JSON", `{"moox-collector":"` + key + `"} {}`},
		{"non-string", `{"moox-collector":17}`},
		{"null key", `{"moox-collector":null}`},
		{"short key", `{"moox-collector":"private-test-value"}`},
		{"non-hex key", `{"moox-collector":"` + strings.Repeat("z", 64) + `"}`},
		{"missing binding", `{"other":"` + key + `"}`},
		{"blank app ID", `{" ":"` + key + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setStorageAuthConfig(t, "moox-collector", "host-fallback-key")
			t.Setenv(storageAppKeysEnv, tc.raw)
			t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "host-test-secret")
			assertStorageAuthConstructorsFailBeforeProxy(t)
		})
	}
}

func TestStorageRuntimeAuthMissingSCFCredentialsFailsBeforeProxy(t *testing.T) {
	setStorageAuthConfig(t, "moox-collector", "")
	unsetStorageAuthEnv(t, storageAppKeysEnv)
	unsetStorageAuthEnv(t, "MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	assertStorageAuthConstructorsFailBeforeProxy(t)
}

func TestStorageRuntimeAuthMissingBindingAppIDFailsBeforeProxy(t *testing.T) {
	setStorageAuthConfig(t, "", "host-fallback-key")
	unsetStorageAuthEnv(t, storageAppKeysEnv)
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "host-test-secret")
	assertStorageAuthConstructorsFailBeforeProxy(t)
}

func assertStorageAuthConstructorsFailBeforeProxy(t *testing.T) {
	t.Helper()
	access := &authPeriodClient{}
	proxyCalls := installAuthProxyFactories(t, access)
	writer, err := NewBatchStorageWithWriteSource("ip://127.0.0.1:11003", InstTypeSPOT, "test")
	require.Error(t, err)
	require.Nil(t, writer)
	metadata, err := NewGatewayResampleMetadataClient(storageGatewayFunc(func(context.Context, string, string, any, any) error { return nil }), InstTypeSPOT)
	require.Error(t, err)
	require.Nil(t, metadata)
	resample, err := NewGatewayResampleStorage(storageGatewayFunc(func(context.Context, string, string, any, any) error { return nil }), InstTypeSPOT, "test")
	require.Error(t, err)
	require.Nil(t, resample)
	auth, err := ResolveStorageAuthInfo(InstTypeSPOT)
	require.Error(t, err)
	require.Nil(t, auth)
	require.NotContains(t, err.Error(), "host-test-secret")
	require.NotContains(t, err.Error(), "private-test-value")
	require.Zero(t, *proxyCalls, "credential errors must be raised before constructing a proxy")
	require.Zero(t, access.calls, "invalid credentials must never reach Storage")
}

func TestStorageRuntimeManagedAuthCompletesPeriodRPC(t *testing.T) {
	setStorageAuthConfig(t, "moox-collector", "host-fallback-key")
	key := strings.Repeat("a", 64)
	t.Setenv(storageAppKeysEnv, `{"other":"`+strings.Repeat("b", 64)+`","moox-collector":"`+key+`"}`)
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "host-test-secret")
	access := &authPeriodClient{t: t, wantKey: key}
	installAuthProxyFactories(t, access)
	writer, err := NewBatchStorageWithWriteSource("ip://127.0.0.1:11003", InstTypeSPOT, "test")
	require.NoError(t, err)
	expectation := validStorageExpectation(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	_, err = writer.EnsureDatasetPeriod(context.Background(), expectation)
	require.NoError(t, err)
	require.NoError(t, writer.CommitTimeSeriesBatch(context.Background(), expectation, []*storagepb.TimeSeriesBatchRow{{SeriesIndex: 0}}, "test-event"))
	require.Equal(t, 2, access.calls)
	metadata, err := NewGatewayResampleMetadataClient(storageGatewayFunc(func(context.Context, string, string, any, any) error { return nil }), InstTypeSPOT)
	require.NoError(t, err)
	require.Equal(t, key, metadata.Auth.AppKey)
	resample, err := NewGatewayResampleStorage(storageGatewayFunc(func(context.Context, string, string, any, any) error { return nil }), InstTypeSPOT, "test")
	require.NoError(t, err)
	require.Equal(t, key, resample.(*storageWriter).authInfo.AppKey)
}

func TestStorageRuntimeHostAuthFallback(t *testing.T) {
	for _, secret := range []string{"", "host-test-secret"} {
		t.Run("secret="+secret, func(t *testing.T) {
			setStorageAuthConfig(t, "moox-collector", "host-config-key")
			unsetStorageAuthEnv(t, storageAppKeysEnv)
			t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", secret)
			wantKey := "host-config-key"
			if secret != "" {
				wantKey = mooxsecurity.HMACSHA256Hex(secret, []byte("moox-collector"))
			}
			auth, err := ResolveStorageAuthInfo(InstTypeSPOT)
			require.NoError(t, err)
			require.Equal(t, wantKey, auth.AppKey)
			require.Equal(t, "test-operator", auth.Operator)
			require.Equal(t, "test-request", auth.RequestId)
		})
	}
}

func setStorageAuthConfig(t *testing.T, appID, appKey string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "binance.yaml")
	config := "storage:\n  bindings:\n    spot:\n      auth_info:\n        app_id: '" + appID + "'\n        app_key: '" + appKey + "'\n        operator: test-operator\n        request_id: test-request\n"
	require.NoError(t, os.WriteFile(path, []byte(config), 0600))
	t.Setenv("MOOX_STORAGE_MARKET_CONFIG", path)
}

func unsetStorageAuthEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	require.NoError(t, os.Unsetenv(name))
}

func installAuthProxyFactories(t *testing.T, access storagepb.PrimaryStoreClientProxy) *int {
	t.Helper()
	oldPrimary, oldMetadata := storagepb.NewPrimaryStoreClientProxy, storagepb.NewMetadataClientProxy
	t.Cleanup(func() {
		storagepb.NewPrimaryStoreClientProxy, storagepb.NewMetadataClientProxy = oldPrimary, oldMetadata
	})
	calls := new(int)
	storagepb.NewPrimaryStoreClientProxy = func(...client.Option) storagepb.PrimaryStoreClientProxy {
		*calls += 1
		return access
	}
	storagepb.NewMetadataClientProxy = func(...client.Option) storagepb.MetadataClientProxy {
		*calls += 1
		return nil
	}
	return calls
}

type authPeriodClient struct {
	storagepb.PrimaryStoreClientProxy
	t       *testing.T
	wantKey string
	calls   int
}

func (c *authPeriodClient) EnsureDatasetPeriod(_ context.Context, req *storagepb.PrimaryEnsureDatasetPeriodReq, _ ...client.Option) (*storagepb.PrimaryEnsureDatasetPeriodRsp, error) {
	c.calls++
	require.Equal(c.t, "moox-collector", req.AuthInfo.AppId)
	require.Equal(c.t, c.wantKey, req.AuthInfo.AppKey)
	return &storagepb.PrimaryEnsureDatasetPeriodRsp{RetInfo: storageSuccess(), Status: "waiting", DeadlineAt: req.Expectation.DeadlineAt}, nil
}

func (c *authPeriodClient) CommitTimeSeriesBatch(_ context.Context, req *storagepb.PrimaryCommitTimeSeriesBatchReq, _ ...client.Option) (*storagepb.PrimaryCommitTimeSeriesBatchRsp, error) {
	c.calls++
	require.Equal(c.t, "moox-collector", req.AuthInfo.AppId)
	require.Equal(c.t, c.wantKey, req.AuthInfo.AppKey)
	return &storagepb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: storageSuccess(), AcceptedSeriesIndexes: []uint32{0}}, nil
}

type storageGatewayFunc func(context.Context, string, string, any, any) error

func (f storageGatewayFunc) Invoke(ctx context.Context, service, method string, request, response any) error {
	return f(ctx, service, method, request, response)
}

func TestInternalGatewayWriterDoesNotRepeatFailedCalls(t *testing.T) {
	setStorageAuthConfig(t, "moox-collector", "host-config-key")
	unsetStorageAuthEnv(t, storageAppKeysEnv)
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "fixture-primary-secret")
	expectation := validStorageExpectation(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	for _, operation := range []struct {
		name string
		call func(BatchStorage) error
	}{
		{"CommitTimeSeriesBatch", func(w BatchStorage) error {
			return w.CommitTimeSeriesBatch(t.Context(), expectation, []*storagepb.TimeSeriesBatchRow{{SeriesIndex: 0}}, "stable-event")
		}},
		{"RecordDatasetPeriodFailures", func(w BatchStorage) error {
			_, err := w.RecordDatasetPeriodFailures(t.Context(), expectation, []uint32{0})
			return err
		}},
		{"UpsertFields", func(w BatchStorage) error { return w.UpsertFields(t.Context(), nil) }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			calls := 0
			gateway := storageGatewayFunc(func(_ context.Context, service, method string, request, response any) error {
				calls++
				require.Equal(t, "trpc.moox.storage.PrimaryStore", service)
				require.Equal(t, operation.name, method)
				return context.DeadlineExceeded
			})
			writer, err := NewGatewayBatchStorage(gateway, InstTypeSPOT, "collector")
			require.NoError(t, err)
			require.ErrorIs(t, operation.call(writer), context.DeadlineExceeded)
			require.Equal(t, 1, calls)
		})
	}
	var methods []string
	gateway := storageGatewayFunc(func(_ context.Context, _, method string, request, response any) error {
		methods = append(methods, method)
		return context.DeadlineExceeded
	})
	writer, err := NewGatewayBatchStorage(gateway, InstTypeSPOT, "collector")
	require.NoError(t, err)
	_, err = writer.EnsureDatasetPeriod(t.Context(), expectation)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, []string{"EnsureDatasetPeriod", "GetDatasetPeriodStatus"}, methods, "an unknown write outcome is checked once without resending the write")
}
