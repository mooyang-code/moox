package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"gorm.io/gorm"
	trpc "trpc.group/trpc-go/trpc-go"
)

var eventBusRoles = []string{"eventbus-internal-admin", "hostagent-publisher", "metrics-publisher", "monitor-hostmetrics-consumer", "monitor-metrics-consumer", "storage-eventbus", "archive-eventbus", "cloudnode-eventbus", "cloudnode-worker", "factor-eventbus", "strategy-eventbus", "trade-eventbus"}
var eventBusKeys = map[string]string{"eventbus-internal-admin": "eventbus_internal_admin", "hostagent-publisher": "eventbus_hostagent_publisher", "metrics-publisher": "eventbus_metrics_publisher", "monitor-hostmetrics-consumer": "eventbus_monitor_consumer", "monitor-metrics-consumer": "eventbus_monitor_metrics_consumer", "storage-eventbus": "eventbus_storage", "archive-eventbus": "eventbus_archive", "cloudnode-eventbus": "eventbus_cloudnode", "cloudnode-worker": "eventbus_cloudnode_worker", "factor-eventbus": "eventbus_factor", "strategy-eventbus": "eventbus_strategy", "trade-eventbus": "eventbus_trade"}

type eventbusBundle struct {
	CA        string    `json:"ca"`
	Cert      string    `json:"cert"`
	Key       string    `json:"key"`
	NATSURL   string    `json:"nats_url"`
	CreatedAt time.Time `json:"created_at"`
}

func isEventBusCredentialsCommand(args []string) bool {
	return len(args) > 1 && args[1] == "eventbus-credentials"
}
func runEventBusCredentialsCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return errors.New("expected eventbus-credentials subcommand")
	}
	fs := flag.NewFlagSet("eventbus-credentials", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath, keyFile, nodeID, outputDir, credential := "./data/admin.db", "", "", "", ""
	confirm := false
	fs.StringVar(&dbPath, "db-path", dbPath, "SQLite database path")
	fs.StringVar(&keyFile, "encryption-key-file", "", "0600 encryption key file")
	fs.StringVar(&nodeID, "node-id", "", "gateway node ID containing the EventBus deployment")
	fs.StringVar(&outputDir, "output-dir", "", "credential output directory")
	fs.StringVar(&credential, "credential", "", "role token to rotate")
	fs.BoolVar(&confirm, "confirm", false, "confirm rotation and immediate invalidation")
	sub := args[1]
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if err := loadCLIKey(dbPath, keyFile); err != nil {
		return err
	}
	db, err := openAdminCLIDB(dbPath)
	if err != nil {
		return err
	}
	defer closeAdminCLIDB(db)
	secretDAO := dao.NewSecretDAO(db)
	switch sub {
	case "ensure":
		natsURL, err := eventBusNATSURL(db, nodeID)
		if err != nil {
			return err
		}
		return ensureEventBus(secretDAO, natsURL, stdout)
	case "export":
		if outputDir == "" {
			return errors.New("--output-dir is required")
		}
		natsURL, err := eventBusNATSURL(db, nodeID)
		if err != nil {
			return err
		}
		return exportEventBus(secretDAO, outputDir, natsURL, stdout)
	case "rotate":
		return rotateEventBus(secretDAO, credential, confirm, stdout)
	default:
		return fmt.Errorf("unknown eventbus-credentials subcommand %q", sub)
	}
}

