package clsprovision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/stretchr/testify/require"
)

type fakeAPI struct {
	opened, logsetFound, topicFound bool
	calls                           []string
	fields                          []tencent.CLSIndexField
	failOpen                        error
}

func (f *fakeAPI) GetService(context.Context) (bool, error) { return f.opened, nil }
func (f *fakeAPI) OpenService(context.Context) (string, error) {
	f.calls = append(f.calls, "open")
	if f.failOpen != nil {
		return "", f.failOpen
	}
	f.opened = true
	return "", nil
}
func (f *fakeAPI) FindLogset(context.Context, string) (tencent.CLSLogset, bool, error) {
	return tencent.CLSLogset{ID: "logset-1"}, f.logsetFound, nil
}
func (f *fakeAPI) CreateLogset(context.Context, string) (tencent.CLSLogset, string, error) {
	f.calls = append(f.calls, "create-logset")
	return tencent.CLSLogset{ID: "logset-1"}, "", nil
}
func (f *fakeAPI) FindTopic(context.Context, string, string) (tencent.CLSTopic, bool, error) {
	return tencent.CLSTopic{ID: "topic-1", LogsetID: "logset-1", IndexEnabled: true}, f.topicFound, nil
}
func (f *fakeAPI) CreateTopic(context.Context, tencent.CLSCreateTopicOptions) (tencent.CLSTopic, string, error) {
	f.calls = append(f.calls, "create-topic")
	return tencent.CLSTopic{ID: "topic-1", LogsetID: "logset-1", IndexEnabled: true}, "", nil
}
func (f *fakeAPI) CreateIndex(context.Context, string) (string, error) { return "", nil }
func (f *fakeAPI) EnsureIndexFields(_ context.Context, _ string, fields []tencent.CLSIndexField) (bool, string, error) {
	f.calls = append(f.calls, "index-fields")
	f.fields = fields
	return true, "", nil
}

func newTestProvisioner(api *fakeAPI, builds *int) *Provisioner {
	return New(DefaultConfig(), func(Credential, string, string) (tencent.CLSAPI, error) {
		*builds++
		return api, nil
	})
}

func TestApplyProvisionsOnceAndFillsTheFunctionEnvironment(t *testing.T) {
	api, builds := &fakeAPI{}, 0
	p := newTestProvisioner(api, &builds)
	env := map[string]string{"MOOX_SPACE_ID": "crypto"}
	require.True(t, p.Apply(context.Background(), "acct", Credential{SecretID: "id", SecretKey: "key"}, env))
	require.Equal(t, "true", env["MOOX_CLS_ENABLED"])
	require.Equal(t, "topic-1", env["MOOX_CLS_TOPIC_ID"])
	require.Equal(t, "logset-1", env["MOOX_CLS_LOGSET_ID"])
	require.Equal(t, "ap-guangzhou.cls.tencentcs.com", env["MOOX_CLS_ENDPOINT"])
	require.Equal(t, "3000", env["MOOX_CLS_TIMEOUT_MS"])
	require.Equal(t, tencent.MarketFetchLogIndexFields, api.fields)
	require.Contains(t, api.calls, "open")
	require.Contains(t, api.calls, "create-logset")

	// 第二个函数：命中缓存，不再调云接口。
	second := map[string]string{}
	require.True(t, p.Apply(context.Background(), "acct", Credential{SecretID: "id", SecretKey: "key"}, second))
	require.Equal(t, 1, builds)
}

func TestApplyKeepsAnOperatorDeclaredCLSEnvironment(t *testing.T) {
	api, builds := &fakeAPI{}, 0
	p := newTestProvisioner(api, &builds)
	env := map[string]string{"MOOX_CLS_ENABLED": "false"}
	require.False(t, p.Apply(context.Background(), "acct", Credential{}, env))
	require.Equal(t, "false", env["MOOX_CLS_ENABLED"])
	require.Zero(t, builds)
}

func TestApplyFailureNeverBlocksAndBacksOff(t *testing.T) {
	api, builds := &fakeAPI{failOpen: errors.New("SecretKey=leaked")}, 0
	p := newTestProvisioner(api, &builds)
	now := time.Now()
	p.now = func() time.Time { return now }
	env := map[string]string{}
	require.False(t, p.Apply(context.Background(), "acct", Credential{}, env))
	require.Empty(t, env)
	// 退避期内不重复调用云接口。
	require.False(t, p.Apply(context.Background(), "acct", Credential{}, env))
	require.Equal(t, 1, builds)
	// 退避过后重试。
	now = now.Add(failureBackoff + time.Second)
	api.failOpen = nil
	require.True(t, p.Apply(context.Background(), "acct", Credential{}, env))
	require.Equal(t, 2, builds)
}

