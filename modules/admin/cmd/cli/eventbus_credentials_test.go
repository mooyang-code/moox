package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

func TestEventBusCredentialsEnsureIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "admin.db")
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("test-encryption-key-for-eventbus"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applySchema(dbPath, adminschema.AdminSQL()); err != nil {
		t.Fatal(err)
	}
	seedEventBusDeployment(t, dbPath)
	args := []string{"eventbus-credentials", "ensure", "--db-path", dbPath, "--encryption-key-file", keyPath, "--node-id", "gateway-node-1"}
	var out bytes.Buffer
	if err := runEventBusCredentialsCommand(args, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	first := out.String()
	out.Reset()
	if err := runEventBusCredentialsCommand(args, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if first != out.String() {
		t.Fatalf("ensure metadata changed: %q vs %q", first, out.String())
	}
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Table("t_secrets").Where("c_category = ? AND c_provider = ? AND c_is_deleted = 0", "eventbus", "moox_eventbus").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 11 {
		t.Fatalf("eventbus records=%d, want 11（9 个角色令牌 + CA + 服务端证书）", count)
	}
}

func TestEventbusCredentialsFactorConsumer(t *testing.T) {
	tokens := make(map[string]string, len(eventBusRoles))
	for _, role := range eventBusRoles {
		tokens[role] = "token"
	}
	yaml := usersYAML(tokens)
	factorACL := eventBusACLBlock(yaml, "factor-eventbus")
	require.Contains(t, aclLine(factorACL, "subscribe:"), "moox.event.storage.collector.period.completed.v1.>")
	for _, permission := range []string{
		"$JS.API.CONSUMER.INFO.*.factor_collector_period_v1",
		"$JS.API.CONSUMER.CREATE.MOOX_STORAGE.factor_collector_period_v1",
		"$JS.API.CONSUMER.DURABLE.CREATE.MOOX_STORAGE.factor_collector_period_v1",
		"$JS.API.CONSUMER.MSG.NEXT.MOOX_STORAGE.factor_collector_period_v1",
		"$JS.ACK.MOOX_STORAGE.factor_collector_period_v1.>",
	} {
		require.Contains(t, aclLine(factorACL, "publish:"), permission)
	}
	require.Equal(t, []string{
		"eventbus-internal-admin", "hostagent-publisher", "metrics-publisher", "monitor-observability-consumer",
		"storage-eventbus",
		"factor-eventbus", "strategy-eventbus", "trade-eventbus", "collector-eventbus",
	}, eventBusRoles)
	collectorACL := eventBusACLBlock(yaml, "collector-eventbus")
	for _, permission := range []string{
		"moox.event.collector.kline.backfilled.v1.>",
		"$JS.API.CONSUMER.CREATE.MOOX_STORAGE.collector_resample_period_crypto_v1",
		"$JS.API.CONSUMER.MSG.NEXT.MOOX_COLLECTOR.collector_resample_backfilled_stockcn_v1",
		"$JS.ACK.MOOX_COLLECTOR.collector_resample_backfilled_crypto_v1.>",
	} {
		require.Contains(t, aclLine(collectorACL, "publish:"), permission)
	}
}

func TestEventBusCredentialsExportAndRotate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "admin.db")
	keyPath := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(keyPath, []byte("test-encryption-key-for-eventbus"), 0o600))
	require.NoError(t, applySchema(dbPath, adminschema.AdminSQL()))
	seedEventBusDeployment(t, dbPath)

	ensureArgs := []string{"eventbus-credentials", "ensure", "--db-path", dbPath, "--encryption-key-file", keyPath, "--node-id", "gateway-node-1"}
	var out bytes.Buffer
	require.NoError(t, runEventBusCredentialsCommand(ensureArgs, &out, &bytes.Buffer{}))

	exportDir := filepath.Join(dir, "out")
	exportArgs := []string{"eventbus-credentials", "export", "--db-path", dbPath, "--encryption-key-file", keyPath, "--output-dir", exportDir, "--node-id", "gateway-node-1"}
	out.Reset()
	require.NoError(t, runEventBusCredentialsCommand(exportArgs, &out, &bytes.Buffer{}))
	assert.FileExists(t, filepath.Join(exportDir, "users.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "ca.pem"))
	assert.FileExists(t, filepath.Join(exportDir, "server.pem"))
	assert.FileExists(t, filepath.Join(exportDir, "hostagent-publisher.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "metrics-publisher.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "monitor-observability.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "trade-eventbus.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "factor-eventbus.yaml"))
	for _, filename := range eventBusRoleFiles() {
		assert.FileExists(t, filepath.Join(exportDir, filename))
	}
	strategyCredential := filepath.Join(exportDir, "strategy-eventbus.yaml")
	assert.FileExists(t, strategyCredential)
	info, err := os.Stat(strategyCredential)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	// 导出一律写对外地址；各单元安装时再按"与 EventBus 是否同机"改写成实际连接地址。
	for name, wantURL := range map[string]string{
		"internal-admin.yaml":      "tls://203.0.113.10:4222",
		"metrics-publisher.yaml":   "tls://203.0.113.10:4222",
		"hostagent-publisher.yaml": "tls://203.0.113.10:4222",
		"storage-eventbus.yaml":    "tls://203.0.113.10:4222",
		"factor-eventbus.yaml":     "tls://203.0.113.10:4222",
	} {
		credential, loadErr := jetstream.LoadCredentialFile(filepath.Join(exportDir, name))
		require.NoError(t, loadErr, name)
		require.Equal(t, []string{wantURL}, credential.URLs, name)
	}

	yaml := usersYAML(map[string]string{
		"eventbus-internal-admin":        "a",
		"hostagent-publisher":            "b",
		"metrics-publisher":              "c",
		"monitor-observability-consumer": "d",
		"storage-eventbus":               "f",
		"factor-eventbus":                "h",
		"strategy-eventbus":              "i",
		"trade-eventbus":                 "k",
	})
	assert.Contains(t, yaml, "eventbus-internal-admin")
	assert.Contains(t, yaml, "factor-eventbus")
	assert.Contains(t, yaml, "moox.event.observability.metrics.snapshot.reported.v1.>")
	assert.NotContains(t, yaml, "moox.event.observability.health.")
	assert.Contains(t, yaml, "moox.event.observability.host.snapshot.reported.v1.>")
	internalAdminACL := eventBusACLBlock(yaml, "eventbus-internal-admin")
	assert.Equal(t, `subscribe: {allow: ["_INBOX.>", "$JS.EVENT.ADVISORY.API"]}`, aclLine(internalAdminACL, "subscribe:"))
	assert.NotContains(t, aclLine(internalAdminACL, "subscribe:"), "$JS.EVENT.ADVISORY.API.>")
	for role, password := range map[string]string{
		"eventbus-internal-admin":        "a",
		"hostagent-publisher":            "b",
		"metrics-publisher":              "c",
		"monitor-observability-consumer": "d",
		"storage-eventbus":               "f",
		"factor-eventbus":                "h",
		"strategy-eventbus":              "i",
		"trade-eventbus":                 "k",
	} {
		assert.Contains(t, eventBusACLBlock(yaml, role), "password: "+password)
	}
	metricsPublisherACL := eventBusACLBlock(yaml, "metrics-publisher")
	assert.NotContains(t, metricsPublisherACL, "$JS.API.>")
	assert.Contains(t, metricsPublisherACL, "moox.event.observability.metrics.snapshot.reported.v1.>")
	assert.NotContains(t, metricsPublisherACL, "moox.event.observability.host.snapshot")
	assert.Equal(t, `subscribe: {allow: ["_INBOX.>"]}`, aclLine(metricsPublisherACL, "subscribe:"))
	hostAgentACL := eventBusACLBlock(yaml, "hostagent-publisher")
	assert.Contains(t, hostAgentACL, "moox.event.observability.host.snapshot.reported.v1.>")
	monitorACL := eventBusACLBlock(yaml, "monitor-observability-consumer")
	assert.Contains(t, monitorACL, "$JS.API.CONSUMER.INFO.*.monitor_observability_ingest_v1")
	assert.Contains(t, monitorACL, "$JS.API.CONSUMER.CREATE.MOOX_OBSERVABILITY.monitor_observability_ingest_v1")
	assert.Contains(t, monitorACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_OBSERVABILITY.monitor_observability_ingest_v1")
	assert.Contains(t, monitorACL, "$JS.ACK.MOOX_OBSERVABILITY.monitor_observability_ingest_v1.>")
	assert.NotContains(t, monitorACL, "MOOX_METRICS")
	assert.Contains(t, yaml, "moox.event.trade.target.weight_requested.v1.>")
	strategyACL := eventBusACLBlock(yaml, "strategy-eventbus")
	assert.NotContains(t, strategyACL, "$JS.API.>")
	assert.Contains(t, strategyACL, `subscribe: {allow: ["_INBOX.>", "moox.event.storage.view.data.ready.v1.>"]}`)
	tradeACL := eventBusACLBlock(yaml, "trade-eventbus")
	assert.Contains(t, tradeACL, "$JS.API.CONSUMER.CREATE.MOOX_TRADE.trade_target_weight_v1")
	assert.Contains(t, tradeACL, "$JS.API.CONSUMER.INFO.*.trade_target_weight_v1")
	assert.Contains(t, tradeACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_TRADE.trade_target_weight_v1")
	assert.Contains(t, tradeACL, "$JS.ACK.MOOX_TRADE.trade_target_weight_v1.>")
	storageACL := eventBusACLBlock(yaml, "storage-eventbus")
	for _, subject := range []string{
		"moox.event.storage.dataset.rows.upserted.v2.>",
		"moox.event.storage.collector.period.completed.v1.>",
		"moox.event.storage.dataset.factor_period.computed.v1.>",
		"moox.event.storage.view.data.ready.v1.>",
		"moox.event.storage.dataset.sync_point.v1.>",
	} {
		assert.Contains(t, storageACL, subject)
	}
	assert.NotContains(t, storageACL, "moox.dlq.")
	assert.NotContains(t, storageACL, "moox.event.storage.rows_committed")
	assert.NotContains(t, storageACL, "storage_view_kline")
	for _, durable := range []string{"storage_view_factor", "storage_view_misc"} {
		assert.Contains(t, storageACL, "$JS.API.CONSUMER.INFO.*."+durable)
		assert.Contains(t, storageACL, "$JS.API.CONSUMER.CREATE.MOOX_STORAGE."+durable)
		assert.Contains(t, storageACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_STORAGE."+durable)
		assert.Contains(t, storageACL, "$JS.ACK.MOOX_STORAGE."+durable+".>")
	}
	assert.NotContains(t, storageACL, "storage_view_period_v1")
	assert.Contains(t, storageACL, `subscribe: {allow: ["_INBOX.>"]}`)
	assert.NotContains(t, aclLine(storageACL, "subscribe:"), "$JS.API")
	assert.NotContains(t, aclLine(storageACL, "subscribe:"), "$JS.ACK")
	assert.NotContains(t, storageACL, "storage_view_rows_committed_v1")
	assert.NotContains(t, yaml, "archive-eventbus")
	factorACL := eventBusACLBlock(yaml, "factor-eventbus")
	assert.Contains(t, aclLine(factorACL, "publish:"), "$JS.API.CONSUMER.CREATE.MOOX_STORAGE.factor_collector_period_v1")
	assert.Equal(t, `subscribe: {allow: ["_INBOX.>", "moox.event.storage.collector.period.completed.v1.>"]}`, aclLine(factorACL, "subscribe:"))
	assert.Equal(t, `responses: {max_messages: 1, expires: 10s}`, aclLine(factorACL, "responses:"))
	for _, role := range eventBusRoles {
		if role == "eventbus-internal-admin" {
			continue
		}
		acl := eventBusACLBlock(yaml, role)
		assert.NotContains(t, aclLine(acl, "subscribe:"), "$JS.API")
		assert.NotContains(t, aclLine(acl, "subscribe:"), "$JS.ACK")
		if role == "strategy-eventbus" {
			assert.Contains(t, acl, `subscribe: {allow: ["_INBOX.>", "moox.event.storage.view.data.ready.v1.>"]}`)
		} else if role == "factor-eventbus" {
			assert.Contains(t, acl, `subscribe: {allow: ["_INBOX.>", "moox.event.storage.collector.period.completed.v1.>"]}`)
		} else {
			assert.Contains(t, acl, `subscribe: {allow: ["_INBOX.>"]}`)
		}
	}

	bundle, err := makeTLSBundle("203.0.113.10")
	require.NoError(t, err)
	assert.Contains(t, bundle.CA, "BEGIN CERTIFICATE")
	assert.Contains(t, bundle.Cert, "BEGIN CERTIFICATE")
	assert.Contains(t, bundle.Key, "BEGIN RSA PRIVATE KEY")

	rotateArgs := []string{"eventbus-credentials", "rotate", "--db-path", dbPath, "--encryption-key-file", keyPath, "--credential", "hostagent-publisher"}
	err = runEventBusCredentialsCommand(rotateArgs, &bytes.Buffer{}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--confirm")

	rotateArgs = append(rotateArgs, "--confirm")
	out.Reset()
	require.NoError(t, runEventBusCredentialsCommand(rotateArgs, &out, &bytes.Buffer{}))
	storageRotateArgs := []string{"eventbus-credentials", "rotate", "--db-path", dbPath, "--encryption-key-file", keyPath, "--credential", "storage-eventbus", "--confirm"}
	out.Reset()
	require.NoError(t, runEventBusCredentialsCommand(storageRotateArgs, &out, &bytes.Buffer{}))
	assert.Contains(t, out.String(), `"rotated":"storage-eventbus"`)

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Table("t_secrets").Where("c_category = ? AND c_provider = ? AND c_is_deleted = 0", "eventbus", "moox_eventbus").Count(&count).Error)
	assert.GreaterOrEqual(t, count, int64(11))
}

func TestEventBusCredentialsReconcilePreservesRoleTokensAndRefreshesACL(t *testing.T) {
	dir := t.TempDir()
	roleFiles := map[string]string{
		"internal-admin.yaml":        "token: admin-token\n",
		"hostagent-publisher.yaml":   "eventbus_token: host-token\n",
		"metrics-publisher.yaml":     "token: metrics-token\n",
		"monitor-observability.yaml": "monitor_eventbus_token: monitor-token\n",
		"storage-eventbus.yaml":      "token: storage-token\n",
		"factor-eventbus.yaml":       "token: factor-token\n",
		"strategy-eventbus.yaml":     "token: strategy-token\n",
		"trade-eventbus.yaml":        "token: trade-token\n",
		"collector-eventbus.yaml":    "token: collector-token\n",
	}
	for name, content := range roleFiles {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	var out bytes.Buffer
	require.NoError(t, runEventBusCredentialsCommand([]string{"eventbus-credentials", "reconcile", "--output-dir", dir}, &out, &bytes.Buffer{}))
	raw, err := os.ReadFile(filepath.Join(dir, "users.yaml"))
	require.NoError(t, err)
	text := string(raw)
	assert.Contains(t, text, "moox.event.trade.target.weight_requested.v1.>")
	assert.Contains(t, text, "moox.event.storage.view.data.ready.v1.>")
	assert.NotContains(t, text, "moox.factor.internal")
	assert.Contains(t, text, "factor_collector_period_v1")
	assert.Contains(t, text, "strategy-token")
	assert.Contains(t, text, "factor-token")
	assert.Contains(t, text, "trade-token")
}

func seedEventBusDeployment(t *testing.T, dbPath string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	dao, err := sysdeploy.NewTopologyDAO(db, "gateway-node-1")
	require.NoError(t, err)
	require.NoError(t, dao.SyncHosts(t.Context(), []sysdeploy.HostSpec{{HostID: "gateway-node-1", Address: "203.0.113.10", Components: []string{"admin", "console-proxy", "web-host", "eventbus"}}}))
}

func eventBusACLBlock(yaml, username string) string {
	start := strings.Index(yaml, "  - username: "+username)
	if start < 0 {
		return ""
	}
	end := strings.Index(yaml[start+1:], "  - username: ")
	if end < 0 {
		return yaml[start:]
	}
	return yaml[start : start+1+end]
}

func aclLine(block, prefix string) string {
	start := strings.Index(block, prefix)
	if start < 0 {
		return ""
	}
	end := strings.IndexByte(block[start:], '\n')
	if end < 0 {
		return block[start:]
	}
	return block[start : start+end]
}

func TestEventBusCredentialsHelpers(t *testing.T) {
	assert.False(t, isEventBusCredentialsCommand(nil))
	assert.False(t, isEventBusCredentialsCommand([]string{"moox-admin"}))
	assert.True(t, isEventBusCredentialsCommand([]string{"moox-admin", "eventbus-credentials"}))

	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")
	require.NoError(t, atomicSecretFile(path, []byte("secret-data")))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "secret-data", string(data))

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, map[string]string{"ok": "1"}))
	assert.Contains(t, buf.String(), `"ok"`)
}

func TestGeneratedACLAllowsOwnedConsumerCreationAndStrategyPublish(t *testing.T) {
	tokens := map[string]string{}
	for index, role := range eventBusRoles {
		tokens[role] = fmt.Sprintf("token-%d", index)
	}
	var parsed struct {
		Users []struct {
			Username    string `yaml:"username"`
			Password    string `yaml:"password"`
			Permissions struct {
				Publish struct {
					Allow []string `yaml:"allow"`
					Deny  []string `yaml:"deny"`
				} `yaml:"publish"`
				Subscribe struct {
					Allow []string `yaml:"allow"`
					Deny  []string `yaml:"deny"`
				} `yaml:"subscribe"`
				Responses struct {
					MaxMessages int           `yaml:"max_messages"`
					Expires     time.Duration `yaml:"expires"`
				} `yaml:"responses"`
			} `yaml:"permissions"`
		} `yaml:"users"`
	}
	require.NoError(t, yamlv3.Unmarshal([]byte(usersYAML(tokens)), &parsed))
	users := make([]*natsserver.User, 0, len(parsed.Users))
	for _, item := range parsed.Users {
		publishAllow := append([]string(nil), item.Permissions.Publish.Allow...)
		var responses *natsserver.ResponsePermission
		if item.Permissions.Responses.MaxMessages > 0 && item.Permissions.Responses.Expires > 0 {
			responses = &natsserver.ResponsePermission{
				MaxMsgs: item.Permissions.Responses.MaxMessages,
				Expires: item.Permissions.Responses.Expires,
			}
			if len(publishAllow) == 0 {
				publishAllow = []string{}
			}
		}
		users = append(users, &natsserver.User{
			Username: item.Username,
			Password: item.Password,
			Permissions: &natsserver.Permissions{
				Response: responses,
				Publish: &natsserver.SubjectPermission{
					Allow: publishAllow,
					Deny:  item.Permissions.Publish.Deny,
				},
				Subscribe: &natsserver.SubjectPermission{
					Allow: item.Permissions.Subscribe.Allow,
					Deny:  item.Permissions.Subscribe.Deny,
				},
			},
		})
	}
	server, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), Users: users,
	})
	require.NoError(t, err)
	server.Start()
	require.True(t, server.ReadyForConnections(5*time.Second))
	t.Cleanup(server.Shutdown)

	admin, err := nats.Connect(
		server.ClientURL(),
		nats.UserInfo("eventbus-internal-admin", tokens["eventbus-internal-admin"]),
	)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	adminJS, err := admin.JetStream()
	require.NoError(t, err)
	_, err = adminJS.AddStream(&nats.StreamConfig{
		Name: "MOOX_TRADE", Subjects: []string{"moox.event.trade.target.weight_requested.v1.>"},
	})
	require.NoError(t, err)
	_, err = adminJS.AddStream(&nats.StreamConfig{
		Name: "MOOX_STORAGE", Subjects: []string{"moox.event.storage.>"},
	})
	require.NoError(t, err)
	_, err = adminJS.AddStream(&nats.StreamConfig{
		Name: "MOOX_OBSERVABILITY", Subjects: []string{"moox.event.observability.>"},
	})
	require.NoError(t, err)

	strategy, err := nats.Connect(
		server.ClientURL(),
		nats.UserInfo("strategy-eventbus", tokens["strategy-eventbus"]),
	)
	require.NoError(t, err)
	t.Cleanup(strategy.Close)
	strategyJS, err := strategy.JetStream()
	require.NoError(t, err)
	_, err = strategyJS.Publish("moox.event.trade.target.weight_requested.v1.space.binding", []byte("payload"))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	trade, err := jetstream.Connect(ctx, jetstream.Config{
		URLs: []string{server.ClientURL()}, Name: "trade-auth-e2e",
		Username: "trade-eventbus", Password: tokens["trade-eventbus"],
	})
	require.NoError(t, err)
	defer trade.Close()
	cfg := jetstream.ConsumerConfig{
		Stream: "MOOX_TRADE", Durable: "trade_target_weight_v1",
		FilterSubject: "moox.event.trade.target.weight_requested.v1.>",
		AckWait:       time.Second, MaxDeliver: 3, MaxAckPending: 8,
		FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
	}
	consumer, err := trade.NewConsumer(ctx, cfg)
	require.NoError(t, err)
	require.NoError(t, consumer.Close())
	cfg.AckWait = 2 * time.Second
	consumer, err = trade.NewConsumer(ctx, cfg)
	require.NoError(t, err)
	defer consumer.Close()

	cfg.Durable = "not_owned"
	_, err = trade.NewConsumer(ctx, cfg)
	require.Error(t, err)

	for role, subjects := range map[string][]string{
		"hostagent-publisher": {
			"moox.event.observability.host.snapshot.reported.v1.mooxsys.host-1",
		},
		"metrics-publisher": {
			"moox.event.observability.metrics.snapshot.reported.v1.mooxsys.trade/instance-1",
		},
	} {
		publisher, connectErr := nats.Connect(
			server.ClientURL(),
			nats.UserInfo(role, tokens[role]),
		)
		require.NoError(t, connectErr)
		t.Cleanup(publisher.Close)
		publisherJS, jetStreamErr := publisher.JetStream()
		require.NoError(t, jetStreamErr)
		for _, subject := range subjects {
			_, publishErr := publisherJS.Publish(subject, []byte("payload"))
			require.NoError(t, publishErr, role, subject)
		}
	}

	monitorCtx, monitorCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer monitorCancel()
	monitor, err := jetstream.Connect(monitorCtx, jetstream.Config{
		URLs: []string{server.ClientURL()}, Name: "monitor-observability-auth-e2e",
		Username: "monitor-observability-consumer",
		Password: tokens["monitor-observability-consumer"],
	})
	require.NoError(t, err)
	defer monitor.Close()
	monitorConsumer, err := monitor.NewConsumer(monitorCtx, jetstream.ConsumerConfig{
		Stream: "MOOX_OBSERVABILITY", Durable: "monitor_observability_ingest_v1",
		FilterSubject: "moox.event.observability.>",
		AckWait:       time.Second, MaxDeliver: 3, MaxAckPending: 8,
		FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
	})
	require.NoError(t, err)
	require.NoError(t, monitorConsumer.Close())

	storage, err := nats.Connect(
		server.ClientURL(),
		nats.UserInfo("storage-eventbus", tokens["storage-eventbus"]),
	)
	require.NoError(t, err)
	t.Cleanup(storage.Close)
	storageJS, err := storage.JetStream()
	require.NoError(t, err)
	_, err = storageJS.Publish("moox.event.storage.view.data.ready.v1.crypto.view", []byte("ready"))
	require.NoError(t, err)
	_, err = storageJS.Publish("moox.event.storage.collector.period.completed.v1.crypto.dataset_binance_kline_1m", []byte("period"))
	require.NoError(t, err)

	factorCtx, factorCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer factorCancel()
	factorJS, err := jetstream.Connect(factorCtx, jetstream.Config{
		URLs: []string{server.ClientURL()}, Name: "factor-period-auth-e2e",
		Username: "factor-eventbus", Password: tokens["factor-eventbus"],
	})
	require.NoError(t, err)
	defer factorJS.Close()
	factorConsumer, err := factorJS.NewConsumer(factorCtx, jetstream.ConsumerConfig{
		Stream: "MOOX_STORAGE", Durable: "factor_collector_period_v1",
		FilterSubject: "moox.event.storage.collector.period.completed.v1.>",
		AckWait:       time.Second, MaxDeliver: 3, MaxAckPending: 8,
		FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
	})
	require.NoError(t, err)
	require.NoError(t, factorConsumer.Close())
}

