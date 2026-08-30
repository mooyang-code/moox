package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/glebarez/sqlite"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if count != 15 {
		t.Fatalf("eventbus records=%d, want 15", count)
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
	assert.FileExists(t, filepath.Join(exportDir, "archive-eventbus.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "trade-eventbus.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "cloudnode-worker.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "market-fetch-publisher.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "collector-market-fetch-consumer.yaml"))
	strategyCredential := filepath.Join(exportDir, "strategy-eventbus.yaml")
	assert.FileExists(t, strategyCredential)
	info, err := os.Stat(strategyCredential)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	for name, wantURL := range map[string]string{
		"internal-admin.yaml":      "tls://127.0.0.1:4222",
		"metrics-publisher.yaml":   "tls://127.0.0.1:4222",
		"cloudnode-eventbus.yaml":  "tls://127.0.0.1:4222",
		"cloudnode-worker.yaml":    "tls://203.0.113.10:4222",
		"hostagent-publisher.yaml": "tls://203.0.113.10:4222",
		"storage-eventbus.yaml":    "tls://203.0.113.10:4222",
		"archive-eventbus.yaml":    "tls://203.0.113.10:4222",
	} {
		credential, loadErr := jetstream.LoadCredentialFile(filepath.Join(exportDir, name))
		require.NoError(t, loadErr, name)
		require.Equal(t, []string{wantURL}, credential.URLs, name)
	}

	yaml := usersYAML(map[string]string{
		"eventbus-internal-admin":         "a",
		"hostagent-publisher":             "b",
		"metrics-publisher":               "c",
		"monitor-observability-consumer":  "d",
		"storage-eventbus":                "f",
		"cloudnode-eventbus":              "g",
		"cloudnode-worker":                "worker",
		"market-fetch-publisher":          "publisher",
		"collector-market-fetch-consumer": "consumer",
		"factor-eventbus":                 "h",
		"strategy-eventbus":               "i",
		"archive-eventbus":                "j",
		"trade-eventbus":                  "k",
	})
	assert.Contains(t, yaml, "eventbus-internal-admin")
	assert.Contains(t, yaml, "factor-eventbus")
	assert.Contains(t, yaml, "moox.observability.metrics.snapshot.reported.v1.>")
	assert.Contains(t, yaml, "moox.observability.health.check.reported.v1.>")
	assert.Contains(t, yaml, "moox.observability.host.snapshot.reported.v1.>")
	internalAdminACL := eventBusACLBlock(yaml, "eventbus-internal-admin")
	assert.Equal(t, `subscribe: {allow: ["_INBOX.>", "$JS.EVENT.ADVISORY.API"]}`, aclLine(internalAdminACL, "subscribe:"))
	assert.NotContains(t, aclLine(internalAdminACL, "subscribe:"), "$JS.EVENT.ADVISORY.API.>")
	for role, password := range map[string]string{
		"eventbus-internal-admin":         "a",
		"hostagent-publisher":             "b",
		"metrics-publisher":               "c",
		"monitor-observability-consumer":  "d",
		"storage-eventbus":                "f",
		"cloudnode-eventbus":              "g",
		"cloudnode-worker":                "worker",
		"market-fetch-publisher":          "publisher",
		"collector-market-fetch-consumer": "consumer",
		"factor-eventbus":                 "h",
		"archive-eventbus":                "j",
		"strategy-eventbus":               "i",
		"trade-eventbus":                  "k",
	} {
		assert.Contains(t, eventBusACLBlock(yaml, role), "password: "+password)
	}
	metricsPublisherACL := eventBusACLBlock(yaml, "metrics-publisher")
	assert.NotContains(t, metricsPublisherACL, "$JS.API.>")
	assert.Contains(t, metricsPublisherACL, "moox.observability.metrics.snapshot.reported.v1.>")
	assert.NotContains(t, metricsPublisherACL, "moox.observability.host.snapshot")
	assert.Equal(t, `subscribe: {allow: ["_INBOX.>"]}`, aclLine(metricsPublisherACL, "subscribe:"))
	hostAgentACL := eventBusACLBlock(yaml, "hostagent-publisher")
	assert.Contains(t, hostAgentACL, "moox.observability.host.snapshot.reported.v1.>")
	monitorACL := eventBusACLBlock(yaml, "monitor-observability-consumer")
	assert.Contains(t, monitorACL, "$JS.API.CONSUMER.INFO.*.monitor_observability_ingest_v1")
	assert.Contains(t, monitorACL, "$JS.API.CONSUMER.CREATE.MOOX_OBSERVABILITY.monitor_observability_ingest_v1")
	assert.Contains(t, monitorACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_OBSERVABILITY.monitor_observability_ingest_v1")
	assert.Contains(t, monitorACL, "$JS.ACK.MOOX_OBSERVABILITY.monitor_observability_ingest_v1.>")
	assert.NotContains(t, monitorACL, "MOOX_METRICS")
	assert.Contains(t, yaml, "moox.trade.target.weight_requested.v1.>")
	strategyACL := eventBusACLBlock(yaml, "strategy-eventbus")
	assert.NotContains(t, strategyACL, "$JS.API.>")
	assert.Contains(t, strategyACL, `subscribe: {allow: ["_INBOX.>", "moox.storage.view.factor_period.ready.v1.>"]}`)
	tradeACL := eventBusACLBlock(yaml, "trade-eventbus")
	assert.Contains(t, tradeACL, "$JS.API.CONSUMER.CREATE.MOOX_TRADE.trade_target_weight_v1")
	assert.Contains(t, tradeACL, "$JS.API.CONSUMER.INFO.*.trade_target_weight_v1")
	assert.Contains(t, tradeACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_TRADE.trade_target_weight_v1")
	assert.Contains(t, tradeACL, "$JS.ACK.MOOX_TRADE.trade_target_weight_v1.>")
	storageACL := eventBusACLBlock(yaml, "storage-eventbus")
	for _, subject := range []string{
		"moox.storage.dataset.rows.upserted.v2.>",
		"moox.storage.dataset.period.collected.v1.>",
		"moox.storage.view.source_period.ready.v1.>",
		"moox.storage.dataset.factor_period.computed.v1.>",
		"moox.storage.view.factor_period.ready.v1.>",
		"moox.storage.dataset.sync_point.v1.>",
	} {
		assert.Contains(t, storageACL, subject)
	}
	assert.NotContains(t, storageACL, "moox.dlq.")
	assert.NotContains(t, storageACL, "moox.storage.rows_committed")
	for _, durable := range []string{"storage_view_kline", "storage_view_factor", "storage_view_metrics", "storage_view_misc"} {
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
	archiveACL := eventBusACLBlock(yaml, "archive-eventbus")
	assert.Contains(t, archiveACL, "password: j")
	assert.Contains(t, archiveACL, "$JS.API.CONSUMER.INFO.*.moox_archive_kline_v2")
	assert.Contains(t, archiveACL, "$JS.API.CONSUMER.CREATE.MOOX_STORAGE.moox_archive_kline_v2")
	assert.Contains(t, archiveACL, "$JS.API.CONSUMER.DURABLE.CREATE.MOOX_STORAGE.moox_archive_kline_v2")
	assert.Contains(t, archiveACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_STORAGE.moox_archive_kline_v2")
	assert.Contains(t, archiveACL, "$JS.ACK.MOOX_STORAGE.moox_archive_kline_v2.>")
	assert.NotContains(t, archiveACL, "moox_archive_kline_"+"v1")
	assert.Contains(t, archiveACL, `subscribe: {allow: ["_INBOX.>"]}`)
	assert.NotContains(t, aclLine(archiveACL, "subscribe:"), "$JS.API")
	assert.NotContains(t, aclLine(archiveACL, "subscribe:"), "$JS.ACK")
	cloudnodeACL := eventBusACLBlock(yaml, "cloudnode-eventbus")
	assert.Contains(t, cloudnodeACL, "$JS.API.CONSUMER.INFO.MOOX_CLOUDNODE_EXEC.>")
	workerACL := eventBusACLBlock(yaml, "cloudnode-worker")
	assert.Contains(t, workerACL, "moox.observability.metrics.snapshot.reported.v1.>")
	assert.Contains(t, workerACL, "moox.observability.health.check.reported.v1.>")
	assert.NotContains(t, workerACL, "moox.observability.host.snapshot")
	assert.Contains(t, cloudnodeACL, "$JS.API.CONSUMER.CREATE.MOOX_CLOUDNODE_EXEC.>")
	assert.Contains(t, cloudnodeACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_CLOUDNODE_EXEC.>")
	assert.Contains(t, cloudnodeACL, "$JS.ACK.MOOX_CLOUDNODE_EXEC.>")
	assert.Contains(t, cloudnodeACL, "$JS.API.STREAM.INFO.KV_MOOX_CLOUDNODE_JOB_ACTIVE")
	assert.Contains(t, cloudnodeACL, "$JS.API.STREAM.MSG.GET.KV_MOOX_CLOUDNODE_JOB_ACTIVE")
	assert.Contains(t, cloudnodeACL, "$JS.API.DIRECT.GET.KV_MOOX_CLOUDNODE_JOB_ACTIVE.>")
	assert.Contains(t, cloudnodeACL, "$JS.API.CONSUMER.CREATE.KV_MOOX_CLOUDNODE_JOB_ACTIVE.>")
	assert.Contains(t, cloudnodeACL, "$JS.API.CONSUMER.DELETE.KV_MOOX_CLOUDNODE_JOB_ACTIVE.>")
	assert.Contains(t, cloudnodeACL, "$KV.MOOX_CLOUDNODE_JOB_ACTIVE.>")
	assert.NotContains(t, cloudnodeACL, "$JS.API.>")
	assert.NotContains(t, cloudnodeACL, "$JS.ACK.>")
	assert.NotContains(t, cloudnodeACL, `subscribe: {allow: ["$JS.API`)
	assert.NotContains(t, cloudnodeACL, `subscribe: {allow: ["$JS.ACK`)
	assert.Contains(t, cloudnodeACL, `subscribe: {allow: ["_INBOX.>"]}`)
	assert.Equal(t, `publish: {allow: ["moox.observability.metrics.snapshot.reported.v1.>", "moox.observability.health.check.reported.v1.>", "$JS.API.CONSUMER.INFO.MOOX_CLOUDNODE_EXEC.>", "$JS.API.CONSUMER.MSG.NEXT.MOOX_CLOUDNODE_EXEC.>", "$JS.ACK.MOOX_CLOUDNODE_EXEC.>"]}`, aclLine(workerACL, "publish:"))
	assert.Equal(t, `subscribe: {allow: ["_INBOX.>"]}`, aclLine(workerACL, "subscribe:"))
	publisherACL := eventBusACLBlock(yaml, "market-fetch-publisher")
	assert.Contains(t, publisherACL, "moox.market.fetch.batch.completed.v1.>")
	assert.NotContains(t, publisherACL, "$JS.API.CONSUMER")
	consumerACL := eventBusACLBlock(yaml, "collector-market-fetch-consumer")
	assert.Contains(t, consumerACL, "$JS.API.CONSUMER.INFO.*.*")
	assert.Contains(t, consumerACL, "$JS.API.CONSUMER.CREATE.MOOX_MARKET_FETCH.*")
	assert.Contains(t, consumerACL, "$JS.ACK.MOOX_MARKET_FETCH.*.>")
	assert.Contains(t, consumerACL, "$JS.API.CONSUMER.CREATE.MOOX_STORAGE.>")
	assert.Contains(t, consumerACL, "$JS.ACK.MOOX_STORAGE.>")
	assert.NotContains(t, consumerACL, "$JS.API.>")
	factorACL := eventBusACLBlock(yaml, "factor-eventbus")
	assert.Contains(t, factorACL, "$JS.API.CONSUMER.INFO.*.factor_calc")
	assert.Contains(t, factorACL, "$JS.API.CONSUMER.INFO.*.factor_view_ready_v1")
	assert.Contains(t, factorACL, "$JS.API.CONSUMER.CREATE.MOOX_STORAGE.factor_view_ready_v1")
	assert.Contains(t, factorACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_STORAGE.factor_view_ready_v1")
	assert.Contains(t, factorACL, "$JS.ACK.MOOX_STORAGE.factor_view_ready_v1.>")
	assert.Contains(t, factorACL, "$JS.API.CONSUMER.CREATE.MOOX_STORAGE.factor_view_ready_e2e")
	assert.Contains(t, factorACL, "$JS.API.CONSUMER.DURABLE.CREATE.MOOX_STORAGE.factor_view_ready_e2e")
	assert.Contains(t, factorACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_STORAGE.factor_view_ready_e2e")
	assert.Contains(t, factorACL, "$JS.ACK.MOOX_STORAGE.factor_view_ready_e2e.>")
	for _, forbidden := range []string{"CONSUMER.CREATE", "CONSUMER.DELETE", "STREAM.NAMES", "$KV.", "moox.cloudnode.job.execution"} {
		assert.NotContains(t, workerACL, forbidden)
	}
	for _, role := range eventBusRoles {
		if role == "eventbus-internal-admin" {
			continue
		}
		acl := eventBusACLBlock(yaml, role)
		assert.NotContains(t, aclLine(acl, "subscribe:"), "$JS.API")
		assert.NotContains(t, aclLine(acl, "subscribe:"), "$JS.ACK")
		if role == "strategy-eventbus" {
			assert.Contains(t, acl, `subscribe: {allow: ["_INBOX.>", "moox.storage.view.factor_period.ready.v1.>"]}`)
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
	archiveRotateArgs := []string{"eventbus-credentials", "rotate", "--db-path", dbPath, "--encryption-key-file", keyPath, "--credential", "archive-eventbus", "--confirm"}
	out.Reset()
	require.NoError(t, runEventBusCredentialsCommand(archiveRotateArgs, &out, &bytes.Buffer{}))
	assert.Contains(t, out.String(), `"rotated":"archive-eventbus"`)
	assert.NotContains(t, out.String(), "archive-secret")

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Table("t_secrets").Where("c_category = ? AND c_provider = ? AND c_is_deleted = 0", "eventbus", "moox_eventbus").Count(&count).Error)
	assert.GreaterOrEqual(t, count, int64(13))
}

func TestEventBusCredentialsReconcilePreservesRoleTokensAndRefreshesACL(t *testing.T) {
	dir := t.TempDir()
	roleFiles := map[string]string{
		"internal-admin.yaml":                  "token: admin-token\n",
		"hostagent-publisher.yaml":             "eventbus_token: host-token\n",
		"metrics-publisher.yaml":               "token: metrics-token\n",
		"monitor-observability.yaml":           "monitor_eventbus_token: monitor-token\n",
		"storage-eventbus.yaml":                "token: storage-token\n",
		"archive-eventbus.yaml":                "token: archive-token\n",
		"cloudnode-eventbus.yaml":              "token: cloud-token\n",
		"cloudnode-worker.yaml":                "token: worker-token\n",
		"market-fetch-publisher.yaml":          "token: market-token\n",
		"collector-market-fetch-consumer.yaml": "token: collector-token\n",
		"factor-eventbus.yaml":                 "token: factor-token\n",
		"strategy-eventbus.yaml":               "token: strategy-token\n",
		"trade-eventbus.yaml":                  "token: trade-token\n",
	}
	for name, content := range roleFiles {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	var out bytes.Buffer
	require.NoError(t, runEventBusCredentialsCommand([]string{"eventbus-credentials", "reconcile", "--output-dir", dir}, &out, &bytes.Buffer{}))
	raw, err := os.ReadFile(filepath.Join(dir, "users.yaml"))
	require.NoError(t, err)
	text := string(raw)
	assert.Contains(t, text, "moox.trade.target.weight_requested.v1.>")
	assert.Contains(t, text, "strategy-token")
	assert.Contains(t, text, "trade-token")
}

func seedEventBusDeployment(t *testing.T, dbPath string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO t_gateway_nodes(c_node_id,c_name,c_public_address,c_status) VALUES (?,?,?,?)`,
		"gateway-node-1", "Gateway", "203.0.113.10", "enabled").Error)
	require.NoError(t, db.Exec(`INSERT INTO t_service_deployments(c_node_id,c_service_name,c_service_kind,c_status,c_extra_config) VALUES (?,?,?,?,?)`,
		"gateway-node-1", "eventbus", "eventbus", "active", `{"nats_url":"tls://203.0.113.10:4222"}`).Error)
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
			} `yaml:"permissions"`
		} `yaml:"users"`
	}
	require.NoError(t, yamlv3.Unmarshal([]byte(usersYAML(tokens)), &parsed))
	users := make([]*natsserver.User, 0, len(parsed.Users))
	for _, item := range parsed.Users {
		users = append(users, &natsserver.User{
			Username: item.Username,
			Password: item.Password,
			Permissions: &natsserver.Permissions{
				Publish: &natsserver.SubjectPermission{
					Allow: item.Permissions.Publish.Allow,
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
		Name: "MOOX_TRADE", Subjects: []string{"moox.trade.target.weight_requested.v1.>"},
	})
	require.NoError(t, err)
	_, err = adminJS.AddStream(&nats.StreamConfig{
		Name: "MOOX_CLOUDNODE_EXEC", Subjects: []string{"moox.cloudnode.job.execution.requested.v1.>"},
	})
	require.NoError(t, err)
	_, err = adminJS.AddStream(&nats.StreamConfig{
		Name: "MOOX_OBSERVABILITY", Subjects: []string{"moox.observability.>"},
	})
	require.NoError(t, err)
	adminKV, err := adminJS.CreateKeyValue(&nats.KeyValueConfig{
		Bucket: "MOOX_CLOUDNODE_JOB_ACTIVE",
	})
	require.NoError(t, err)
	require.NotNil(t, adminKV)

	strategy, err := nats.Connect(
		server.ClientURL(),
		nats.UserInfo("strategy-eventbus", tokens["strategy-eventbus"]),
	)
	require.NoError(t, err)
	t.Cleanup(strategy.Close)
	strategyJS, err := strategy.JetStream()
	require.NoError(t, err)
	_, err = strategyJS.Publish("moox.trade.target.weight_requested.v1.space.binding", []byte("payload"))
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
		FilterSubject: "moox.trade.target.weight_requested.v1.>",
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
			"moox.observability.host.snapshot.reported.v1.moox_system.host-1",
		},
		"metrics-publisher": {
			"moox.observability.metrics.snapshot.reported.v1.moox_system.trade/instance-1",
			"moox.observability.health.check.reported.v1.moox_system.trade/instance-1",
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
		FilterSubject: "moox.observability.>",
		AckWait:       time.Second, MaxDeliver: 3, MaxAckPending: 8,
		FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
	})
	require.NoError(t, err)
	require.NoError(t, monitorConsumer.Close())

	cloudCtx, cloudCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cloudCancel()
	cloudnode, err := jetstream.Connect(cloudCtx, jetstream.Config{
		URLs: []string{server.ClientURL()}, Name: "cloudnode-auth-e2e",
		Username: "cloudnode-eventbus", Password: tokens["cloudnode-eventbus"],
	})
	require.NoError(t, err)
	defer cloudnode.Close()
	cloudConsumer, err := cloudnode.NewConsumer(cloudCtx, jetstream.ConsumerConfig{
		Stream: "MOOX_CLOUDNODE_EXEC", Durable: "cn_exec_auth_e2e",
		FilterSubject: "moox.cloudnode.job.execution.requested.v1.space.package.>",
		AckWait:       time.Second, MaxDeliver: 3, MaxAckPending: 8,
		FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
	})
	require.NoError(t, err)
	defer cloudConsumer.Close()
	cloudRaw, err := nats.Connect(
		server.ClientURL(),
		nats.UserInfo("cloudnode-eventbus", tokens["cloudnode-eventbus"]),
	)
	require.NoError(t, err)
	defer cloudRaw.Close()
	cloudJS, err := cloudRaw.JetStream()
	require.NoError(t, err)
	cloudKV, err := cloudJS.KeyValue("MOOX_CLOUDNODE_JOB_ACTIVE")
	require.NoError(t, err)
	_, err = cloudKV.Put("job.space.item", []byte("pending"))
	require.NoError(t, err)
	cloudEntry, err := cloudKV.Get("job.space.item")
	require.NoError(t, err)
	require.Equal(t, "pending", string(cloudEntry.Value()))
	message := nats.NewMsg("moox.cloudnode.job.execution.requested.v1.space.package.job")
	message.Header.Set(nats.MsgIdHdr, "worker-auth-e2e")
	message.Data = []byte("payload")
	_, err = cloudJS.PublishMsg(message)
	require.NoError(t, err)

	worker, err := jetstream.Connect(cloudCtx, jetstream.Config{
		URLs: []string{server.ClientURL()}, Name: "cloudnode-worker-auth-e2e",
		Username: "cloudnode-worker", Password: tokens["cloudnode-worker"],
	})
	require.NoError(t, err)
	defer worker.Close()
	workerConsumer, err := worker.BindConsumer(cloudCtx, jetstream.ConsumerConfig{
		Stream: "MOOX_CLOUDNODE_EXEC", Durable: "cn_exec_auth_e2e",
		FilterSubject: "moox.cloudnode.job.execution.requested.v1.space.package.>",
		FetchMaxWait:  time.Second,
	})
	require.NoError(t, err)
	defer workerConsumer.Close()
	deliveries, err := workerConsumer.Fetch(cloudCtx, 1)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	require.NoError(t, deliveries[0].Ack(cloudCtx))

	requirePublishPermissionViolation(
		t, server.ClientURL(), "cloudnode-worker", tokens["cloudnode-worker"],
		"$JS.API.CONSUMER.CREATE.MOOX_CLOUDNODE_EXEC.cn_exec_forbidden",
		"CloudNode worker 不得创建 Consumer",
	)
	requirePublishPermissionViolation(
		t, server.ClientURL(), "cloudnode-worker", tokens["cloudnode-worker"],
		"moox.cloudnode.job.execution.requested.v1.space.package.job",
		"CloudNode worker 不得发布执行事件",
	)

	_, err = cloudJS.ConsumerInfo("MOOX_TRADE", "trade_target_weight_v1")
	require.NoError(t, err, "CloudNode 需要只读 Consumer 元数据完成跨 Stream 命名检查")
	_, err = cloudJS.AddConsumer("MOOX_TRADE", &nats.ConsumerConfig{
		Name: "cn_exec_escape", Durable: "cn_exec_escape",
		FilterSubject: "moox.trade.target.weight_requested.v1.>",
		AckPolicy:     nats.AckExplicitPolicy,
	})
	require.Error(t, err, "CloudNode 不得在所属 Stream 之外创建 Consumer")

	requirePublishPermissionViolation(
		t, server.ClientURL(), "cloudnode-eventbus", tokens["cloudnode-eventbus"],
		"$JS.API.CONSUMER.MSG.NEXT.MOOX_TRADE.trade_target_weight_v1",
		"CloudNode 不得拉取其他 Stream 的 Consumer",
	)
	requirePublishPermissionViolation(
		t, server.ClientURL(), "cloudnode-eventbus", tokens["cloudnode-eventbus"],
		"$JS.ACK.MOOX_TRADE.trade_target_weight_v1.1.1.1.1.1",
		"CloudNode 不得 ACK 其他 Stream 的 Consumer",
	)
}

func requirePublishPermissionViolation(t *testing.T, url, username, password, subject, message string) {
	t.Helper()
	permissionErrors := make(chan error, 1)
	conn, err := nats.Connect(
		url,
		nats.UserInfo(username, password),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			permissionErrors <- err
		}),
	)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.Publish(subject, []byte("1")))
	require.NoError(t, conn.Flush())
	select {
	case err := <-permissionErrors:
		require.ErrorContains(t, err, "Permissions Violation", message)
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}
