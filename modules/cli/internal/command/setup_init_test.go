package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"

	"github.com/mooyang-code/moox/modules/cli/internal/rpcclient"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSetupInitStorage struct {
	stages []string
	record func(string)
}

type fakeSetupInitFactor struct {
	plan setupFactorPlan
}

func (f *fakeSetupInitFactor) Apply(_ context.Context, plan setupFactorPlan) (setupFactorSummary, error) {
	f.plan = plan
	return setupFactorSummary{Definitions: len(plan.Definitions), Tasks: len(plan.Tasks), Members: plan.memberCount(), DefinitionsCreated: len(plan.Definitions), MembersCreated: plan.memberCount()}, nil
}

func (f *fakeSetupInitFactor) Close() error { return nil }

func (f *fakeSetupInitStorage) recordStage(stage string) {
	f.stages = append(f.stages, stage)
	if f.record != nil {
		f.record(stage)
	}
}

func (f *fakeSetupInitStorage) Apply(_ context.Context, calls []metadataImportCall) (metadataImportSummary, error) {
	f.recordStage("storage-apply")
	return metadataImportSummary{Status: "ok", Planned: len(calls), Applied: len(calls)}, nil
}

func (f *fakeSetupInitStorage) Verify(_ context.Context, calls []metadataImportCall) (metadataImportSummary, error) {
	f.recordStage("verify")
	return metadataImportSummary{Status: "ok", Planned: len(calls), Unchanged: len(calls)}, nil
}

func (f *fakeSetupInitStorage) Close() error { return nil }

func TestSetupInitRequiresStorageHost(t *testing.T) {
	cmd := newSetupCommand(setupDeps{})
	cmd.SetArgs([]string{"init"})
	require.Error(t, cmd.Execute())
}

func TestLoadSetupInitBundleUsesDefaultMetadata(t *testing.T) {
	bundle, err := loadSetupInitBundle(defaultSetupBundlePath())
	require.NoError(t, err)
	assert.Equal(t, []string{"crypto", "stockcn", "stockhk", "stockus"}, bundle.SpaceIDs)
	require.NotEmpty(t, bundle.Calls)
	// 种子只剩 A 股财务两个数据集：行情数据集由采集任务派生，主机快照改存 Monitor。
	resources := map[string]int{}
	for _, call := range bundle.Calls {
		resources[call.Resource]++
	}
	require.Equal(t, 2, resources["datasets"])
}

