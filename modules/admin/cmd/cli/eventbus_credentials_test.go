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
	args := []string{"eventbus-credentials", "ensure", "--db-path", dbPath, "--encryption-key-file", keyPath, "--public-ip", "203.0.113.10"}
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
	if count != 13 {
		t.Fatalf("eventbus records=%d, want 13", count)
	}
}

func TestEventBusCredentialsExportAndRotate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "admin.db")
	keyPath := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(keyPath, []byte("test-encryption-key-for-eventbus"), 0o600))
	require.NoError(t, applySchema(dbPath, adminschema.AdminSQL()))

	ensureArgs := []string{"eventbus-credentials", "ensure", "--db-path", dbPath, "--encryption-key-file", keyPath, "--public-ip", "203.0.113.10"}
	var out bytes.Buffer
	require.NoError(t, runEventBusCredentialsCommand(ensureArgs, &out, &bytes.Buffer{}))

	exportDir := filepath.Join(dir, "out")
	exportArgs := []string{"eventbus-credentials", "export", "--db-path", dbPath, "--encryption-key-file", keyPath, "--output-dir", exportDir, "--public-ip", "203.0.113.10"}
	out.Reset()
	require.NoError(t, runEventBusCredentialsCommand(exportArgs, &out, &bytes.Buffer{}))
	assert.FileExists(t, filepath.Join(exportDir, "users.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "ca.pem"))
	assert.FileExists(t, filepath.Join(exportDir, "server.pem"))
	assert.FileExists(t, filepath.Join(exportDir, "hostagent-publisher.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "metrics-publisher.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "monitor-eventbus.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "monitor-metrics-consumer.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "archive-eventbus.yaml"))
	assert.FileExists(t, filepath.Join(exportDir, "trade-eventbus.yaml"))
	strategyCredential := filepath.Join(exportDir, "strategy-eventbus.yaml")
	assert.FileExists(t, strategyCredential)
	info, err := os.Stat(strategyCredential)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	yaml := usersYAML(map[string]string{
		"eventbus-internal-admin":      "a",
		"hostagent-publisher":          "b",
		"metrics-publisher":            "c",
		"monitor-hostmetrics-consumer": "d",
		"monitor-metrics-consumer":     "e",
		"storage-eventbus":             "f",
		"cloudnode-eventbus":           "g",
		"factor-eventbus":              "h",
		"strategy-eventbus":            "i",
		"archive-eventbus":             "j",
		"trade-eventbus":               "k",
	})
	assert.Contains(t, yaml, "eventbus-internal-admin")
	assert.Contains(t, yaml, "factor-eventbus")
	assert.Contains(t, yaml, "moox.metrics.snapshot.reported.v1.>")
	internalAdminACL := eventBusACLBlock(yaml, "eventbus-internal-admin")
	assert.Equal(t, `subscribe: {allow: ["_INBOX.>", "$JS.EVENT.ADVISORY.API"]}`, aclLine(internalAdminACL, "subscribe:"))
	assert.NotContains(t, aclLine(internalAdminACL, "subscribe:"), "$JS.EVENT.ADVISORY.API.>")
	for role, password := range map[string]string{
		"eventbus-internal-admin":      "a",
		"hostagent-publisher":          "b",
		"metrics-publisher":            "c",
		"monitor-hostmetrics-consumer": "d",
		"monitor-metrics-consumer":     "e",
		"storage-eventbus":             "f",
		"cloudnode-eventbus":           "g",
		"factor-eventbus":              "h",
		"archive-eventbus":             "j",
		"strategy-eventbus":            "i",
		"trade-eventbus":               "k",
	} {
		assert.Contains(t, eventBusACLBlock(yaml, role), "password: "+password)
	}
	metricsPublisherACL := eventBusACLBlock(yaml, "metrics-publisher")
	assert.NotContains(t, metricsPublisherACL, "$JS.API.>")
	assert.Equal(t, `subscribe: {allow: ["_INBOX.>"]}`, aclLine(metricsPublisherACL, "subscribe:"))
	assert.Contains(t, yaml, "moox.trade.rebalance.requested.v1.>")
	strategyACL := eventBusACLBlock(yaml, "strategy-eventbus")
	assert.NotContains(t, strategyACL, "$JS.API.>")
	assert.Contains(t, strategyACL, `subscribe: {allow: ["_INBOX.>"]}`)
	tradeACL := eventBusACLBlock(yaml, "trade-eventbus")
	assert.Contains(t, tradeACL, "$JS.API.CONSUMER.CREATE.MOOX_TRADE.trade_rebalance_v1")
	assert.Contains(t, tradeACL, "$JS.API.CONSUMER.INFO.*.trade_rebalance_v1")
	assert.Contains(t, tradeACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_TRADE.trade_rebalance_v1")
	assert.Contains(t, tradeACL, "$JS.ACK.MOOX_TRADE.trade_rebalance_v1.>")
	storageACL := eventBusACLBlock(yaml, "storage-eventbus")
	assert.Contains(t, storageACL, "moox.storage.dataset.rows.upserted.v1.>")
	assert.NotContains(t, storageACL, "moox.dlq.")
	assert.NotContains(t, storageACL, "moox.storage.rows_committed")
	assert.Contains(t, storageACL, "$JS.API.CONSUMER.INFO.*.storage_view")
	assert.Contains(t, storageACL, "$JS.API.CONSUMER.CREATE.MOOX_STORAGE.storage_view")
	assert.Contains(t, storageACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_STORAGE.storage_view")
	assert.Contains(t, storageACL, "$JS.ACK.MOOX_STORAGE.storage_view.>")
	assert.Contains(t, storageACL, `subscribe: {allow: ["_INBOX.>"]}`)
	assert.NotContains(t, aclLine(storageACL, "subscribe:"), "$JS.API")
	assert.NotContains(t, aclLine(storageACL, "subscribe:"), "$JS.ACK")
	assert.NotContains(t, storageACL, "storage_view_rows_committed_v1")
	archiveACL := eventBusACLBlock(yaml, "archive-eventbus")
	assert.Contains(t, archiveACL, "password: j")
	assert.Contains(t, archiveACL, "$JS.API.CONSUMER.INFO.*.moox_archive_kline_v1")
	assert.Contains(t, archiveACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_STORAGE.moox_archive_kline_v1")
	assert.Contains(t, archiveACL, "$JS.ACK.MOOX_STORAGE.moox_archive_kline_v1.>")
	assert.Contains(t, archiveACL, `subscribe: {allow: ["_INBOX.>"]}`)
	assert.NotContains(t, archiveACL, "moox.storage.dataset.rows.upserted.v1")
	assert.NotContains(t, aclLine(archiveACL, "subscribe:"), "$JS.API")
	assert.NotContains(t, aclLine(archiveACL, "subscribe:"), "$JS.ACK")
	cloudnodeACL := eventBusACLBlock(yaml, "cloudnode-eventbus")
	assert.Contains(t, cloudnodeACL, "$JS.API.CONSUMER.INFO.MOOX_CLOUDNODE_EXEC.>")
	assert.Contains(t, cloudnodeACL, "$JS.API.CONSUMER.CREATE.MOOX_CLOUDNODE_EXEC.>")
	assert.Contains(t, cloudnodeACL, "$JS.API.CONSUMER.MSG.NEXT.MOOX_CLOUDNODE_EXEC.>")
	assert.Contains(t, cloudnodeACL, "$JS.ACK.MOOX_CLOUDNODE_EXEC.>")
	assert.Contains(t, cloudnodeACL, "$JS.API.STREAM.INFO.KV_MOOX_CLOUDNODE_JOB_ACTIVE")
	assert.Contains(t, cloudnodeACL, "$JS.API.STREAM.MSG.GET.KV_MOOX_CLOUDNODE_JOB_ACTIVE")
	assert.Contains(t, cloudnodeACL, "$JS.API.CONSUMER.CREATE.KV_MOOX_CLOUDNODE_JOB_ACTIVE.>")
	assert.Contains(t, cloudnodeACL, "$JS.API.CONSUMER.DELETE.KV_MOOX_CLOUDNODE_JOB_ACTIVE.>")
	assert.Contains(t, cloudnodeACL, "$KV.MOOX_CLOUDNODE_JOB_ACTIVE.>")
	assert.NotContains(t, cloudnodeACL, "$JS.API.>")
	assert.NotContains(t, cloudnodeACL, "$JS.ACK.>")
	assert.NotContains(t, cloudnodeACL, `subscribe: {allow: ["$JS.API`)
	assert.NotContains(t, cloudnodeACL, `subscribe: {allow: ["$JS.ACK`)
	assert.Contains(t, cloudnodeACL, `subscribe: {allow: ["_INBOX.>"]}`)
	for _, role := range eventBusRoles {
		if role == "eventbus-internal-admin" {
			continue
		}
		acl := eventBusACLBlock(yaml, role)
		assert.NotContains(t, aclLine(acl, "subscribe:"), "$JS.API")
		assert.NotContains(t, aclLine(acl, "subscribe:"), "$JS.ACK")
		assert.Contains(t, acl, `subscribe: {allow: ["_INBOX.>"]}`)
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
		Name: "MOOX_TRADE", Subjects: []string{"moox.trade.rebalance.requested.v1.>"},
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
	_, err = strategyJS.Publish("moox.trade.rebalance.requested.v1.space.binding", []byte("payload"))
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
		Stream: "MOOX_TRADE", Durable: "trade_rebalance_v1",
		FilterSubject: "moox.trade.rebalance.requested.v1.>",
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
}