func TestEventBusURLFollowsRemoteDeploymentAndRejectsDisabledHost(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")), &gorm.Config{})
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	require.NoError(t, db.Exec(adminschema.AdminSQL()).Error)
	dao, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	require.NoError(t, dao.SyncHosts(t.Context(), []sysdeploy.HostSpec{
		{HostID: "control", Address: "control.example.test", Components: []string{"admin", "console-proxy", "web-host"}},
		{HostID: "bus", Address: "bus.example.test", Components: []string{"eventbus"}},
	}))
	address, err := eventBusNATSURL(db, "control")
	require.NoError(t, err)
	require.Equal(t, "tls://bus.example.test:4222", address)
	require.NoError(t, dao.SetHostStatus(t.Context(), "bus", "disabled"))
	_, err = eventBusNATSURL(db, "control")
	require.ErrorContains(t, err, "active EventBus deployment not found")
}

// EventBus 换主机之后：旧 TLS 身份绑定的是旧地址，ensure、export、export-clients 都必须拒绝，
// 轮换之后全部通过；角色令牌不变，CA 与服务端证书换新。
func TestEventBusCredentialsRotateTLSAfterEventBusMoves(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "admin.db")
	keyPath := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(keyPath, []byte("test-encryption-key-for-eventbus"), 0o600))
	require.NoError(t, applySchema(dbPath, adminschema.AdminSQL()))
	seedEventBusDeployment(t, dbPath)

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		full := append([]string{"eventbus-credentials", args[0], "--db-path", dbPath, "--encryption-key-file", keyPath, "--node-id", "gateway-node-1"}, args[1:]...)
		err := runEventBusCredentialsCommand(full, &out, &bytes.Buffer{})
		return out.String(), err
	}
	_, err := run("ensure")
	require.NoError(t, err)
	oldDir := filepath.Join(dir, "old")
	_, err = run("export", "--output-dir", oldDir)
	require.NoError(t, err)
	oldCA, err := os.ReadFile(filepath.Join(oldDir, "ca.pem"))
	require.NoError(t, err)
	oldUsers, err := os.ReadFile(filepath.Join(oldDir, "users.yaml"))
	require.NoError(t, err)

	// EventBus 从控制机搬到存储机：先去掉控制机上的登记，同一次同步里加上存储机的登记。
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	topology, err := sysdeploy.NewTopologyDAO(db, "gateway-node-1")
	require.NoError(t, err)
	require.NoError(t, topology.SyncHosts(t.Context(), []sysdeploy.HostSpec{
		{HostID: "gateway-node-1", Address: "203.0.113.10", Components: []string{"admin", "console-proxy", "web-host"}},
		{HostID: "storage-node-1", Address: "203.0.113.20", Components: []string{"eventbus"}},
	}))
	closeAdminCLIDB(db)

	for _, sub := range []string{"ensure", "export"} {
		args := []string{sub}
		if sub == "export" {
			args = append(args, "--output-dir", filepath.Join(dir, "stale"))
		}
		_, err = run(args...)
		require.Error(t, err, sub)
		assert.Contains(t, err.Error(), "explicit identity rotation is required", sub)
	}
	_, err = run("export-clients", "--output-dir", filepath.Join(dir, "stale-clients"), "--roles", "strategy-eventbus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "explicit identity rotation is required")

	_, err = run("rotate-tls")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--confirm")
	out, err := run("rotate-tls", "--confirm")
	require.NoError(t, err)
	assert.Contains(t, out, `"rotated":"tls"`)
	assert.Contains(t, out, "tls://203.0.113.20:4222")

	_, err = run("ensure")
	require.NoError(t, err)
	newDir := filepath.Join(dir, "new")
	_, err = run("export", "--output-dir", newDir)
	require.NoError(t, err)
	newCA, err := os.ReadFile(filepath.Join(newDir, "ca.pem"))
	require.NoError(t, err)
	newUsers, err := os.ReadFile(filepath.Join(newDir, "users.yaml"))
	require.NoError(t, err)
	assert.NotEqual(t, string(oldCA), string(newCA), "CA 必须换新")
	assert.Equal(t, string(oldUsers), string(newUsers), "角色令牌不变")
	credential, err := jetstream.LoadCredentialFile(filepath.Join(newDir, "strategy-eventbus.yaml"))
	require.NoError(t, err)
	assert.Equal(t, []string{"tls://203.0.113.20:4222"}, credential.URLs)

	clientsParent := filepath.Join(dir, "clients-parent")
	require.NoError(t, os.Mkdir(clientsParent, 0o700))
	clientsDir := filepath.Join(clientsParent, "clients")
	_, err = run("export-clients", "--output-dir", clientsDir, "--roles", "strategy-eventbus")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(clientsDir, "clients.json"))

	// 没有 TLS 身份时没有可轮换的东西。
	emptyDB := filepath.Join(dir, "empty.db")
	require.NoError(t, applySchema(emptyDB, adminschema.AdminSQL()))
	seedEventBusDeployment(t, emptyDB)
	var buf bytes.Buffer
	err = runEventBusCredentialsCommand([]string{"eventbus-credentials", "rotate-tls", "--db-path", emptyDB, "--encryption-key-file", keyPath, "--node-id", "gateway-node-1", "--confirm"}, &buf, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run ensure first")
}