func eventBusNATSURL(db *gorm.DB, nodeID string) (string, error) {
	if nodeID == "" || nodeID != strings.TrimSpace(nodeID) {
		return "", errors.New("--node-id is required and must not contain surrounding whitespace")
	}
	var deployment sysdeploy.Deployment
	result := db.Where("c_node_id = ? AND c_service_name = ? AND c_status = ?", nodeID, "eventbus", "active").Find(&deployment)
	if result.Error != nil {
		return "", fmt.Errorf("query EventBus service deployment: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return "", fmt.Errorf("active EventBus service deployment for node %q not found", nodeID)
	}
	var extra map[string]any
	if err := json.Unmarshal([]byte(deployment.ExtraConfig), &extra); err != nil {
		return "", fmt.Errorf("decode EventBus extra_config: %w", err)
	}
	raw, _ := extra["nats_url"].(string)
	if _, err := validateEventBusNATSURL(raw); err != nil {
		return "", fmt.Errorf("EventBus service deployment nats_url: %w", err)
	}
	return raw, nil
}

func openAdminCLIDB(path string) (*gorm.DB, error) {
	if path == "" {
		return nil, errors.New("db path is required")
	}
	return gorm.Open(sqlite.Open(path), &gorm.Config{})
}
func closeAdminCLIDB(db *gorm.DB) {
	if db != nil {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
}
func loadCLIKey(dbPath, keyFile string) error {
	if keyFile == "" {
		return errors.New("--encryption-key-file is required")
	}
	info, err := os.Stat(dbPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Chmod(keyFile, 0o600); err != nil && !os.IsNotExist(err) {
		return err
	}
	raw, err := os.ReadFile(keyFile)
	if os.IsNotExist(err) {
		if !os.IsNotExist(err) {
			return err
		}
		if info != nil {
			return errors.New("admin database exists but encryption key is missing")
		}
		keyBytes := make([]byte, 32)
		if _, err := rand.Read(keyBytes); err != nil {
			return err
		}
		key := []byte(base64.RawStdEncoding.EncodeToString(keyBytes))
		if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(keyFile, key, 0o600); err != nil {
			return err
		}
		raw = key
	} else if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return errors.New("encryption key file is empty")
	}
	return os.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", strings.TrimSpace(string(raw)))
}

func ensureEventBus(d *dao.SecretDAO, natsURL string, out io.Writer) error {
	ctx := trpc.BackgroundContext()
	for _, role := range eventBusRoles {
		key := eventBusKeys[role]
		if _, err := ensureToken(ctx, d, key, role); err != nil {
			return err
		}
	}
	existing, _ := listEventbus(d, ctx)
	if _, ok := existing["eventbus_tls_ca"]; !ok {
		bundle, err := makeTLSBundle(natsURL)
		if err != nil {
			return err
		}
		extra, _ := json.Marshal(map[string]string{"nats_url": natsURL})
		if err := d.Create(ctx, &model.Secret{SpaceID: "moox_system", SecretID: uuid.New().String(), Name: "EventBus TLS CA", Description: "private EventBus CA bundle", Category: "eventbus", Provider: "moox_eventbus", SecretType: "certificate", KeyID: "eventbus_tls_ca", SecretValue: bundle.CA, ExtraConfig: string(extra)}); err != nil {
			return err
		}
		serverValue, _ := json.Marshal(map[string]string{"cert": bundle.Cert, "key": bundle.Key})
		if err := d.Create(ctx, &model.Secret{SpaceID: "moox_system", SecretID: uuid.New().String(), Name: "EventBus TLS server", Description: "private EventBus server bundle", Category: "eventbus", Provider: "moox_eventbus", SecretType: "certificate", KeyID: "eventbus_tls_server", SecretValue: string(serverValue), ExtraConfig: string(extra)}); err != nil {
			return err
		}
	} else {
		var extra map[string]string
		if err := json.Unmarshal([]byte(existing["eventbus_tls_ca"].ExtraConfig), &extra); err != nil || extra["nats_url"] != natsURL {
			return errors.New("EventBus TLS certificate host differs from the service directory; use --reset-data to rebuild")
		}
	}
	return writeJSON(out, map[string]any{"status": "ok", "roles": eventBusRoles, "tls": true})
}
func ensureToken(ctx context.Context, d *dao.SecretDAO, key, role string) (string, error) {
	records, err := listEventbus(d, ctx)
	if err != nil {
		return "", err
	}
	if existing, ok := records[key]; ok {
		return existing.SecretValue, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	err = d.Create(ctx, &model.Secret{SpaceID: "moox_system", SecretID: uuid.New().String(), Name: "EventBus " + role, Description: "permanent EventBus role token", Category: "eventbus", Provider: "moox_eventbus", SecretType: "token", KeyID: key, SecretValue: value, ExtraConfig: `{}`})
	return value, err
}
func listEventbus(d *dao.SecretDAO, ctx context.Context) (map[string]model.Secret, error) {
	rows, _, err := d.List(ctx, 0, 100, &dao.SecretFilters{Category: "eventbus", Provider: "moox_eventbus", Status: "active"})
	if err != nil {
		return nil, err
	}
	out := make(map[string]model.Secret, len(rows))
	for _, row := range rows {
		out[row.KeyID] = row
	}
	return out, nil
}

func exportEventBus(d *dao.SecretDAO, dir, natsURL string, out io.Writer) error {
	ctx := trpc.BackgroundContext()
	rows, err := listEventbus(d, ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tokens := map[string]string{}
	for _, role := range eventBusRoles {
		row, ok := rows[eventBusKeys[role]]
		if !ok {
			return fmt.Errorf("missing EventBus role %s; run ensure first", role)
		}
		tokens[role] = row.SecretValue
	}
	users := usersYAML(tokens)
	if err := atomicSecretFile(filepath.Join(dir, "users.yaml"), []byte(users)); err != nil {
		return err
	}
	roleFiles := map[string]string{
		"eventbus-internal-admin":      "internal-admin.yaml",
		"hostagent-publisher":          "hostagent-publisher.yaml",
		"metrics-publisher":            "metrics-publisher.yaml",
		"monitor-hostmetrics-consumer": "monitor-eventbus.yaml",
		"monitor-metrics-consumer":     "monitor-metrics-consumer.yaml",
		"storage-eventbus":             "storage-eventbus.yaml",
		"archive-eventbus":             "archive-eventbus.yaml",
		"cloudnode-eventbus":           "cloudnode-eventbus.yaml",
		"cloudnode-worker":             "cloudnode-worker.yaml",
		"factor-eventbus":              "factor-eventbus.yaml",
		"strategy-eventbus":            "strategy-eventbus.yaml",
		"trade-eventbus":               "trade-eventbus.yaml",
	}
	for role, name := range roleFiles {
		field := "token"
		if role == "hostagent-publisher" {
			field = "eventbus_token"
		}
		if role == "monitor-hostmetrics-consumer" || role == "monitor-metrics-consumer" {
			field = "monitor_eventbus_token"
		}
		content := fmt.Sprintf("version: 1\nurls:\n  - %s\nusername: %s\n%s: %s\nca_file: ca.pem\n", natsURL, role, field, tokens[role])
		if err := atomicSecretFile(filepath.Join(dir, name), []byte(content)); err != nil {
			return err
		}
	}
	ca, ok := rows["eventbus_tls_ca"]
	if !ok {
		return errors.New("missing EventBus TLS CA; run ensure first")
	}
	server, ok := rows["eventbus_tls_server"]
	if !ok {
		return errors.New("missing EventBus TLS server; run ensure first")
	}
	if err := atomicSecretFile(filepath.Join(dir, "ca.pem"), []byte(ca.SecretValue)); err != nil {
		return err
	}
	var serverParts map[string]string
	if err := json.Unmarshal([]byte(server.SecretValue), &serverParts); err != nil {
		return fmt.Errorf("decode EventBus TLS server bundle: %w", err)
	}
	if serverParts["cert"] != "" && serverParts["key"] != "" {
		if err := atomicSecretFile(filepath.Join(dir, "server.pem"), []byte(serverParts["cert"])); err != nil {
			return err
		}
		if err := atomicSecretFile(filepath.Join(dir, "server-key.pem"), []byte(serverParts["key"])); err != nil {
			return err
		}
	}
	return writeJSON(out, map[string]any{"status": "ok", "output_dir": dir, "roles": eventBusRoles})
}
func usersYAML(tokens map[string]string) string { // ACLs are deliberately subject-scoped; publisher roles never receive broad JetStream API access.
	return fmt.Sprintf("users:\n"+
		"  - username: eventbus-internal-admin\n    password: %s\n    permissions:\n      publish: {allow: [\"$JS.API.>\"]}\n      subscribe: {allow: [\"_INBOX.>\", \"$JS.EVENT.ADVISORY.API\"]}\n"+
		"  - username: hostagent-publisher\n    password: %s\n    permissions:\n      publish: {allow: [\"moox.metrics.host.reported.v1.>\"]}\n      subscribe: {allow: [\"_INBOX.>\"]}\n"+
		"  - username: metrics-publisher\n    password: %s\n    permissions:\n      publish: {allow: [\"moox.metrics.snapshot.reported.v1.>\"]}\n      subscribe: {allow: [\"_INBOX.>\"]}\n"+
		"  - username: monitor-hostmetrics-consumer\n    password: %s\n    permissions:\n      publish: {allow: [\"$JS.API.STREAM.NAMES\", \"$JS.API.CONSUMER.INFO.*.monitor_hostmetrics_ingest_v1\", \"$JS.API.CONSUMER.CREATE.MOOX_METRICS.monitor_hostmetrics_ingest_v1\", \"$JS.API.CONSUMER.CREATE.MOOX_METRICS.monitor_hostmetrics_ingest_v1.>\", \"$JS.API.CONSUMER.DURABLE.CREATE.MOOX_METRICS.monitor_hostmetrics_ingest_v1\", \"$JS.API.CONSUMER.MSG.NEXT.MOOX_METRICS.monitor_hostmetrics_ingest_v1\", \"$JS.ACK.MOOX_METRICS.monitor_hostmetrics_ingest_v1.>\"]}\n      subscribe: {allow: [\"_INBOX.>\"]}\n"+
		"  - username: monitor-metrics-consumer\n    password: %s\n    permissions:\n      publish: {allow: [\"$JS.API.STREAM.NAMES\", \"$JS.API.CONSUMER.INFO.*.monitor_metrics_ingest_v1\", \"$JS.API.CONSUMER.CREATE.MOOX_METRICS.monitor_metrics_ingest_v1\", \"$JS.API.CONSUMER.CREATE.MOOX_METRICS.monitor_metrics_ingest_v1.>\", \"$JS.API.CONSUMER.DURABLE.CREATE.MOOX_METRICS.monitor_metrics_ingest_v1\", \"$JS.API.CONSUMER.MSG.NEXT.MOOX_METRICS.monitor_metrics_ingest_v1\", \"$JS.ACK.MOOX_METRICS.monitor_metrics_ingest_v1.>\"]}\n      subscribe: {allow: [\"_INBOX.>\"]}\n"+
		"  - username: storage-eventbus\n    password: %s\n    permissions:\n      publish: {allow: [\"moox.storage.dataset.rows.upserted.v1.>\", \"$JS.API.STREAM.NAMES\", \"$JS.API.CONSUMER.INFO.*.storage_view\", \"$JS.API.CONSUMER.CREATE.MOOX_STORAGE.storage_view\", \"$JS.API.CONSUMER.CREATE.MOOX_STORAGE.storage_view.>\", \"$JS.API.CONSUMER.DURABLE.CREATE.MOOX_STORAGE.storage_view\", \"$JS.API.CONSUMER.MSG.NEXT.MOOX_STORAGE.storage_view\", \"$JS.ACK.MOOX_STORAGE.storage_view.>\"]}\n      subscribe: {allow: [\"_INBOX.>\"]}\n"+
		"  - username: archive-eventbus\n    password: %s\n    permissions:\n      publish: {allow: [\"$JS.API.STREAM.NAMES\", \"$JS.API.CONSUMER.INFO.*.moox_archive_kline_v1\", \"$JS.API.CONSUMER.CREATE.MOOX_STORAGE.moox_archive_kline_v1\", \"$JS.API.CONSUMER.CREATE.MOOX_STORAGE.moox_archive_kline_v1.>\", \"$JS.API.CONSUMER.DURABLE.CREATE.MOOX_STORAGE.moox_archive_kline_v1\", \"$JS.API.CONSUMER.MSG.NEXT.MOOX_STORAGE.moox_archive_kline_v1\", \"$JS.ACK.MOOX_STORAGE.moox_archive_kline_v1.>\"]}\n      subscribe: {allow: [\"_INBOX.>\"]}\n"+
		"  - username: cloudnode-eventbus\n    password: %s\n    permissions:\n      publish: {allow: [\"moox.cloudnode.job.execution.requested.v1.>\", \"$JS.API.STREAM.NAMES\", \"$JS.API.CONSUMER.INFO.*.>\", \"$JS.API.CONSUMER.INFO.MOOX_CLOUDNODE_EXEC.>\", \"$JS.API.CONSUMER.CREATE.MOOX_CLOUDNODE_EXEC.>\", \"$JS.API.CONSUMER.MSG.NEXT.MOOX_CLOUDNODE_EXEC.>\", \"$JS.ACK.MOOX_CLOUDNODE_EXEC.>\", \"$JS.API.STREAM.INFO.KV_MOOX_CLOUDNODE_JOB_ACTIVE\", \"$JS.API.STREAM.MSG.GET.KV_MOOX_CLOUDNODE_JOB_ACTIVE\", \"$JS.API.CONSUMER.CREATE.KV_MOOX_CLOUDNODE_JOB_ACTIVE.>\", \"$JS.API.CONSUMER.DELETE.KV_MOOX_CLOUDNODE_JOB_ACTIVE.>\", \"$KV.MOOX_CLOUDNODE_JOB_ACTIVE.>\"]}\n      subscribe: {allow: [\"_INBOX.>\"]}\n"+
		"  - username: cloudnode-worker\n    password: %s\n    permissions:\n      publish: {allow: [\"$JS.API.CONSUMER.INFO.MOOX_CLOUDNODE_EXEC.>\", \"$JS.API.CONSUMER.MSG.NEXT.MOOX_CLOUDNODE_EXEC.>\", \"$JS.ACK.MOOX_CLOUDNODE_EXEC.>\"]}\n      subscribe: {allow: [\"_INBOX.>\"]}\n"+
		"  - username: factor-eventbus\n    password: %s\n    permissions:\n      publish: {allow: [\"$JS.API.STREAM.NAMES\", \"$JS.API.CONSUMER.INFO.*.factor_calc\", \"$JS.API.CONSUMER.CREATE.MOOX_STORAGE.factor_calc\", \"$JS.API.CONSUMER.CREATE.MOOX_STORAGE.factor_calc.>\", \"$JS.API.CONSUMER.DURABLE.CREATE.MOOX_STORAGE.factor_calc\", \"$JS.API.CONSUMER.MSG.NEXT.MOOX_STORAGE.factor_calc\", \"$JS.ACK.MOOX_STORAGE.factor_calc.>\"]}\n      subscribe: {allow: [\"_INBOX.>\"]}\n"+
		"  - username: strategy-eventbus\n    password: %s\n    permissions:\n      publish: {allow: [\"moox.trade.rebalance.requested.v1.>\"]}\n      subscribe: {allow: [\"_INBOX.>\"]}\n"+
		"  - username: trade-eventbus\n    password: %s\n    permissions:\n      publish: {allow: [\"$JS.API.STREAM.NAMES\", \"$JS.API.CONSUMER.INFO.*.trade_rebalance_v1\", \"$JS.API.CONSUMER.CREATE.MOOX_TRADE.trade_rebalance_v1\", \"$JS.API.CONSUMER.CREATE.MOOX_TRADE.trade_rebalance_v1.>\", \"$JS.API.CONSUMER.DURABLE.CREATE.MOOX_TRADE.trade_rebalance_v1\", \"$JS.API.CONSUMER.MSG.NEXT.MOOX_TRADE.trade_rebalance_v1\", \"$JS.ACK.MOOX_TRADE.trade_rebalance_v1.>\"]}\n      subscribe: {allow: [\"_INBOX.>\"]}\n",
		tokens["eventbus-internal-admin"], tokens["hostagent-publisher"], tokens["metrics-publisher"], tokens["monitor-hostmetrics-consumer"], tokens["monitor-metrics-consumer"], tokens["storage-eventbus"], tokens["archive-eventbus"], tokens["cloudnode-eventbus"], tokens["cloudnode-worker"], tokens["factor-eventbus"], tokens["strategy-eventbus"], tokens["trade-eventbus"])
}
func atomicSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".secret-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
func writeJSON(w io.Writer, value any) error { return json.NewEncoder(w).Encode(value) }

func makeTLSBundle(natsURL string) (eventbusBundle, error) {
	now := time.Now()
	caKey, _ := rsa.GenerateKey(rand.Reader, 3072)
	caTmpl := &x509.Certificate{SerialNumber: newSerial(), Subject: pkix.Name{CommonName: "MooX EventBus Private CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(10 * 365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return eventbusBundle{}, err
	}
	serverKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	serverTmpl := &x509.Certificate{SerialNumber: newSerial(), Subject: pkix.Name{CommonName: "MooX EventBus"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(5 * 365 * 24 * time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	parsed, err := url.Parse(natsURL)
	if err != nil {
		return eventbusBundle{}, err
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		serverTmpl.IPAddresses = append(serverTmpl.IPAddresses, ip)
	} else if host != "localhost" {
		serverTmpl.DNSNames = append(serverTmpl.DNSNames, host)
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTmpl, caTmpl, &serverKey.PublicKey, caKey)
	if err != nil {
		return eventbusBundle{}, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})
	return eventbusBundle{CA: string(caPEM), Cert: string(certPEM), Key: string(keyPEM), NATSURL: natsURL, CreatedAt: now}, nil
}
func newSerial() *big.Int {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return new(big.Int).SetBytes(b)
}
func rotateEventBus(d *dao.SecretDAO, credential string, confirm bool, out io.Writer) error {
	if !confirm {
		return errors.New("rotation requires --confirm; old token is invalidated immediately")
	}
	key, ok := eventBusKeys[credential]
	if !ok {
		return fmt.Errorf("unsupported credential %q", credential)
	}
	rows, err := listEventbus(d, trpc.BackgroundContext())
	if err != nil {
		return err
	}
	row, ok := rows[key]
	if !ok {
		return fmt.Errorf("credential %q is not provisioned", credential)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	row.SecretValue = base64.RawURLEncoding.EncodeToString(raw)
	if err := d.Update(trpc.BackgroundContext(), &row); err != nil {
		return err
	}
	return writeJSON(out, map[string]any{"status": "ok", "rotated": credential, "warning": "redeploy affected clients now; old token is invalid"})
}