func TestSetupInitRunsStagesInOrder(t *testing.T) {
	snapshot := testSetupSnapshot(t)
	stages := []string{}
	storage := &fakeSetupInitStorage{record: func(stage string) {
		stages = append(stages, stage)
	}}
	deps := setupDeps{
		loadInitBundle: func(string) (setupInitBundle, error) {
			stages = append(stages, "load-bundle")
			return setupInitBundle{
				SpaceIDs: []string{"crypto"},
				Calls:    make([]metadataImportCall, 3),
			}, nil
		},
		load: func(string) (*setupSnapshot, error) {
			stages = append(stages, "load-config")
			return snapshot, nil
		},
		ensureFirewall: func(context.Context, *setupSnapshot) (setupFirewallSummary, error) {
			stages = append(stages, "firewall")
			return setupFirewallSummary{Status: "ready", Targets: 3}, nil
		},
		applySecrets: func(context.Context, *setupSnapshot) (setupclient.ApplyResult, error) {
			stages = append(stages, "admin-apply")
			return setupclient.ApplyResult{Action: "created", Secrets: 1}, nil
		},
		setupStatus: func(context.Context, *setupSnapshot) (setupclient.StatusResult, error) {
			stages = append(stages, "admin-status")
			return setupclient.StatusResult{State: "completed"}, nil
		},
		ensureNotification: func(context.Context, *setupSnapshot) (*setupNotificationSummary, error) {
			stages = append(stages, "notification")
			return &setupNotificationSummary{Status: notificationKept, Existing: 1}, nil
		},
		registerCloudAccounts: func(context.Context, *setupSnapshot) (*setupCloudAccountSummary, error) {
			stages = append(stages, "cloud-accounts")
			return &setupCloudAccountSummary{Enabled: true, Planned: 1, Registered: 1}, nil
		},
		login: func(context.Context, *setupSnapshot) (setupclient.LoginResult, error) {
			stages = append(stages, "login")
			return setupclient.LoginResult{LoginAPI: "valid"}, nil
		},
		openInitStorage: func(context.Context, *setupSnapshot, string) (setupInitStorage, error) {
			return storage, nil
		},
		ensureArchiveTasks: func(_ context.Context, _ *setupSnapshot, spaceID string) (*setupArchiveTasksSummary, error) {
			require.Empty(t, spaceID, "setup init 应覆盖全部业务空间")
			stages = append(stages, "archive-tasks")
			return &setupArchiveTasksSummary{Enabled: true, Planned: 5, Created: 5}, nil
		},
	}
	var output bytes.Buffer
	cmd := newSetupCommand(deps)
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"init", "--file", "moox.toml", "--config-dir", "bundle", "--storage-host", "compute"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []string{
		"load-bundle",
		"load-config",
		"firewall",
		"admin-apply",
		"admin-status",
		"notification",
		"cloud-accounts",
		"login",
		"storage-apply",
		"verify",
		"archive-tasks",
	}, stages)
	var summary map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &summary))
	require.Equal(t, "pending", summary["browser_trust"].(map[string]any)["status"], "信任不在 setup init 里做")
	delete(summary, "browser_trust")
	summaryJSON, err := json.Marshal(summary)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"status":"ready",
		"firewall":{"status":"ready","targets":3,"rules":0,"skipped":0,"already_open":0,"created":0},
		"business_spaces":1,
		"business_space_ids":["crypto"],
		"admin":{"action":"created","users":0,"secrets":1},
		"notification":{"status":"kept","existing":1},
		"cloud_accounts":{"enabled":true,"planned":1,"registered":1,"unchanged":0},
		"admin_state":"completed",
		"login_api":"valid",
		"metadata":{"planned":3,"applied":3,"unchanged":0},
		"verification":{"planned":3,"unchanged":3},
		"archive_tasks":{"enabled":true,"planned":5,"created":5,"unchanged":0}
	}`, string(summaryJSON))
}

func TestSetupInitStopsAfterAdminFailure(t *testing.T) {
	snapshot := testSetupSnapshot(t)
	stages := []string{}
	cmd := newSetupCommand(setupDeps{
		loadInitBundle: func(string) (setupInitBundle, error) {
			stages = append(stages, "load-bundle")
			return setupInitBundle{}, nil
		},
		load: func(string) (*setupSnapshot, error) {
			stages = append(stages, "load-config")
			return snapshot, nil
		},
		ensureFirewall: func(context.Context, *setupSnapshot) (setupFirewallSummary, error) {
			stages = append(stages, "firewall")
			return setupFirewallSummary{Status: "ready"}, nil
		},
		applySecrets: func(context.Context, *setupSnapshot) (setupclient.ApplyResult, error) {
			stages = append(stages, "admin-apply")
			return setupclient.ApplyResult{}, errors.New("setup_invalid")
		},
	})
	cmd.SetArgs([]string{"init", "--storage-host", "compute"})
	require.ErrorContains(t, cmd.Execute(), "setup_invalid")
	assert.Equal(t, []string{"load-bundle", "load-config", "firewall", "admin-apply"}, stages)
}

func TestSetupInitStopsOnPartialFirewall(t *testing.T) {
	snapshot := testSetupSnapshot(t)
	adminApplied := false
	cmd := newSetupCommand(setupDeps{
		loadInitBundle: func(string) (setupInitBundle, error) { return setupInitBundle{}, nil },
		load:           func(string) (*setupSnapshot, error) { return snapshot, nil },
		ensureFirewall: func(context.Context, *setupSnapshot) (setupFirewallSummary, error) {
			return setupFirewallSummary{Status: "partial", Targets: 3, Skipped: 1}, nil
		},
		applySecrets: func(context.Context, *setupSnapshot) (setupclient.ApplyResult, error) {
			adminApplied = true
			return setupclient.ApplyResult{}, nil
		},
	})
	cmd.SetArgs([]string{"init", "--storage-host", "compute"})
	require.ErrorContains(t, cmd.Execute(), "firewall_incomplete")
	assert.False(t, adminApplied)
}

func TestSetupInitJSONDoesNotExposeSecrets(t *testing.T) {
	snapshot := testSetupSnapshot(t)
	storage := &fakeSetupInitStorage{}
	monitor := useFakeMonitorChannels(t, 0)
	cmd := newSetupCommand(setupDeps{
		loadInitBundle: func(string) (setupInitBundle, error) { return setupInitBundle{}, nil },
		load:           func(string) (*setupSnapshot, error) { return snapshot, nil },
		ensureFirewall: func(context.Context, *setupSnapshot) (setupFirewallSummary, error) {
			return setupFirewallSummary{Status: "ready"}, nil
		},
		applySecrets: func(context.Context, *setupSnapshot) (setupclient.ApplyResult, error) {
			return setupclient.ApplyResult{Action: "unchanged"}, nil
		},
		setupStatus: func(context.Context, *setupSnapshot) (setupclient.StatusResult, error) {
			return setupclient.StatusResult{State: "completed"}, nil
		},
		login: func(context.Context, *setupSnapshot) (setupclient.LoginResult, error) {
			return setupclient.LoginResult{LoginAPI: "valid"}, nil
		},
		openInitStorage: func(context.Context, *setupSnapshot, string) (setupInitStorage, error) {
			return storage, nil
		},
		ensureArchiveTasks: func(context.Context, *setupSnapshot, string) (*setupArchiveTasksSummary, error) {
			return nil, nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"init", "--storage-host", "compute"})
	require.NoError(t, cmd.Execute())
	for _, secret := range []string{
		"admin-test-password", "control-ssh-password", "other-ssh-password",
		"AKID-test-secret", "cloud-test-secret", "key=test",
	} {
		assert.NotContains(t, output.String(), secret)
	}
	require.Len(t, monitor.added, 1, "Monitor 没有渠道时写入秘密文件里的初始渠道")
	assert.Equal(t, "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test", monitor.added[0].GetWebhookUrl())
	assert.Contains(t, output.String(), `"notification":{"status":"created","type":"wecom","webhook":"https://qyapi.weixin.qq.com/…test"}`)
}

// 管理台已有渠道、密钥时 setup init 不改写：Monitor 已有任何渠道就不加；管理员、云密钥、交易所密钥由 ApplySetup 只建不改
// （服务端返回 kept / exists），CLI 不另外写，也不拿秘密文件里的密码去校验登录。
func TestSetupInitKeepsExistingChannelsAndSecrets(t *testing.T) {
	snapshot := testSetupSnapshot(t)
	monitor := useFakeMonitorChannels(t, 2)
	applied, logins := 0, 0
	cmd := newSetupCommand(setupDeps{
		loadInitBundle: func(string) (setupInitBundle, error) { return setupInitBundle{}, nil },
		load:           func(string) (*setupSnapshot, error) { return snapshot, nil },
		ensureFirewall: func(context.Context, *setupSnapshot) (setupFirewallSummary, error) {
			return setupFirewallSummary{Status: "ready"}, nil
		},
		applySecrets: func(context.Context, *setupSnapshot) (setupclient.ApplyResult, error) {
			applied++
			return setupclient.ApplyResult{Action: "unchanged", Kept: []string{"admin", "tencent_cloud"},
				ExchangeKeys: []setupclient.ExchangeKeyResult{{SecretID: "exchange-binance-production-main", State: setupclient.ExchangeKeyExists}}}, nil
		},
		setupStatus: func(context.Context, *setupSnapshot) (setupclient.StatusResult, error) {
			return setupclient.StatusResult{State: "completed"}, nil
		},
		login: func(context.Context, *setupSnapshot) (setupclient.LoginResult, error) {
			logins++
			return setupclient.LoginResult{}, errors.New("login_failed")
		},
		openInitStorage: func(context.Context, *setupSnapshot, string) (setupInitStorage, error) {
			return &fakeSetupInitStorage{}, nil
		},
		ensureArchiveTasks: func(context.Context, *setupSnapshot, string) (*setupArchiveTasksSummary, error) {
			return nil, nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"init", "--storage-host", "compute"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, 1, applied, "初始秘密只经 ApplySetup 写一次")
	assert.Empty(t, monitor.added, "Monitor 已有渠道时不写")
	assert.Zero(t, logins, "管理员以管理台为准时不校验登录")
	var summary struct {
		Admin        setupclient.ApplyResult  `json:"admin"`
		Notification setupNotificationSummary `json:"notification"`
		LoginSkipped string                   `json:"login_skipped"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &summary))
	assert.Equal(t, []string{"admin", "tencent_cloud"}, summary.Admin.Kept)
	assert.Equal(t, setupclient.ExchangeKeyExists, summary.Admin.ExchangeKeys[0].State)
	assert.Equal(t, setupNotificationSummary{Status: notificationKept, Existing: 2, Note: "Monitor 已有告警渠道，以管理台为准，没有写入"}, summary.Notification)
	assert.Contains(t, summary.LoginSkipped, "管理台")
}

