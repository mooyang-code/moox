package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudfunction/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/clsprovision"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/store"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/stretchr/testify/require"
)

type recordingCLSEnvironment struct {
	calls      int
	credential clsprovision.Credential
}

func (r *recordingCLSEnvironment) Apply(_ context.Context, _ string, credential clsprovision.Credential, environment map[string]string) bool {
	r.calls++
	r.credential = credential
	environment["MOOX_CLS_ENABLED"] = "true"
	return true
}

func TestApplyCLSEnvironmentOnlyTouchesMarketFetchFunctions(t *testing.T) {
	recorder := &recordingCLSEnvironment{}
	svc := &Service{clsEnvironment: recorder, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}}
	account := store.CloudAccount{AccountID: "acct"}
	fetcher := &store.FunctionInstance{Metadata: `{"workload_type":"market_fetcher"}`}
	other := &store.FunctionInstance{Metadata: `{"workload_type":"strategy"}`}

	environment := map[string]string{}
	require.True(t, svc.applyCLSEnvironment(context.Background(), account, fetcher, environment))
	require.Equal(t, "true", environment["MOOX_CLS_ENABLED"])
	require.Equal(t, clsprovision.Credential{SecretID: "id", SecretKey: "key"}, recorder.credential)

	untouched := map[string]string{}
	require.False(t, svc.applyCLSEnvironment(context.Background(), account, other, untouched))
	require.Empty(t, untouched)
	require.Equal(t, 1, recorder.calls)
}

func TestApplyCLSEnvironmentNeverBlocksOnCredentialOrConfigurationGaps(t *testing.T) {
	fetcher := &store.FunctionInstance{Metadata: `{"workload_type":"market_fetcher"}`}
	environment := map[string]string{}
	// 未配置 CLS 下发：空操作。
	require.False(t, (&Service{}).applyCLSEnvironment(context.Background(), store.CloudAccount{}, fetcher, environment))
	// 凭证取不到：不下发，也不报错。
	svc := &Service{clsEnvironment: &recordingCLSEnvironment{}, credentialResolver: fakeCredentialResolver{err: errors.New("secret unavailable")}}
	require.False(t, svc.applyCLSEnvironment(context.Background(), store.CloudAccount{}, fetcher, environment))
	require.Empty(t, environment)
}

// fullCLSEnvironment 补齐与真实实现同量级的七个变量（约 300 字节）。
type fullCLSEnvironment struct{}

func (fullCLSEnvironment) Apply(_ context.Context, _ string, _ clsprovision.Credential, environment map[string]string) bool {
	for _, key := range []string{"MOOX_CLS_ENABLED", "MOOX_CLS_ENDPOINT", "MOOX_CLS_LOGSET_ID", "MOOX_CLS_TOPIC_ID", "MOOX_CLS_SECRET_ID", "MOOX_CLS_SECRET_KEY", "MOOX_CLS_TIMEOUT_MS"} {
		environment[key] = strings.Repeat("v", 40)
	}
	return true
}

func TestApplyCLSEnvironmentSkipsInjectionThatWouldOverflowTheEnvironmentLimit(t *testing.T) {
	recorder := fullCLSEnvironment{}
	svc := &Service{clsEnvironment: recorder, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}}
	fetcher := &store.FunctionInstance{InstanceID: "fn", Metadata: `{"workload_type":"market_fetcher"}`}
	// 原环境已经接近平台上限：补齐 CLS 变量会超限，应放弃注入而不是让后续校验失败。
	environment := map[string]string{"MOOX_SPACE_ID": "crypto", "FILLER": strings.Repeat("x", tencent.SCFEnvironmentLimitBytes-150)}
	before := len(environment)
	require.False(t, svc.applyCLSEnvironment(context.Background(), store.CloudAccount{AccountID: "acct"}, fetcher, environment))
	require.Len(t, environment, before)
	require.NotContains(t, environment, "MOOX_CLS_ENABLED")
	require.NoError(t, tencent.ValidateSCFEnvironment(environment), "原环境保持合法")
}