func TestDisabledProvisionerIsANoop(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	p := New(cfg, nil)
	env := map[string]string{}
	require.False(t, p.Apply(context.Background(), "acct", Credential{}, env))
	require.Empty(t, env)
	require.NoError(t, cfg.Validate())
	cfg.Enabled = true
	cfg.TimeoutMS = 10
	require.Error(t, cfg.Validate())
}

func TestApplyUsesTheDedicatedWriterCredentialWhenConfigured(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "writer.key")
	require.NoError(t, os.WriteFile(keyFile, []byte("writer-secret-key\n"), 0o600))
	cfg := DefaultConfig()
	cfg.WriterSecretID, cfg.WriterSecretKeyFile = "writer-id", keyFile
	require.NoError(t, cfg.Validate())
	api, builds := &fakeAPI{}, 0
	p := New(cfg, func(credential Credential, _, _ string) (tencent.CLSAPI, error) {
		builds++
		require.Equal(t, "management-id", credential.SecretID, "开通仍用管理凭证")
		return api, nil
	})
	env := map[string]string{}
	require.True(t, p.Apply(context.Background(), "acct", Credential{SecretID: "management-id", SecretKey: "management-key"}, env))
	require.Equal(t, "writer-id", env["MOOX_CLS_SECRET_ID"], "函数拿到的是只写专用凭证，不是管理密钥")
	require.Equal(t, "writer-secret-key", env["MOOX_CLS_SECRET_KEY"])
	require.Equal(t, 1, builds)

	// 专用凭证文件读不到时不注入，也不退回管理密钥。
	require.NoError(t, os.Remove(keyFile))
	other := map[string]string{}
	require.False(t, p.Apply(context.Background(), "acct", Credential{SecretID: "management-id", SecretKey: "management-key"}, other))
	require.Empty(t, other)
}

func TestWriterCredentialConfigMustBeSetTogether(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WriterSecretID = "only-id"
	require.ErrorContains(t, cfg.Validate(), "together")
	cfg.WriterSecretID, cfg.WriterSecretKeyFile = "", "/some/file"
	require.ErrorContains(t, cfg.Validate(), "together")
}

func TestConcurrentApplyForOneAccountProvisionsOnce(t *testing.T) {
	api, builds := &fakeAPI{}, 0
	p := newTestProvisioner(api, &builds)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env := map[string]string{}
			require.True(t, p.Apply(context.Background(), "acct", Credential{SecretID: "id", SecretKey: "key"}, env))
		}()
	}
	wg.Wait()
	require.Equal(t, 1, builds, "同一账号的并发请求合并为一次开通")
}

// 同账号的等待者随调用方取消退出，不被慢开通拖住；不同账号互不阻塞。
func TestApplyWaitersHonourCancellationAndAccountsDoNotBlockEachOther(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 4)
	api := &fakeAPI{}
	p := New(DefaultConfig(), func(credential Credential, _, _ string) (tencent.CLSAPI, error) {
		started <- credential.SecretID
		if credential.SecretID == "slow" {
			<-release
		}
		return api, nil
	})
	slowDone := make(chan bool, 1)
	go func() {
		slowDone <- p.Apply(context.Background(), "acct-slow", Credential{SecretID: "slow", SecretKey: "k"}, map[string]string{})
	}()
	require.Equal(t, "slow", <-started) // 慢开通已持有 acct-slow 的执行权

	// 同账号的第二个请求：调用方超时后立即退出。
	waiter, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	begin := time.Now()
	require.False(t, p.Apply(waiter, "acct-slow", Credential{SecretID: "slow", SecretKey: "k"}, map[string]string{}))
	require.Less(t, time.Since(begin), 2*time.Second)

	// 不同账号不被 acct-slow 阻塞。
	require.True(t, p.Apply(context.Background(), "acct-fast", Credential{SecretID: "fast", SecretKey: "k"}, map[string]string{}))

	close(release)
	require.True(t, <-slowDone)

	// 已取消的调用即使缓存命中也不继续。
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	require.False(t, p.Apply(cancelled, "acct-fast", Credential{SecretID: "fast", SecretKey: "k"}, map[string]string{}))
}