// 秘密文件里没填 Webhook 地址时不连 Monitor。
func TestInitialNotificationIsSkippedWithoutAWebhook(t *testing.T) {
	summary, err := ensureInitialNotification(context.Background(), nil, topology.Notification{ChannelType: "wecom"})
	require.NoError(t, err)
	assert.Equal(t, notificationSkipped, summary.Status)
}

// fakeMonitorChannels 是 Monitor 告警渠道接口的替身：existing 是已有渠道数，added 记下写入的渠道。
type fakeMonitorChannels struct {
	existing int
	added    []*monitorpb.AddNotificationChannelReq
}

func useFakeMonitorChannels(t *testing.T, existing int) *fakeMonitorChannels {
	t.Helper()
	fake := &fakeMonitorChannels{existing: existing}
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *topology.Config) (commandGateway, error) { return fake, nil }
	t.Cleanup(func() { openCommandGateway = previous })
	return fake
}

func (f *fakeMonitorChannels) Invoke(_ context.Context, service, method string, req, rsp any) error {
	if service != monitorService {
		return errors.New("unexpected service " + service)
	}
	switch method {
	case "ListNotificationChannels":
		response := rsp.(*monitorpb.ListNotificationChannelsRsp)
		response.RetInfo = &commonpb.RetInfo{}
		for i := 0; i < f.existing; i++ {
			response.Channels = append(response.Channels, &monitorpb.NotificationChannel{ChannelId: fmt.Sprint(i), ChannelType: "feishu"})
		}
	case "AddNotificationChannel":
		f.added = append(f.added, req.(*monitorpb.AddNotificationChannelReq))
		rsp.(*monitorpb.AddNotificationChannelRsp).RetInfo = &commonpb.RetInfo{}
	default:
		return errors.New("unexpected method " + method)
	}
	return nil
}

