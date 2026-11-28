package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/mooyang-code/moox/packages/sqlitedb"
	"gorm.io/gorm"
	trpc "trpc.group/trpc-go/trpc-go"
)

// eventBusCAFile 是角色文件里写的根证书位置：角色文件在 secrets/eventbus/ 下，MooX CA 根证书在上一级的 secrets/ 下。
// EventBus 的服务端证书就是它所在主机的主机证书，客户端用这张根证书校验。
const eventBusCAFile = "../" + hostbundle.CAFile

func isEventBusCredentialsCommand(args []string) bool {
	return len(args) > 1 && args[1] == "eventbus-credentials"
}
func runEventBusCredentialsCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return errors.New("expected eventbus-credentials subcommand")
	}
	fs := flag.NewFlagSet("eventbus-credentials", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath, keyFile, nodeID, outputDir, credential, roles := "./data/admin.db", "", "", "", "", ""
	confirm, server := false, false
	fs.StringVar(&dbPath, "db-path", dbPath, "SQLite database path")
	fs.StringVar(&keyFile, "encryption-key-file", "", "0600 encryption key file")
	fs.StringVar(&nodeID, "node-id", "", "registered control host (the Admin host); export and export-clients read the EventBus location from the deployment records")
	fs.StringVar(&outputDir, "output-dir", "", "credential output directory")
	fs.StringVar(&credential, "credential", "", "role token to rotate")
	fs.StringVar(&roles, "roles", "", "comma-separated canonical client roles for export-clients")
	fs.BoolVar(&server, "server", false, "export-clients only: also export the broker user table for the host that runs EventBus")
	fs.BoolVar(&confirm, "confirm", false, "confirm rotation (the old token is invalidated immediately)")
	sub := args[1]
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if roles != "" && sub != "export-clients" {
		return errors.New("--roles is only valid for export-clients")
	}
	if server && sub != "export-clients" {
		return errors.New("--server is only valid for export-clients")
	}
	encryptionKey, err := loadCLIKey(keyFile)
	if err != nil {
		return err
	}
	db, err := openAdminCLIDB(dbPath)
	if err != nil {
		return err
	}
	defer closeAdminCLIDB(db)
	secretDAO := dao.NewSecretDAO(db, encryptionKey)
	switch sub {
	case "export-clients":
		return exportEventBusClients(context.Background(), db, encryptionKey, nodeID, outputDir, roles, server, stdout)
	case "ensure":
		var metadata bytes.Buffer
		if err := db.Transaction(func(tx *gorm.DB) error {
			return ensureEventBus(dao.NewSecretDAO(tx, encryptionKey), &metadata)
		}); err != nil {
			return err
		}
		_, err = stdout.Write(metadata.Bytes())
		return err
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

// EventBus 的地址跟随组件部署（可能在与 Admin 不同的主机上）；签发凭据不改动拓扑。
func eventBusNATSURL(db *gorm.DB, controlHostID string) (string, error) {
	endpoint, err := sysdeploy.ResolveEventBusEndpoint(context.Background(), db, controlHostID)
	return endpoint.URL, err
}

func openAdminCLIDB(path string) (*gorm.DB, error) {
	if path == "" {
		return nil, errors.New("db path is required")
	}
	return sqlitedb.OpenGORM(sqlitedb.Options{
		Path:          path,
		Synchronous:   "FULL",
		BusyTimeoutMS: 10000,
		SkipCreateDir: true,
	})
}
func closeAdminCLIDB(db *gorm.DB) {
	if db != nil {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
}

// loadCLIKey 读取已由 bootstrap 生成的 0600 主密钥文件并显式返回；不生成第二套主密钥，也不写进程环境变量。
func loadCLIKey(keyFile string) (string, error) {
	if strings.TrimSpace(keyFile) == "" {
		return "", errors.New("--encryption-key-file is required")
	}
	info, err := os.Lstat(keyFile)
	if err != nil {
		return "", fmt.Errorf("stat encryption key file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return "", errors.New("encryption key file must be a regular 0600 file")
	}
	raw, err := os.ReadFile(filepath.Clean(keyFile))
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return "", errors.New("encryption key file is empty")
	}
	return key, nil
}

// ensureEventBus 给缺少的角色生成令牌，已有的原样保留。EventBus 没有自己的证书：服务端用所在主机的主机证书。
func ensureEventBus(d *dao.SecretDAO, out io.Writer) error {
	ctx := trpc.BackgroundContext()
	for _, role := range eventBusRoles {
		if _, err := ensureToken(ctx, d, eventBusKeys[role], role); err != nil {
			return err
		}
	}
	return writeJSON(out, map[string]any{"status": "ok", "roles": eventBusRoles})
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
	err = d.Create(ctx, &model.Secret{SecretID: uuid.New().String(), Name: "EventBus " + role, Description: "permanent EventBus role token", Category: "eventbus", Provider: "moox_eventbus", SecretType: "token", KeyID: key, SecretValue: value, ExtraConfig: `{}`})
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
	roleFiles := eventBusRoleFiles()
	for role, name := range roleFiles {
		// 导出里写的是对外地址；各单元安装时会按"与 EventBus 是否同机"改写成实际使用的地址。
		content := fmt.Sprintf("version: 1\nurls:\n  - %s\nusername: %s\ntoken: %s\nca_file: %s\n", natsURL, role, tokens[role], eventBusCAFile)
		if err := atomicSecretFile(filepath.Join(dir, name), []byte(content)); err != nil {
			return err
		}
	}
	return writeJSON(out, map[string]any{"status": "ok", "output_dir": dir, "roles": eventBusRoles})
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