func (f *fakeMonitorChannels) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return nil, errors.New("not used")
}

func (f *fakeMonitorChannels) Close() error { return nil }

func TestProvisionSetupCLSReportsReadyAndNeverBlocksInit(t *testing.T) {
	ready := provisionSetupCLS(context.Background(), clsAccountSource{}, func(string, string) (tencent.CLSAPI, error) { return clsReadyAPI{}, nil }, "", 0)
	require.Equal(t, "ready", ready.Status)
	require.Equal(t, "topic-1", ready.TopicID)
	require.True(t, ready.IndexFieldsChanged)

	failed := provisionSetupCLS(context.Background(), clsAccountSource{}, func(string, string) (tencent.CLSAPI, error) { return nil, errors.New("boom SecretKey=leak") }, "", 0)
	require.Equal(t, "failed", failed.Status)
	require.NotContains(t, failed.Warning, "leak")
}

type clsAccountSource struct{}

func (clsAccountSource) ListCloudAccounts(context.Context, string) ([]*collectorpb.CloudAccount, error) {
	return []*collectorpb.CloudAccount{{AccountId: "a", Provider: "tencent", CredentialSecretId: "s"}}, nil
}

func (clsAccountSource) GetSecretValue(context.Context, string) (*rpcclient.SecretMaterial, error) {
	return &rpcclient.SecretMaterial{Category: "cloud", Provider: "tencent", Status: "active", KeyID: "id", SecretValue: "key"}, nil
}

type clsReadyAPI struct{}

func (clsReadyAPI) GetService(context.Context) (bool, error)    { return true, nil }
func (clsReadyAPI) OpenService(context.Context) (string, error) { return "", nil }
func (clsReadyAPI) FindLogset(context.Context, string) (tencent.CLSLogset, bool, error) {
	return tencent.CLSLogset{ID: "logset-1"}, true, nil
}
func (clsReadyAPI) CreateLogset(context.Context, string) (tencent.CLSLogset, string, error) {
	return tencent.CLSLogset{}, "", nil
}
func (clsReadyAPI) FindTopic(context.Context, string, string) (tencent.CLSTopic, bool, error) {
	return tencent.CLSTopic{ID: "topic-1", LogsetID: "logset-1", IndexEnabled: true}, true, nil
}
func (clsReadyAPI) CreateTopic(context.Context, tencent.CLSCreateTopicOptions) (tencent.CLSTopic, string, error) {
	return tencent.CLSTopic{}, "", nil
}
func (clsReadyAPI) CreateIndex(context.Context, string) (string, error) { return "", nil }
func (clsReadyAPI) EnsureIndexFields(context.Context, string, []tencent.CLSIndexField) (bool, string, error) {
	return true, "r", nil
}
